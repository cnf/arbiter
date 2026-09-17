package store

import (
	"context"
	"testing"
	"time"
)

// TestKindDefaultsToClient proves a caller that never sets Kind (every
// existing call site in the codebase, until Phase B) still gets "client"
// written, both through the writer's queue and through the direct insert
// path a reader test uses.
func TestKindDefaultsToClient(t *testing.T) {
	r, q := newTestReader(t)
	insertRow(t, q, Event{Ts: time.Now().UTC()})

	rows, err := r.ListRequests(context.Background(), RequestFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(rows) != 1 || rows[0].Kind != "client" {
		t.Fatalf("Kind = %q, want client (rows: %+v)", rows[0].Kind, rows)
	}
}

// TestListRequestsKindFilter proves RequestFilter.Kind follows every other
// field's zero-value convention at the reader layer: empty means no filter
// (both kinds returned), a non-empty value narrows to an exact match. The
// "absent query param defaults to client" behavior lives at the HTTP/UI
// layer (requestKindFilter), not here — this only tests the reader itself.
func TestListRequestsKindFilter(t *testing.T) {
	r, q := newTestReader(t)
	now := time.Now().UTC()
	insertRow(t, q, Event{Ts: now, TraceID: "client-row"})
	insertRow(t, q, Event{Ts: now, TraceID: "classifier-row", Kind: "classifier"})

	all, err := r.ListRequests(context.Background(), RequestFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListRequests (no filter): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d rows with no kind filter, want 2 (both kinds)", len(all))
	}

	clientOnly, err := r.ListRequests(context.Background(), RequestFilter{Limit: 10, Kind: "client"})
	if err != nil {
		t.Fatalf("ListRequests (kind=client): %v", err)
	}
	if len(clientOnly) != 1 || clientOnly[0].TraceID != "client-row" {
		t.Fatalf("kind=client got %+v, want only client-row", clientOnly)
	}

	classifierOnly, err := r.ListRequests(context.Background(), RequestFilter{Limit: 10, Kind: "classifier"})
	if err != nil {
		t.Fatalf("ListRequests (kind=classifier): %v", err)
	}
	if len(classifierOnly) != 1 || classifierOnly[0].TraceID != "classifier-row" {
		t.Fatalf("kind=classifier got %+v, want only classifier-row", classifierOnly)
	}
}

// TestAggregatesExcludeNonClientKind proves the aggregate/dashboard queries
// are scoped to real traffic — a classifier call's own tokens/cost/latency
// must not appear in Overall, Tools, the sessions index, or the pivot table,
// which is the whole point of tagging it a different kind in the first
// place (don't skew the numbers meant to describe client traffic).
func TestAggregatesExcludeNonClientKind(t *testing.T) {
	r, q := newTestReader(t)
	now := time.Now().UTC()

	insertRow(t, q, Event{
		Ts: now, TraceID: "client-row", SessionKey: "sess-1",
		Usage:     usageOf(100, 50, 1),
		ToolCalls: []string{"read_file"},
	})
	insertRow(t, q, Event{
		Ts: now, TraceID: "classifier-row", Kind: "classifier", SessionKey: "sess-1",
		Usage:     usageOf(9999, 9999, 999), // would dominate every aggregate if counted
		ToolCalls: []string{"should_not_count"},
	})

	overall, err := r.Overall(context.Background(), WindowFrom(time.Hour))
	if err != nil {
		t.Fatalf("Overall: %v", err)
	}
	if overall.Requests != 1 || overall.CostUSD != 1 {
		t.Errorf("Overall = %+v, want 1 request costing $1 (classifier row excluded)", overall)
	}

	tools, err := r.Tools(context.Background(), WindowFrom(time.Hour))
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	for _, s := range tools {
		if s.Tool == "should_not_count" {
			t.Errorf("Tools included the classifier row's tool call: %+v", tools)
		}
	}

	sessions, err := r.Sessions(context.Background(), WindowFrom(time.Hour), 10)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Turns != 1 || sessions[0].CostUSD != 1 {
		t.Fatalf("Sessions = %+v, want one session with 1 turn costing $1", sessions)
	}

	rows, err := r.PivotTotals(context.Background(), WindowFrom(time.Hour), DimProvider, MetricRequests, 10)
	if err != nil {
		t.Fatalf("PivotTotals: %v", err)
	}
	var total int64
	for _, p := range rows {
		total += p.Requests
	}
	if total != 1 {
		t.Errorf("PivotTotals summed to %v requests, want 1 (classifier row excluded)", total)
	}
}

// TestSessionTrajectoryAndDetailIncludeEveryKind is the other half of the
// contract: the aggregates hide non-client kinds, but the per-session
// trajectory and the single-request detail lookup must not — that's what
// makes a classifier call debuggable in context rather than invisible.
func TestSessionTrajectoryAndDetailIncludeEveryKind(t *testing.T) {
	r, q := newTestReader(t)
	now := time.Now().UTC()

	insertRow(t, q, Event{Ts: now, TraceID: "client-row", SessionKey: "sess-1"})
	insertRow(t, q, Event{Ts: now.Add(time.Second), TraceID: "classifier-row", Kind: "classifier", SessionKey: "sess-1"})

	turns, err := r.Session(context.Background(), "sess-1", 10)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("Session trajectory has %d turns, want 2 (classifier row must be visible)", len(turns))
	}
	kinds := map[string]bool{}
	for _, tn := range turns {
		kinds[tn.Kind] = true
	}
	if !kinds["client"] || !kinds["classifier"] {
		t.Errorf("Session trajectory kinds = %v, want both client and classifier", kinds)
	}

	rows, err := r.ListRequests(context.Background(), RequestFilter{Limit: 10, Kind: "classifier"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListRequests(kind=classifier): rows=%v err=%v", rows, err)
	}
	d, ok, err := r.GetRequest(context.Background(), rows[0].ID)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if !ok || d.Kind != "classifier" {
		t.Fatalf("GetRequest on the classifier row = %+v, ok=%v, want it found with kind=classifier", d, ok)
	}
}
