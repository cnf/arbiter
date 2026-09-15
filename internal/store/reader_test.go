package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/cnf/arbiter/pkg/types"
)

// newTestReader applies the schema to a fresh in-memory database and wraps it
// in a Reader, so read tests exercise the same SQL a real deployment runs
// without going through the writer's queue/goroutine.
func newTestReader(t *testing.T) (*Reader, *sql.DB) {
	t.Helper()
	db := openMemory(t)
	return &Reader{db: db}, db
}

// insertRow writes one row through the same hand-written insert the writer
// uses, so read tests exercise the real SQL without the writer's queue.
func insertRow(t *testing.T, db *sql.DB, ev Event) {
	t.Helper()
	if ev.TraceID == "" {
		ev.TraceID = "trace"
	}
	if ev.Format == "" {
		ev.Format = "openai"
	}
	if ev.Provider == "" {
		ev.Provider = "mockllm"
	}
	if ev.Model == "" {
		ev.Model = "mock-llm"
	}
	if ev.RoutingRationale == "" {
		ev.RoutingRationale = "r"
	}
	if ev.StatusCode == 0 {
		ev.StatusCode = 200
	}
	if ev.Ts.IsZero() {
		ev.Ts = time.Now().UTC()
	}
	if err := insertRequest(context.Background(), db, ev); err != nil {
		t.Fatalf("insert row: %v", err)
	}
}

