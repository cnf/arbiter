package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/cnf/arbiter/pkg/types"

	_ "modernc.org/sqlite"
)

// openMemory applies the schema to a fresh in-memory database and returns the
// handle. There is no generated query layer any more (sqlc was retired — see
// writer.go's insertRequest), so tests drive plain database/sql.
func openMemory(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite driver: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	applySchema(t, db)
	return db
}

// applySchema executes the embedded schema against db. Shared with the
// idempotence test so both apply it the same way.
func applySchema(t *testing.T, db *sql.DB) {
	t.Helper()
	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		t.Fatalf("read embedded schema: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), string(schema)); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
}

// TestSchemaAppliesAndRoundTrips is the load-bearing schema test: the DDL must
// apply as written (sqlite validates it at Exec time — nothing else checks it,
// which is why retiring sqlc costs no schema validation), and a row written
// through the hand-written insert must read back with every field intact,
// including the nullable columns and the TIMESTAMP mapping.
func TestSchemaAppliesAndRoundTrips(t *testing.T) {
	db := openMemory(t)
	ctx := context.Background()

	ts := time.Date(2026, 9, 15, 14, 30, 0, 0, time.UTC)
	ev := Event{
		TraceID:          "trace-abc",
		SessionKey:       "session-1",
		ClientID:         "", // unpopulated until per-client API keys land
		Ts:               ts,
		Format:           "anthropic",
		Provider:         "claude",
		Model:            "claude-3-haiku-20240307",
		AliasUsed:        "cheap-claude",
		RoutingRationale: "policy rule matched domain=code_generation",
		Domain:           "code_generation",
		Difficulty:       "easy",
		CostClass:        "budget",
		Confidence:       0.82,
		Usage:            types.Usage{InputTokens: 1200, OutputTokens: 340, CacheRead: 100, CacheWrite: 0, CostUSD: 0.000725},
		LatencyMs:        910,
		StatusCode:       200,
		Stream:           true,
		ToolCalls:        []string{"read_file", "grep"},
	}

	if err := insertRequest(ctx, db, ev); err != nil {
		t.Fatalf("insertRequest: %v", err)
	}

	// Read it back through the Reader's detail path, which is the same shape
	// the API serves.
	r := &Reader{db: db}
	rows, err := r.ListRequests(ctx, RequestFilter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	id := rows[0].ID

	got, ok, err := r.GetRequest(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetRequest = ok %v, err %v", ok, err)
	}
	if got.TraceID != "trace-abc" || got.Provider != "claude" || got.Model != "claude-3-haiku-20240307" {
		t.Errorf("identity fields changed: %+v", got.RequestRow)
	}
	if got.SessionKey != "session-1" {
		t.Errorf("session_key = %q, want session-1", got.SessionKey)
	}
	if got.ClientID != "" {
		t.Errorf("client_id = %q, want empty (NULL until API keys land)", got.ClientID)
	}
	if got.AliasUsed != "cheap-claude" {
		t.Errorf("alias_used = %q, want cheap-claude", got.AliasUsed)
	}
	if got.Error != "" {
		t.Errorf("error = %q, want empty (NULL)", got.Error)
	}
	if !got.Stream {
		t.Error("stream round-tripped as false, want true")
	}
	if got.InputTokens != 1200 || got.OutputTokens != 340 {
		t.Errorf("tokens = %d/%d, want 1200/340", got.InputTokens, got.OutputTokens)
	}
	if got.CacheReadTokens != 100 || got.CacheWriteTokens != 0 {
		t.Errorf("cache tokens = %d/%d, want 100/0", got.CacheReadTokens, got.CacheWriteTokens)
	}
	if got.CostUSD != 0.000725 {
		t.Errorf("cost_usd = %v, want 0.000725", got.CostUSD)
	}
	if got.LatencyMs != 910 || got.StatusCode != 200 {
		t.Errorf("latency/status = %d/%d, want 910/200", got.LatencyMs, got.StatusCode)
	}
	if got.ToolCalls != `["read_file","grep"]` {
		t.Errorf("tool_calls = %q, want the JSON array", got.ToolCalls)
	}
	// The TIMESTAMP column has to survive as an instant, not a string: parse
	// it back and compare. (formatTime renders RFC3339 in UTC.)
	if parsed, err := time.Parse(time.RFC3339, got.Ts); err != nil {
		t.Errorf("ts %q is not RFC3339: %v", got.Ts, err)
	} else if !parsed.Equal(ts) {
		t.Errorf("ts = %v, want %v (TIMESTAMP round-trip)", parsed, ts)
	}
}

// TestNullableSessionKeyInserts proves a request whose affinity derivation
// declined to produce a key still gets a row — a NULL session_key must not
// mean losing the request.
func TestNullableSessionKeyInserts(t *testing.T) {
	db := openMemory(t)
	ctx := context.Background()

	ev := Event{
		TraceID:          "trace-no-session",
		SessionKey:       "", // no usable session key
		Ts:               time.Now().UTC(),
		Format:           "openai",
		Provider:         "mockllm",
		Model:            "mock-llm",
		RoutingRationale: "simple router default",
		LatencyMs:        12,
		StatusCode:       200,
	}
	if err := insertRequest(ctx, db, ev); err != nil {
		t.Fatalf("insertRequest with empty session_key: %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM requests WHERE session_key IS NULL`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("got %d rows with NULL session_key, want 1", n)
	}
}

// TestDuplicateTraceIDIsKept is the reason trace_id is not the primary key:
// the inbound header is trusted verbatim, so two rows sharing a trace_id must
// both insert rather than colliding.
func TestDuplicateTraceIDIsKept(t *testing.T) {
	db := openMemory(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		ev := Event{
			TraceID:          "same-trace",
			Ts:               time.Now().UTC(),
			Format:           "openai",
			Provider:         "mockllm",
			Model:            "mock-llm",
			RoutingRationale: "r",
			StatusCode:       200,
		}
		if err := insertRequest(ctx, db, ev); err != nil {
			t.Fatalf("insert %d with duplicate trace_id: %v", i, err)
		}
	}

	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM requests WHERE trace_id = 'same-trace'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("got %d rows for a duplicated trace_id, want 2", n)
	}
}

// TestSchemaIsIdempotent proves CREATE TABLE/INDEX IF NOT EXISTS tolerate being
// applied twice, since startup applies the schema unconditionally.
func TestSchemaIsIdempotent(t *testing.T) {
	db := openMemory(t)
	applySchema(t, db)
}

// TestMigrationAddsRequestKindToAnExistingTable is the upgrade path, and it is
// the one that matters in production: the deployment's database was created
// before request_kind existed, and CREATE TABLE IF NOT EXISTS is a no-op on an
// existing table — so the column only ever appears via the explicit
// addColumnIfMissing pass. Without that pass the insert references a missing
// column and EVERY event is silently lost, which is the failure mode the
// migration loop's own comment warns about.
//
// It builds the pre-change table shape, migrates it, and proves both that the
// column appears and that the new insert writes to it.
func TestMigrationAddsRequestKindToAnExistingTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")

	// A database as it existed before this change: the real pre-change table
	// shape, minus request_kind. The full column list rather than a minimal
	// stub, because the schema's own indexes reference these columns and are
	// applied on top of it.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `CREATE TABLE requests (
		id                    INTEGER PRIMARY KEY,
		trace_id              TEXT NOT NULL,
		session_key           TEXT,
		client_id             TEXT,
		ts                    TIMESTAMP NOT NULL,
		format                TEXT NOT NULL,
		provider              TEXT NOT NULL,
		model                 TEXT NOT NULL,
		actual_model          TEXT,
		alias_used            TEXT,
		routing_rationale     TEXT NOT NULL,
		domain                TEXT,
		difficulty            TEXT,
		cost_class            TEXT,
		confidence            REAL,
		input_tokens          INTEGER NOT NULL DEFAULT 0,
		output_tokens         INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
		cache_write_tokens    INTEGER NOT NULL DEFAULT 0,
		cost_usd              REAL NOT NULL DEFAULT 0,
		latency_ms            INTEGER NOT NULL,
		status_code           INTEGER NOT NULL,
		error                 TEXT,
		stream                BOOLEAN NOT NULL,
		tool_calls_json       TEXT,
		config_epoch          TEXT,
		headers_json          TEXT,
		kind                  TEXT NOT NULL DEFAULT 'client'
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	// Startup: this applies the schema (a no-op on the existing table) and
	// then the explicit column pass.
	w, err := NewSQLiteWriter(path, &recordingLogger{})
	if err != nil {
		t.Fatalf("NewSQLiteWriter on a pre-change database: %v", err)
	}

	w.Record(Event{
		TraceID:          "trace-migrated",
		Format:           "openai",
		Provider:         "openrouter",
		Model:            "@preset/deepseek-flash",
		RoutingRationale: `explicit model "@preset/deepseek-flash" -> provider "openrouter"`,
		RequestKind:      "title",
		// tags_json also rides the addColumnIfMissing pass; the legacy table
		// below predates it, so this insert is itself a guard that the
		// column pass ran (an insert naming a missing column fails every
		// time — the event-loss failure mode the loop warns about).
		Tags:       []string{"python"},
		StatusCode: 502,
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := &Reader{db: reopenReads(t, path)}
	rows, err := r.ListRequests(ctx, RequestFilter{RequestKind: "title"})
	if err != nil {
		t.Fatalf("ListRequests after migration: %v", err)
	}
	if len(rows) != 1 || rows[0].RequestKind != "title" {
		t.Fatalf("after migration rows = %+v, want one row with request_kind=title", rows)
	}
	if len(rows[0].Tags) != 1 || rows[0].Tags[0] != "python" {
		t.Fatalf("after migration tags = %v, want [python]", rows[0].Tags)
	}
}

// TestMigrationRenamesEffortToDifficulty is the upgrade path for #79: the
// routing axis column was renamed, not added, so a database created before the
// rename still has `effort` while every read/write now names `difficulty`. A
// plain ADD COLUMN cannot express it and CREATE TABLE IF NOT EXISTS is a no-op
// against the existing table, so without renameColumnIfPresent every row after
// the change would fail to insert (unknown column) — silently losing events.
//
// Covers both guards: the rename happens on a database that still has `effort`,
// the pre-existing value survives, and running startup a second time (the
// column now already named `difficulty`) is a no-op rather than an error.
func TestMigrationRenamesEffortToDifficulty(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-effort.db")

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `CREATE TABLE requests (
		id                    INTEGER PRIMARY KEY,
		trace_id              TEXT NOT NULL,
		session_key           TEXT,
		client_id             TEXT,
		ts                    TIMESTAMP NOT NULL,
		format                TEXT NOT NULL,
		provider              TEXT NOT NULL,
		model                 TEXT NOT NULL,
		actual_model          TEXT,
		alias_used            TEXT,
		routing_rationale     TEXT NOT NULL,
		domain                TEXT,
		effort                TEXT,
		cost_class            TEXT,
		confidence            REAL,
		input_tokens          INTEGER NOT NULL DEFAULT 0,
		output_tokens         INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
		cache_write_tokens    INTEGER NOT NULL DEFAULT 0,
		cost_usd              REAL NOT NULL DEFAULT 0,
		latency_ms            INTEGER NOT NULL,
		status_code           INTEGER NOT NULL,
		error                 TEXT,
		stream                BOOLEAN NOT NULL,
		tool_calls_json       TEXT,
		config_epoch          TEXT,
		headers_json          TEXT,
		kind                  TEXT NOT NULL DEFAULT 'client',
		request_kind          TEXT
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	// A row written under the old spelling, so the value surviving the rename
	// is proven and not just the column name.
	if _, err := legacy.ExecContext(ctx, `INSERT INTO requests
		(trace_id, ts, format, provider, model, routing_rationale, domain, effort, latency_ms, status_code, stream)
		VALUES ('trace-effort', CURRENT_TIMESTAMP, 'openai', 'openrouter', 'm', 'legacy', 'code_generation', 'hard', 10, 200, 0)`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	w, err := NewSQLiteWriter(path, &recordingLogger{})
	if err != nil {
		t.Fatalf("NewSQLiteWriter on a pre-rename database: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Startup a second time: the column is already `difficulty`, so
	// renameColumnIfPresent must do nothing rather than fail on a missing
	// `effort`.
	w2, err := NewSQLiteWriter(path, &recordingLogger{})
	if err != nil {
		t.Fatalf("NewSQLiteWriter second run (must be idempotent): %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("Close second run: %v", err)
	}

	r := &Reader{db: reopenReads(t, path)}
	rows, err := r.ListRequests(ctx, RequestFilter{})
	if err != nil {
		t.Fatalf("ListRequests after migration: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("after migration rows = %d, want 1", len(rows))
	}
	if rows[0].Difficulty != "hard" {
		t.Errorf("Difficulty = %q, want hard — the pre-rename value must survive", rows[0].Difficulty)
	}
}

// TestMigrationAddsNameToContentRefs is the upgrade path for #22: a database
// created before content_refs.name existed has content_refs without that
// column, and CREATE TABLE IF NOT EXISTS is a no-op against it — so, exactly
// like request_kind above, the column only ever appears via the explicit
// addColumnIfMissing pass. Without it, writeContent's insert references a
// missing column and every captured block is lost.
func TestMigrationAddsNameToContentRefs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-content-refs.db")

	// The pre-change content_refs shape, minus name. content/requests are
	// also needed: writeContent's FK-less design still requires the content
	// table to exist for the INSERT OR IGNORE, and the schema's own indexes
	// reference content_refs' pre-existing columns.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `CREATE TABLE content (
		hash BLOB PRIMARY KEY,
		kind TEXT NOT NULL,
		body BLOB
	)`); err != nil {
		t.Fatalf("create legacy content table: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `CREATE TABLE content_refs (
		owner_kind TEXT NOT NULL,
		owner_id INTEGER NOT NULL,
		direction TEXT NOT NULL,
		msg_index INTEGER NOT NULL,
		position INTEGER NOT NULL,
		role TEXT,
		block_type TEXT NOT NULL,
		hash BLOB NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy content_refs table: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	// Startup: applies the schema (a no-op on the existing tables) and then
	// the explicit column pass.
	w, err := NewSQLiteWriter(path, &recordingLogger{})
	if err != nil {
		t.Fatalf("NewSQLiteWriter on a pre-change database: %v", err)
	}

	req := &types.NormalizedRequest{
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{
				types.AttachmentBlock("application/pdf", "ZmFrZS1wZGYtYnl0ZXM=", "report.pdf", false),
			}},
		},
	}
	w.Record(Event{
		TraceID: "trace-migrated-attachment", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: CaptureRequest(req)},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := &Reader{db: reopenReads(t, path)}
	rows, err := r.ListRequests(ctx, RequestFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListRequests after migration = %v, %v", rows, err)
	}
	blocks, _, err := r.ContentForRequest(ctx, rows[0].ID, false)
	if err != nil {
		t.Fatalf("ContentForRequest after migration: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Name != "report.pdf" {
		t.Fatalf("after migration blocks = %+v, want one block named report.pdf", blocks)
	}
}

// TestEmptyStringsBecomeNull proves an Event's unset nullable fields are
// written as NULL rather than "", so "absent" and "empty" are not two states
// in the database.
func TestEmptyStringsBecomeNull(t *testing.T) {
	db := openMemory(t)
	ctx := context.Background()

	ev := Event{
		TraceID:          "trace-nulls",
		Format:           "openai",
		Provider:         "mockllm",
		Model:            "mock-llm",
		RoutingRationale: "r",
		StatusCode:       200,
	}
	if err := insertRequest(ctx, db, ev); err != nil {
		t.Fatalf("insertRequest: %v", err)
	}

	for _, col := range []string{"session_key", "client_id", "alias_used", "domain", "difficulty", "cost_class", "error", "tool_calls_json", "config_epoch"} {
		var isNull int
		q := `SELECT ` + col + ` IS NULL FROM requests WHERE trace_id = 'trace-nulls'`
		if err := db.QueryRowContext(ctx, q).Scan(&isNull); err != nil {
			t.Fatalf("%s: %v", col, err)
		}
		if isNull != 1 {
			t.Errorf("%s is not NULL for an unset Event field", col)
		}
	}
}

// TestCapabilitiesRoundTrip is the store half of the classified-signals work:
// required_capabilities_json has three meaningful states and all three must
// survive a write and a read. NULL means classification never ran; [] means it
// ran and no classifier named a capability; a populated list is what matched.
// Collapsing [] into NULL is exactly the defect this column exists to fix — a
// request that WAS classified then looked identical to one that was not.
func TestCapabilitiesRoundTrip(t *testing.T) {
	db := openMemory(t)
	ctx := context.Background()

	cases := []struct {
		trace string
		caps  []string
		raw   string // stored JSON text; "NULL" means SQL NULL
	}{
		{"caps-nil", nil, "NULL"},
		{"caps-empty", []string{}, "[]"},
		{"caps-present", []string{"vision", "tool_use"}, `["vision","tool_use"]`},
	}
	for _, tc := range cases {
		ev := Event{
			TraceID: tc.trace, Format: "openai", Provider: "p", Model: "m",
			StatusCode: 200, RequiredCapabilities: tc.caps,
		}
		if err := insertRequest(ctx, db, ev); err != nil {
			t.Fatalf("insert %s: %v", tc.trace, err)
		}
		var raw sql.NullString
		if err := db.QueryRowContext(ctx,
			`SELECT required_capabilities_json FROM requests WHERE trace_id = ?`, tc.trace).Scan(&raw); err != nil {
			t.Fatalf("read %s: %v", tc.trace, err)
		}
		if tc.raw == "NULL" {
			if raw.Valid {
				t.Errorf("%s stored %q, want SQL NULL", tc.trace, raw.String)
			}
			continue
		}
		if !raw.Valid || raw.String != tc.raw {
			t.Errorf("%s stored %q (valid %v), want %s", tc.trace, raw.String, raw.Valid, tc.raw)
		}
	}

	// Read back through both projections: the list row and the detail row are
	// separate scans with separate column lists, so a column dropped from
	// either would silently flatten the distinction the write side keeps.
	r := &Reader{db: db}
	rows, err := r.ListRequests(ctx, RequestFilter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	byTrace := make(map[string]RequestRow, len(rows))
	for _, row := range rows {
		byTrace[row.TraceID] = row
	}
	for _, tc := range cases {
		if _, ok := byTrace[tc.trace]; !ok {
			t.Fatalf("ListRequests did not return %s", tc.trace)
		}
	}

	if got := byTrace["caps-nil"].RequiredCapabilities; got != nil {
		t.Errorf("list: NULL capabilities read back as %v, want nil", got)
	}
	if got := byTrace["caps-empty"].RequiredCapabilities; got == nil || len(got) != 0 {
		t.Errorf("list: [] capabilities read back as %v (nil %v), want empty non-nil", got, got == nil)
	}
	if got := byTrace["caps-present"].RequiredCapabilities; len(got) != 2 || got[0] != "vision" || got[1] != "tool_use" {
		t.Errorf("list: capabilities = %v, want [vision tool_use]", got)
	}

	detail, ok, err := r.GetRequest(ctx, byTrace["caps-empty"].ID)
	if err != nil || !ok {
		t.Fatalf("GetRequest(empty) = ok %v, err %v", ok, err)
	}
	if detail.RequiredCapabilities == nil {
		t.Error("detail: [] capabilities read back as nil, want empty non-nil")
	}
	detail, ok, err = r.GetRequest(ctx, byTrace["caps-present"].ID)
	if err != nil || !ok {
		t.Fatalf("GetRequest(present) = ok %v, err %v", ok, err)
	}
	if len(detail.RequiredCapabilities) != 2 {
		t.Errorf("detail: capabilities = %v, want 2 entries", detail.RequiredCapabilities)
	}
	detail, ok, err = r.GetRequest(ctx, byTrace["caps-nil"].ID)
	if err != nil || !ok {
		t.Fatalf("GetRequest(nil) = ok %v, err %v", ok, err)
	}
	if detail.RequiredCapabilities != nil {
		t.Errorf("detail: NULL capabilities read back as %v, want nil", detail.RequiredCapabilities)
	}
}

// TestTagsRoundTrip is the store half of the tags axis: tags_json has the same
// three meaningful states as required_capabilities_json, and all three must
// survive a write and a read through BOTH projections. Tags get their own
// column rather than sharing the capabilities one, so this also guards that
// the two never bleed into each other.
func TestTagsRoundTrip(t *testing.T) {
	db := openMemory(t)
	ctx := context.Background()

	cases := []struct {
		trace string
		tags  []string
		raw   string // stored JSON text; "NULL" means SQL NULL
	}{
		{"tags-nil", nil, "NULL"},
		{"tags-empty", []string{}, "[]"},
		{"tags-present", []string{"python", "french"}, `["python","french"]`},
	}
	for _, tc := range cases {
		ev := Event{
			TraceID: tc.trace, Format: "openai", Provider: "p", Model: "m",
			StatusCode: 200, Tags: tc.tags,
			// capabilities deliberately set on the same row to prove the two
			// columns are independent.
			RequiredCapabilities: []string{"vision"},
		}
		if err := insertRequest(ctx, db, ev); err != nil {
			t.Fatalf("insert %s: %v", tc.trace, err)
		}
		var raw sql.NullString
		if err := db.QueryRowContext(ctx,
			`SELECT tags_json FROM requests WHERE trace_id = ?`, tc.trace).Scan(&raw); err != nil {
			t.Fatalf("read %s: %v", tc.trace, err)
		}
		if tc.raw == "NULL" {
			if raw.Valid {
				t.Errorf("%s stored %q, want SQL NULL", tc.trace, raw.String)
			}
			continue
		}
		if !raw.Valid || raw.String != tc.raw {
			t.Errorf("%s stored %q (valid %v), want %s", tc.trace, raw.String, raw.Valid, tc.raw)
		}
	}

	// Both projections: the list row and the detail row are separate scans
	// with separate column lists, so a column dropped from either would
	// silently flatten the distinction the write side keeps.
	r := &Reader{db: db}
	rows, err := r.ListRequests(ctx, RequestFilter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	byTrace := make(map[string]RequestRow, len(rows))
	for _, row := range rows {
		byTrace[row.TraceID] = row
	}
	for _, tc := range cases {
		if _, ok := byTrace[tc.trace]; !ok {
			t.Fatalf("ListRequests did not return %s", tc.trace)
		}
	}

	if got := byTrace["tags-nil"].Tags; got != nil {
		t.Errorf("list: NULL tags read back as %v, want nil", got)
	}
	if got := byTrace["tags-empty"].Tags; got == nil || len(got) != 0 {
		t.Errorf("list: [] tags read back as %v (nil %v), want empty non-nil", got, got == nil)
	}
	if got := byTrace["tags-present"].Tags; len(got) != 2 || got[0] != "python" || got[1] != "french" {
		t.Errorf("list: tags = %v, want [python french]", got)
	}
	// The capabilities column on the same rows must be untouched by tags.
	if got := byTrace["tags-nil"].RequiredCapabilities; len(got) != 1 || got[0] != "vision" {
		t.Errorf("list: capabilities = %v, want [vision] (tags must not clobber it)", got)
	}

	detail, ok, err := r.GetRequest(ctx, byTrace["tags-empty"].ID)
	if err != nil || !ok {
		t.Fatalf("GetRequest(empty) = ok %v, err %v", ok, err)
	}
	if detail.Tags == nil {
		t.Error("detail: [] tags read back as nil, want empty non-nil")
	}
	detail, ok, err = r.GetRequest(ctx, byTrace["tags-present"].ID)
	if err != nil || !ok {
		t.Fatalf("GetRequest(present) = ok %v, err %v", ok, err)
	}
	if len(detail.Tags) != 2 || detail.Tags[0] != "python" || detail.Tags[1] != "french" {
		t.Errorf("detail: tags = %v, want [python french]", detail.Tags)
	}
	if len(detail.RequiredCapabilities) != 1 || detail.RequiredCapabilities[0] != "vision" {
		t.Errorf("detail: capabilities = %v, want [vision] (tags must not clobber it)", detail.RequiredCapabilities)
	}
	detail, ok, err = r.GetRequest(ctx, byTrace["tags-nil"].ID)
	if err != nil || !ok {
		t.Fatalf("GetRequest(nil) = ok %v, err %v", ok, err)
	}
	if detail.Tags != nil {
		t.Errorf("detail: NULL tags read back as %v, want nil", detail.Tags)
	}
}

// TestClientEffortRoundTrip is the store half of #79 phase 4: the reasoning
// effort the CLIENT requested is a request fact and must survive a write and a
// read through BOTH projections. The list row and the detail row are separate
// scans with separate column lists, so a column dropped from either would
// silently read back empty while the write side kept it — the same trap the
// capabilities column-set has.
//
// Two states, not three: there is no "locked by a force alias" state yet
// (that is phase 3, deferred), so the stored value is always what the client
// sent. NULL (empty) means the client asked for none.
func TestClientEffortRoundTrip(t *testing.T) {
	db := openMemory(t)
	ctx := context.Background()

	cases := []struct {
		trace        string
		clientEffort string
		raw          string // stored text; "NULL" means SQL NULL
	}{
		{"effort-none", "", "NULL"},
		{"effort-high", "high", "high"},
		{"effort-longscale", "extrahigh", "extrahigh"}, // Hermes' longer vocabulary, verbatim
	}
	for _, tc := range cases {
		ev := Event{
			TraceID: tc.trace, Format: "openai", Provider: "p", Model: "m",
			StatusCode: 200, ClientEffort: tc.clientEffort,
		}
		if err := insertRequest(ctx, db, ev); err != nil {
			t.Fatalf("insert %s: %v", tc.trace, err)
		}
		var raw sql.NullString
		if err := db.QueryRowContext(ctx,
			`SELECT client_effort FROM requests WHERE trace_id = ?`, tc.trace).Scan(&raw); err != nil {
			t.Fatalf("read %s: %v", tc.trace, err)
		}
		if tc.raw == "NULL" {
			if raw.Valid {
				t.Errorf("%s stored %q, want SQL NULL", tc.trace, raw.String)
			}
			continue
		}
		if !raw.Valid || raw.String != tc.raw {
			t.Errorf("%s stored %q (valid %v), want %s", tc.trace, raw.String, raw.Valid, tc.raw)
		}
	}

	// Read back through the list projection and the detail projection — two
	// independent column lists.
	r := &Reader{db: db}
	rows, err := r.ListRequests(ctx, RequestFilter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	byTrace := make(map[string]RequestRow, len(rows))
	for _, row := range rows {
		byTrace[row.TraceID] = row
	}
	for _, tc := range cases {
		row, ok := byTrace[tc.trace]
		if !ok {
			t.Fatalf("ListRequests did not return %s", tc.trace)
		}
		if row.ClientEffort != tc.clientEffort {
			t.Errorf("list: %s client effort = %q, want %q", tc.trace, row.ClientEffort, tc.clientEffort)
		}
		detail, ok, err := r.GetRequest(ctx, row.ID)
		if err != nil || !ok {
			t.Fatalf("GetRequest(%s) = ok %v, err %v", tc.trace, ok, err)
		}
		if detail.ClientEffort != tc.clientEffort {
			t.Errorf("detail: %s client effort = %q, want %q", tc.trace, detail.ClientEffort, tc.clientEffort)
		}
	}
}
