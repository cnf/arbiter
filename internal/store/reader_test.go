package store

import (
	"context"
	"testing"
	"time"

	"github.com/cnf/arbiter/pkg/types"
)

// newTestReader applies the schema to a fresh in-memory database and wraps it
// in a Reader, so read tests exercise the same SQL a real deployment runs
// without going through the writer's queue/goroutine.
func newTestReader(t *testing.T) (*Reader, *Queries) {
	t.Helper()
	db, q := openMemory(t)
	return &Reader{db: db}, q
}

func insertRow(t *testing.T, q *Queries, p InsertRequestParams) {
	t.Helper()
	if p.TraceID == "" {
		p.TraceID = "trace"
	}
	if p.Format == "" {
		p.Format = "openai"
	}
	if p.Provider == "" {
		p.Provider = "mockllm"
	}
	if p.Model == "" {
		p.Model = "mock-llm"
	}
	if p.RoutingRationale == "" {
		p.RoutingRationale = "r"
	}
	if p.StatusCode == 0 {
		p.StatusCode = 200
	}
	if p.Ts.IsZero() {
		p.Ts = time.Now().UTC()
	}
	if _, err := q.InsertRequest(context.Background(), p); err != nil {
		t.Fatalf("insert row: %v", err)
	}
}

// TestOverallAggregatesWindow proves the window bound actually excludes rows
// outside it — the property every other read query relies on — and that
// error counting only counts >=400 status codes.
func TestOverallAggregatesWindow(t *testing.T) {
	r, q := newTestReader(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	insertRow(t, q, InsertRequestParams{Ts: now.Add(-2 * time.Hour), InputTokens: 100, OutputTokens: 50, CostUsd: 1, StatusCode: 200})
	insertRow(t, q, InsertRequestParams{Ts: now.Add(-30 * time.Minute), InputTokens: 200, OutputTokens: 80, CostUsd: 2, StatusCode: 500})
	insertRow(t, q, InsertRequestParams{Ts: now.Add(-10 * time.Minute), InputTokens: 300, OutputTokens: 120, CostUsd: 3, StatusCode: 200})

	stats, err := r.Overall(context.Background(), Window{Since: now.Add(-1 * time.Hour)})
	if err != nil {
		t.Fatalf("Overall: %v", err)
	}
	if stats.Requests != 2 {
		t.Fatalf("Requests = %d, want 2 (the -2h row must be excluded)", stats.Requests)
	}
	if stats.InputTokens != 500 || stats.OutputTokens != 200 {
		t.Errorf("tokens = %d/%d, want 500/200", stats.InputTokens, stats.OutputTokens)
	}
	if stats.CostUSD != 5 {
		t.Errorf("CostUSD = %v, want 5", stats.CostUSD)
	}
	if stats.Errors != 1 {
		t.Errorf("Errors = %d, want 1 (only the 500)", stats.Errors)
	}
}

func TestOverallOnEmptyStoreIsZeroNotError(t *testing.T) {
	r, _ := newTestReader(t)
	stats, err := r.Overall(context.Background(), WindowFrom(24*time.Hour))
	if err != nil {
		t.Fatalf("Overall on empty store: %v", err)
	}
	if stats.Requests != 0 || stats.CostUSD != 0 {
		t.Errorf("stats = %+v, want all zero", stats)
	}
}

// TestByProviderGroupsAndOrdersByCost proves grouping is per provider+model
// pair (not provider alone) and that the most expensive pair sorts first —
// the property the "which models get used most" question relies on.
func TestByProviderGroupsAndOrdersByCost(t *testing.T) {
	r, q := newTestReader(t)
	now := time.Now().UTC()

	insertRow(t, q, InsertRequestParams{Ts: now, Provider: "cheap", Model: "small", CostUsd: 0.01})
	insertRow(t, q, InsertRequestParams{Ts: now, Provider: "cheap", Model: "small", CostUsd: 0.01})
	insertRow(t, q, InsertRequestParams{Ts: now, Provider: "pricey", Model: "big", CostUsd: 5.00})
	insertRow(t, q, InsertRequestParams{Ts: now, Provider: "cheap", Model: "big", CostUsd: 0.02})

	rows, err := r.ByProvider(context.Background(), WindowFrom(time.Hour))
	if err != nil {
		t.Fatalf("ByProvider: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d groups, want 3 (distinct provider/model pairs)", len(rows))
	}
	if rows[0].Provider != "pricey" || rows[0].Model != "big" {
		t.Fatalf("most expensive group sorted first, got %+v", rows[0])
	}
	for _, s := range rows {
		if s.Provider == "cheap" && s.Model == "small" && s.Requests != 2 {
			t.Errorf("cheap/small Requests = %d, want 2", s.Requests)
		}
	}
}

// TestByEpochGroupsNullAsEmptyString is the load-bearing epoch test: rows
// written before the epoch column existed (or by a build with no epoch set)
// must still show up as one comparable group, not be dropped or panic the
// scan.
func TestByEpochGroupsNullAsEmptyString(t *testing.T) {
	r, q := newTestReader(t)
	now := time.Now().UTC()

	insertRow(t, q, InsertRequestParams{Ts: now, ConfigEpoch: nil, CostUsd: 1})
	insertRow(t, q, InsertRequestParams{Ts: now, ConfigEpoch: strptr("epoch-a"), CostUsd: 2})
	insertRow(t, q, InsertRequestParams{Ts: now, ConfigEpoch: strptr("epoch-a"), CostUsd: 4})

	rows, err := r.ByEpoch(context.Background(), WindowFrom(time.Hour))
	if err != nil {
		t.Fatalf("ByEpoch: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d epoch groups, want 2", len(rows))
	}

	var nullGroup, aGroup *EpochStats
	for i := range rows {
		switch rows[i].ConfigEpoch {
		case "":
			nullGroup = &rows[i]
		case "epoch-a":
			aGroup = &rows[i]
		}
	}
	if nullGroup == nil || nullGroup.Requests != 1 || nullGroup.CostUSD != 1 {
		t.Fatalf("NULL-epoch group = %+v", nullGroup)
	}
	if aGroup == nil || aGroup.Requests != 2 || aGroup.CostUSD != 6 || aGroup.AvgCostUSD != 3 {
		t.Fatalf("epoch-a group = %+v, want requests=2 cost=6 avg=3", aGroup)
	}
	if _, err := time.Parse(time.RFC3339, aGroup.FirstSeen); err != nil {
		t.Errorf("FirstSeen = %q, want RFC3339 (MIN/MAX(ts) must still be formatted, not passed through raw): %v", aGroup.FirstSeen, err)
	}
	if _, err := time.Parse(time.RFC3339, aGroup.LastSeen); err != nil {
		t.Errorf("LastSeen = %q, want RFC3339: %v", aGroup.LastSeen, err)
	}
}

// TestSessionReturnsTrajectoryInOrder proves a session's requests come back
// oldest-first regardless of insertion order — the property that makes the
// result read as a trajectory rather than an arbitrary set.
func TestSessionReturnsTrajectoryInOrder(t *testing.T) {
	r, q := newTestReader(t)
	base := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	insertRow(t, q, InsertRequestParams{Ts: base.Add(2 * time.Minute), SessionKey: strptr("s1"), Model: "third"})
	insertRow(t, q, InsertRequestParams{Ts: base, SessionKey: strptr("s1"), Model: "first"})
	insertRow(t, q, InsertRequestParams{Ts: base.Add(1 * time.Minute), SessionKey: strptr("s1"), Model: "second"})
	insertRow(t, q, InsertRequestParams{Ts: base, SessionKey: strptr("other-session"), Model: "unrelated"})

	rows, err := r.Session(context.Background(), "s1", 10)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 (other-session excluded)", len(rows))
	}
	if rows[0].Model != "first" || rows[1].Model != "second" || rows[2].Model != "third" {
		t.Fatalf("ordering wrong: got %q, %q, %q", rows[0].Model, rows[1].Model, rows[2].Model)
	}
}

func TestSessionUnknownKeyReturnsEmptyNotError(t *testing.T) {
	r, _ := newTestReader(t)
	rows, err := r.Session(context.Background(), "never-seen", 10)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("got %d rows for an unknown session, want 0", len(rows))
	}
}

func TestSessionRespectsLimit(t *testing.T) {
	r, q := newTestReader(t)
	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		insertRow(t, q, InsertRequestParams{Ts: base.Add(time.Duration(i) * time.Second), SessionKey: strptr("s1")})
	}
	rows, err := r.Session(context.Background(), "s1", 2)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (LIMIT)", len(rows))
	}
}

// TestToolsCountsAcrossRowsAndExcludesToollessRows is the reason Tools is
// hand-written: json_each must expand each row's array, and a plain chat
// row (no tool_calls_json) must not appear as a phantom "0 tools used" entry.
func TestToolsCountsAcrossRowsAndExcludesToollessRows(t *testing.T) {
	r, q := newTestReader(t)
	now := time.Now().UTC()

	insertRow(t, q, InsertRequestParams{Ts: now, ToolCallsJson: strptr(`["read_file","grep"]`)})
	insertRow(t, q, InsertRequestParams{Ts: now, ToolCallsJson: strptr(`["read_file"]`)})
	insertRow(t, q, InsertRequestParams{Ts: now, ToolCallsJson: nil}) // plain chat turn

	stats, err := r.Tools(context.Background(), WindowFrom(time.Hour))
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	counts := map[string]int64{}
	for _, s := range stats {
		counts[s.Tool] = s.Uses
	}
	if counts["read_file"] != 2 {
		t.Errorf("read_file uses = %d, want 2", counts["read_file"])
	}
	if counts["grep"] != 1 {
		t.Errorf("grep uses = %d, want 1", counts["grep"])
	}
	if _, sawEmpty := counts[""]; sawEmpty {
		t.Error("a tool-less row contributed a phantom entry")
	}
}

func TestToolsOnEmptyStoreReturnsEmpty(t *testing.T) {
	r, _ := newTestReader(t)
	stats, err := r.Tools(context.Background(), WindowFrom(time.Hour))
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(stats) != 0 {
		t.Fatalf("got %d entries, want 0", len(stats))
	}
}

// TestReaderCoexistsWithActiveWriter is the regression test for a real
// SQLITE_BUSY: the reader and writer are separate connections on the same
// file, and under the default rollback journal a reader query colliding with
// the writer's insert failed instantly. WAL + busy_timeout (see dsn) is what
// makes the two coexist; this drives writes through the real writer while
// reading, which an in-memory single-connection test would never catch.
func TestReaderCoexistsWithActiveWriter(t *testing.T) {
	w, path := newTestWriter(t)

	// Populate, then keep the writer's connection open (do not Close it —
	// that is what tears down the handle) while reading through a second
	// connection.
	w.Record(Event{TraceID: "t1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Usage: types.Usage{InputTokens: 10}, ConfigEpoch: "epoch-a"})

	reader, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })

	// Interleave writes and reads: each read must not fail with SQLITE_BUSY
	// even while the writer is draining its queue.
	for i := 0; i < 5; i++ {
		w.Record(Event{TraceID: "t-more", Format: "openai", Provider: "p", Model: "m", StatusCode: 200})

		if _, err := reader.Overall(context.Background(), WindowFrom(time.Hour)); err != nil {
			t.Fatalf("read %d while writing: %v", i, err)
		}
	}
}