// TestOverallAggregatesWindow proves the window bound actually excludes rows
// outside it — the property every other read query relies on — and that
// error counting only counts >=400 status codes.
func TestOverallAggregatesWindow(t *testing.T) {
	r, q := newTestReader(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	insertRow(t, q, Event{Ts: now.Add(-2 * time.Hour), StatusCode: 200, Usage: types.Usage{InputTokens: 100, OutputTokens: 50, CostUSD: 1}})
	insertRow(t, q, Event{Ts: now.Add(-30 * time.Minute), StatusCode: 500, Usage: types.Usage{InputTokens: 200, OutputTokens: 80, CostUSD: 2}})
	insertRow(t, q, Event{Ts: now.Add(-10 * time.Minute), StatusCode: 200, Usage: types.Usage{InputTokens: 300, OutputTokens: 120, CostUSD: 3}})

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

	insertRow(t, q, Event{Ts: now, Provider: "cheap", Model: "small", Usage: types.Usage{CostUSD: 0.01}})
	insertRow(t, q, Event{Ts: now, Provider: "cheap", Model: "small", Usage: types.Usage{CostUSD: 0.01}})
	insertRow(t, q, Event{Ts: now, Provider: "pricey", Model: "big", Usage: types.Usage{CostUSD: 5.00}})
	insertRow(t, q, Event{Ts: now, Provider: "cheap", Model: "big", Usage: types.Usage{CostUSD: 0.02}})

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

	insertRow(t, q, Event{Ts: now, ConfigEpoch: "", Usage: types.Usage{CostUSD: 1}})
	insertRow(t, q, Event{Ts: now, ConfigEpoch: "epoch-a", Usage: types.Usage{CostUSD: 2}})
	insertRow(t, q, Event{Ts: now, ConfigEpoch: "epoch-a", Usage: types.Usage{CostUSD: 4}})

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

	insertRow(t, q, Event{Ts: base.Add(2 * time.Minute), SessionKey: "s1", Model: "third"})
	insertRow(t, q, Event{Ts: base, SessionKey: "s1", Model: "first"})
	insertRow(t, q, Event{Ts: base.Add(1 * time.Minute), SessionKey: "s1", Model: "second"})
	insertRow(t, q, Event{Ts: base, SessionKey: "other-session", Model: "unrelated"})

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
		insertRow(t, q, Event{Ts: base.Add(time.Duration(i) * time.Second), SessionKey: "s1"})
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

	insertRow(t, q, Event{Ts: now, ToolCalls: []string{"read_file", "grep"}})
	insertRow(t, q, Event{Ts: now, ToolCalls: []string{"read_file"}})
	insertRow(t, q, Event{Ts: now, ToolCalls: nil}) // plain chat turn

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
// TestListRequestsFiltersAndOrders covers the list endpoint's contract: newest
// first, and each filter actually narrowing rather than being accepted and
// ignored (the failure mode that makes a UI's filter UI lie).
func TestListRequestsFiltersAndOrders(t *testing.T) {
	r, q := newTestReader(t)
	base := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	insertRow(t, q, Event{Ts: base, Provider: "a", Model: "m1", SessionKey: "s1", AliasUsed: "auto", StatusCode: 200})
	insertRow(t, q, Event{Ts: base.Add(time.Minute), Provider: "b", Model: "m2", SessionKey: "s1", AliasUsed: "coding", StatusCode: 500})
	insertRow(t, q, Event{Ts: base.Add(2 * time.Minute), Provider: "a", Model: "m3", SessionKey: "s2", StatusCode: 200})

	// Newest first, with no filter.
	all, err := r.ListRequests(context.Background(), RequestFilter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("len = %d, want 3", len(all))
	}
	if all[0].Model != "m3" || all[2].Model != "m1" {
		t.Errorf("order = %s..%s, want m3..m1", all[0].Model, all[2].Model)
	}

	// A row inside the window but outside ?since is excluded.
	recent, err := r.ListRequests(context.Background(), RequestFilter{Since: base.Add(90 * time.Second)})
	if err != nil {
		t.Fatalf("ListRequests(since): %v", err)
	}
	if len(recent) != 1 || recent[0].Model != "m3" {
		t.Errorf("since filter returned %d rows (%v), want just m3", len(recent), recent)
	}

	cases := []struct {
		name   string
		filter RequestFilter
		want   int
	}{
		{"provider", RequestFilter{Provider: "a"}, 2},
		{"session", RequestFilter{SessionKey: "s1"}, 2},
		{"alias", RequestFilter{Alias: "coding"}, 1},
		{"status", RequestFilter{StatusCode: 500}, 1},
		{"errors", RequestFilter{ErrorsOnly: true}, 1},
		{"provider+errors", RequestFilter{Provider: "a", ErrorsOnly: true}, 0},
	}
	for _, tc := range cases {
		got, err := r.ListRequests(context.Background(), tc.filter)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(got) != tc.want {
			t.Errorf("%s returned %d rows, want %d", tc.name, len(got), tc.want)
		}
	}

	// limit truncates to the newest N, not an arbitrary N.
	limited, err := r.ListRequests(context.Background(), RequestFilter{Limit: 2})
	if err != nil {
		t.Fatalf("ListRequests(limit): %v", err)
	}
	if len(limited) != 2 || limited[0].Model != "m3" || limited[1].Model != "m2" {
		t.Errorf("limit=2 returned %v, want the two newest (m3, m2)", limited)
	}

	// An over-large limit is clamped by the reader, not passed to sqlite.
	clamped, err := r.ListRequests(context.Background(), RequestFilter{Limit: maxRequestListLimit * 10})
	if err != nil {
		t.Fatalf("ListRequests(clamped): %v", err)
	}
	if len(clamped) != 3 {
		t.Errorf("clamped limit returned %d rows, want all 3", len(clamped))
	}
}

// TestListRequestsIsEmptyArrayNotNull keeps the "no rows" and "query failed"
// cases distinguishable on the wire, matching the aggregate endpoints.
func TestListRequestsIsEmptyArrayNotNull(t *testing.T) {
	r, _ := newTestReader(t)
	out, err := r.ListRequests(context.Background(), RequestFilter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if out == nil {
		t.Fatal("ListRequests returned nil; must be an empty slice so JSON is [] not null")
	}
	if len(out) != 0 {
		t.Fatalf("len = %d, want 0", len(out))
	}
}

// TestGetRequestReturnsFullRowAndMissingIsNotAnError pins both halves of the
// detail contract: the nullable columns survive the round trip, and an unknown
// id is (zero, false, nil) so the handler can answer 404 rather than 500.
func TestGetRequestReturnsFullRowAndMissingIsNotAnError(t *testing.T) {
	r, q := newTestReader(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	insertRow(t, q, Event{Ts: now, Provider: "a", Model: "m1", SessionKey: "s1",
		AliasUsed: "auto", Domain: "code_generation",
		Effort: "hard", CostClass: "budget", ConfigEpoch: "epoch-a", LatencyMs: 250, StatusCode: 200,
		ToolCalls: []string{"read", "write"}, Usage: types.Usage{InputTokens: 10, OutputTokens: 20, CacheRead: 5, CacheWrite: 7, CostUSD: 1.5}})

	rows, err := r.ListRequests(context.Background(), RequestFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListRequests = %v, %v; want one row", rows, err)
	}

	d, ok, err := r.GetRequest(context.Background(), rows[0].ID)
	if err != nil || !ok {
		t.Fatalf("GetRequest = ok %v, err %v; want ok", ok, err)
	}
	if d.Model != "m1" || d.SessionKey != "s1" || d.AliasUsed != "auto" {
		t.Errorf("route fields = %+v, want m1/s1/auto", d.RequestRow)
	}
	if d.Domain != "code_generation" || d.Effort != "hard" || d.CostClass != "budget" {
		t.Errorf("axes = %q/%q/%q, want code_generation/hard/budget", d.Domain, d.Effort, d.CostClass)
	}
	if d.CacheReadTokens != 5 || d.CacheWriteTokens != 7 {
		t.Errorf("cache tokens = %d/%d, want 5/7", d.CacheReadTokens, d.CacheWriteTokens)
	}
	if d.ToolCalls != `["read","write"]` {
		t.Errorf("ToolCalls = %q, want the raw JSON array", d.ToolCalls)
	}
	if d.Ts == "" {
		t.Error("Ts is empty; want RFC3339")
	}

	// Content is not captured — the detail shape says so rather than lying.
	if d.RequestText != "" || d.ResponseText != "" {
		t.Errorf("content fields = %q/%q, want empty (not stored)", d.RequestText, d.ResponseText)
	}

	if _, ok, err := r.GetRequest(context.Background(), 424242); ok || err != nil {
		t.Errorf("missing id = ok %v, err %v; want false, nil", ok, err)
	}
}

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
