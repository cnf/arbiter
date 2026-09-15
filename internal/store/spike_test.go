package store

import (
	"context"
	"database/sql"
	"embed"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// schemaFS embeds the schema so applying it needs no filesystem path — the
// same way the eventual writer will create the table on startup.
//
//go:embed schema.sql
var schemaFS embed.FS

// openMemory applies the schema to a fresh in-memory database and returns the
// sqlc handle. This is the spike's whole point: prove that the pure-Go driver
// registers under "sqlite" and behaves the way sqlc's generated code assumes.
func openMemory(t *testing.T) (*sql.DB, *Queries) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite driver: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		t.Fatalf("read embedded schema: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), string(schema)); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return db, New(db)
}

func strptr(s string) *string { return &s }

// TestSchemaAppliesAndRoundTrips is the load-bearing spike: the schema must
// apply as written, and a row written through sqlc's generated insert must read
// back with every field intact, including the pointer-typed nullable columns
// and the time.Time-mapped TIMESTAMP.
func TestSchemaAppliesAndRoundTrips(t *testing.T) {
	db, q := openMemory(t)
	ctx := context.Background()

	ts := time.Date(2026, 9, 15, 14, 30, 0, 0, time.UTC)
	params := InsertRequestParams{
		TraceID:          "trace-abc",
		SessionKey:       strptr("session-1"),
		ClientID:         nil, // unpopulated until per-client API keys land
		Ts:               ts,
		Format:           "anthropic",
		Provider:         "claude",
		Model:            "claude-3-haiku-20240307",
		AliasUsed:        strptr("cheap-claude"),
		RoutingRationale: "policy rule matched domain=code_generation",
		Domain:           strptr("code_generation"),
		Effort:           strptr("easy"),
		CostClass:        strptr("budget"),
		Confidence:       ptr(0.82),
		InputTokens:      1200,
		OutputTokens:     340,
		CacheReadTokens:  100,
		CacheWriteTokens: 0,
		CostUsd:          0.000725,
		LatencyMs:        910,
		StatusCode:       200,
		Error:            nil,
		Stream:           true,
		ToolCallsJson:    strptr(`["read_file","grep"]`),
	}

	id, err := q.InsertRequest(ctx, params)
	if err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}
	if id <= 0 {
		t.Fatalf("InsertRequest returned id %d, want positive rowid", id)
	}

	var got Request
	row := db.QueryRowContext(ctx, `SELECT id, trace_id, session_key, client_id, ts, format, provider, model,
        alias_used, routing_rationale, domain, effort, cost_class, confidence,
        input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
        cost_usd, latency_ms, status_code, error, stream, tool_calls_json
        FROM requests WHERE id = ?`, id)
	if err := row.Scan(
		&got.ID, &got.TraceID, &got.SessionKey, &got.ClientID, &got.Ts, &got.Format, &got.Provider, &got.Model,
		&got.AliasUsed, &got.RoutingRationale, &got.Domain, &got.Effort, &got.CostClass, &got.Confidence,
		&got.InputTokens, &got.OutputTokens, &got.CacheReadTokens, &got.CacheWriteTokens,
		&got.CostUsd, &got.LatencyMs, &got.StatusCode, &got.Error, &got.Stream, &got.ToolCallsJson,
	); err != nil {
		t.Fatalf("scan back: %v", err)
	}

	if got.TraceID != params.TraceID || got.Provider != params.Provider || got.Model != params.Model {
		t.Errorf("identity fields changed: %+v", got)
	}
	if got.SessionKey == nil || *got.SessionKey != "session-1" {
		t.Errorf("session_key = %v, want session-1", got.SessionKey)
	}
	if got.ClientID != nil {
		t.Errorf("client_id = %v, want NULL (unset until API keys land)", *got.ClientID)
	}
	if got.AliasUsed == nil || *got.AliasUsed != "cheap-claude" {
		t.Errorf("alias_used = %v, want cheap-claude", got.AliasUsed)
	}
	if got.Error != nil {
		t.Errorf("error = %v, want NULL", *got.Error)
	}
	if !got.Stream {
		t.Error("stream round-tripped as false, want true")
	}
	if got.InputTokens != 1200 || got.OutputTokens != 340 {
		t.Errorf("tokens = %d/%d, want 1200/340", got.InputTokens, got.OutputTokens)
	}
	if got.CostUsd != 0.000725 {
		t.Errorf("cost_usd = %v, want 0.000725", got.CostUsd)
	}
	if !got.Ts.Equal(ts) {
		t.Errorf("ts = %v, want %v (TIMESTAMP round-trip)", got.Ts, ts)
	}
}

// TestNullableSessionKeyInserts proves a request whose affinity derivation
// declined to produce a key still gets a row — the plan calls this out
// explicitly, since NULL-ing it must not mean losing the request.
func TestNullableSessionKeyInserts(t *testing.T) {
	_, q := openMemory(t)
	ctx := context.Background()

	id, err := q.InsertRequest(ctx, InsertRequestParams{
		TraceID:          "trace-no-session",
		SessionKey:       nil,
		Ts:               time.Now().UTC(),
		Format:           "openai",
		Provider:         "mockllm",
		Model:            "mock-llm",
		RoutingRationale: "simple router default",
		LatencyMs:        12,
		StatusCode:       200,
	})
	if err != nil {
		t.Fatalf("InsertRequest with nil session_key: %v", err)
	}
	if id <= 0 {
		t.Fatalf("got id %d, want positive", id)
	}
}

// TestDuplicateTraceIDIsKept is the reason trace_id is not the primary key:
// the inbound header is trusted verbatim, so two rows sharing a trace_id must
// both insert rather than colliding.
func TestDuplicateTraceIDIsKept(t *testing.T) {
	db, q := openMemory(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := q.InsertRequest(ctx, InsertRequestParams{
			TraceID:          "same-trace",
			Ts:               time.Now().UTC(),
			Format:           "openai",
			Provider:         "mockllm",
			Model:            "mock-llm",
			RoutingRationale: "r",
			StatusCode:       200,
		}); err != nil {
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

// TestRecentRequestsOrdersByTs exercises the generated :many query, including
// the LIMIT placeholder and the time.Time scan.
func TestRecentRequestsOrdersByTs(t *testing.T) {
	_, q := openMemory(t)
	ctx := context.Background()

	base := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	for i, model := range []string{"old", "mid", "new"} {
		if _, err := q.InsertRequest(ctx, InsertRequestParams{
			TraceID:          model,
			Ts:               base.Add(time.Duration(i) * time.Minute),
			Format:           "openai",
			Provider:         "mockllm",
			Model:            model,
			RoutingRationale: "r",
			StatusCode:       200,
		}); err != nil {
			t.Fatalf("insert %s: %v", model, err)
		}
	}

	rows, err := q.RecentRequests(ctx, 2)
	if err != nil {
		t.Fatalf("RecentRequests: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (LIMIT)", len(rows))
	}
	if rows[0].Model != "new" || rows[1].Model != "mid" {
		t.Errorf("ordering wrong: got %q, %q; want new, mid", rows[0].Model, rows[1].Model)
	}
}

// TestSchemaIsIdempotent proves CREATE TABLE/INDEX IF NOT EXISTS tolerate being
// applied twice, since startup applies the schema unconditionally.
func TestSchemaIsIdempotent(t *testing.T) {
	db, _ := openMemory(t)
	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), string(schema)); err != nil {
		t.Fatalf("re-applying the schema must be a no-op, got: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }
