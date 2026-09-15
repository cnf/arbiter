package store

import (
	"context"
	"database/sql"
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
		Effort:           "easy",
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

	for _, col := range []string{"session_key", "client_id", "alias_used", "domain", "effort", "cost_class", "error", "tool_calls_json", "config_epoch"} {
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
