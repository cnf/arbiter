package store

import (
	"context"
	"testing"
	"time"

	"github.com/cnf/arbiter/pkg/types"
)

// flowEvent builds a client request on one alias→provider/model route, with the
// four token counters the flow aggregate sums.
func flowEvent(ts time.Time, alias, provider, model string, in, out, cacheRead, cacheWrite int64, cost float64) Event {
	ev := event(ts, provider, model)
	ev.AliasUsed = alias
	ev.Kind = "client"
	ev.Usage = types.Usage{
		InputTokens:  int(in),
		OutputTokens: int(out),
		CacheRead:    int(cacheRead),
		CacheWrite:   int(cacheWrite),
		CostUSD:      cost,
	}
	return ev
}

// TestRoutingFlowGroupsByAliasProviderModel is the shape check: one edge per
// distinct route, busiest first, with the token counters summed per edge rather
// than across the window.
func TestRoutingFlowGroupsByAliasProviderModel(t *testing.T) {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	r := openSeeded(t,
		// "coding" → claude, twice
		flowEvent(base, "coding", "claude", "sonnet", 100, 10, 900, 50, 1.0),
		flowEvent(base.Add(time.Second), "coding", "claude", "sonnet", 100, 10, 900, 50, 1.0),
		// same model, different provider: must NOT merge with the above
		flowEvent(base.Add(2*time.Second), "coding", "openrouter", "sonnet", 50, 5, 0, 0, 0.5),
		// a literal-model request: empty alias is a real route, not a gap
		flowEvent(base.Add(3*time.Second), "", "claude", "opus", 20, 2, 0, 0, 0.25),
	)

	w := Window{Since: base.Add(-time.Hour)}
	edges, err := r.RoutingFlow(context.Background(), w, 10)
	if err != nil {
		t.Fatalf("routing flow: %v", err)
	}
	if len(edges) != 3 {
		t.Fatalf("got %d edges, want 3 distinct routes: %+v", len(edges), edges)
	}

	// Busiest first: the 2-request coding→claude/sonnet route leads.
	top := edges[0]
	if top.Alias != "coding" || top.Provider != "claude" || top.Model != "sonnet" {
		t.Errorf("top edge = %s→%s/%s, want coding→claude/sonnet", top.Alias, top.Provider, top.Model)
	}
	if top.Requests != 2 {
		t.Errorf("top edge requests = %d, want 2", top.Requests)
	}
	// Tokens must include cache read+write, not just input+output.
	if want := int64(2 * (100 + 10 + 900 + 50)); top.Tokens != want {
		t.Errorf("top edge tokens = %d, want %d (input+output+cache_read+cache_write)", top.Tokens, want)
	}
	if top.CacheReadTokens != 1800 || top.CacheWriteTokens != 100 {
		t.Errorf("top edge cache = %d read / %d write, want 1800/100",
			top.CacheReadTokens, top.CacheWriteTokens)
	}

	// The same model under a second provider stayed its own edge.
	var foundOR bool
	for _, e := range edges {
		if e.Provider == "openrouter" && e.Model == "sonnet" {
			foundOR = true
			if e.Requests != 1 {
				t.Errorf("openrouter/sonnet requests = %d, want 1", e.Requests)
			}
		}
	}
	if !foundOR {
		t.Error("openrouter/sonnet was merged into claude/sonnet — grouping must include provider")
	}

	// The empty-alias route survived as a route.
	var foundLiteral bool
	for _, e := range edges {
		if e.Alias == "" && e.Model == "opus" {
			foundLiteral = true
			if e.IsRemainder() {
				t.Error("a literal-model route was misidentified as the remainder bucket")
			}
		}
	}
	if !foundLiteral {
		t.Error("the empty-alias (literal model) route was dropped; it is a real route")
	}
}

// TestRoutingFlowFoldsPastTheCap proves the remainder bucket both exists and
// conserves the window's totals — a cap that silently dropped the tail would
// make every KPI computed from these edges wrong.
func TestRoutingFlowFoldsPastTheCap(t *testing.T) {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	var events []Event
	var wantRequests, wantTokens int64
	var wantCost float64
	// 8 distinct routes, descending request counts so the ordering is
	// deterministic: route i gets (8-i) requests.
	for i := 0; i < 8; i++ {
		for j := 0; j < 8-i; j++ {
			ev := flowEvent(base.Add(time.Duration(i*100+j)*time.Millisecond),
				"a", "p", string(rune('m'+i)), 10, 1, 5, 2, 0.5)
			events = append(events, ev)
			wantRequests++
			wantTokens += 10 + 1 + 5 + 2
			wantCost += 0.5
		}
	}
	r := openSeeded(t, events...)

	w := Window{Since: base.Add(-time.Hour)}
	edges, err := r.RoutingFlow(context.Background(), w, 3)
	if err != nil {
		t.Fatalf("routing flow: %v", err)
	}
	// 3 real edges + 1 remainder.
	if len(edges) != 4 {
		t.Fatalf("got %d edges, want 3 capped + 1 remainder: %+v", len(edges), edges)
	}
	rem := edges[len(edges)-1]
	if !rem.IsRemainder() {
		t.Fatal("last edge is not the remainder bucket")
	}
	if rem.FoldedRoutes != 5 {
		t.Errorf("remainder folded %d routes, want 5", rem.FoldedRoutes)
	}
	for _, e := range edges[:3] {
		if e.IsRemainder() {
			t.Error("a capped real route was flagged as the remainder")
		}
	}

	// Conservation: the edges together still describe the whole window.
	var gotRequests, gotTokens int64
	var gotCost float64
	for _, e := range edges {
		gotRequests += e.Requests
		gotTokens += e.Tokens
		gotCost += e.CostUSD
	}
	if gotRequests != wantRequests {
		t.Errorf("edges total %d requests, window has %d — the cap lost rows", gotRequests, wantRequests)
	}
	if gotTokens != wantTokens {
		t.Errorf("edges total %d tokens, window has %d", gotTokens, wantTokens)
	}
	if gotCost < wantCost-0.001 || gotCost > wantCost+0.001 {
		t.Errorf("edges total $%.4f, window has $%.4f", gotCost, wantCost)
	}
}

// TestRoutingFlowExcludesNonClientTraffic guards the filter that makes this a
// diagram of client routing decisions rather than of Arbiter's own machinery.
func TestRoutingFlowExcludesNonClientTraffic(t *testing.T) {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	classifier := flowEvent(base.Add(time.Second), "auto", "claude", "haiku", 10, 1, 0, 0, 0.01)
	classifier.Kind = "classifier"
	r := openSeeded(t,
		flowEvent(base, "coding", "claude", "sonnet", 100, 10, 0, 0, 1.0),
		classifier,
	)

	edges, err := r.RoutingFlow(context.Background(), Window{Since: base.Add(-time.Hour)}, 10)
	if err != nil {
		t.Fatalf("routing flow: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1 (classifier traffic must be excluded): %+v", len(edges), edges)
	}
	if edges[0].Model == "haiku" {
		t.Error("classifier traffic leaked into the routing flow")
	}
}

// TestWindowUntilIsExclusiveAndTiles is the compare-mode correctness property:
// two adjacent windows must partition the traffic exactly, with the row sitting
// on the shared boundary counted once. A closed upper bound counts it twice.
func TestWindowUntilIsExclusiveAndTiles(t *testing.T) {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	boundary := base.Add(10 * time.Minute)

	r := openSeeded(t,
		flowEvent(base, "a", "p", "before", 1, 1, 0, 0, 0.1),
		flowEvent(base.Add(5*time.Minute), "a", "p", "before", 1, 1, 0, 0, 0.1),
		// exactly on the boundary: belongs to the *after* window only
		flowEvent(boundary, "a", "p", "onboundary", 1, 1, 0, 0, 0.1),
		flowEvent(boundary.Add(time.Minute), "a", "p", "after", 1, 1, 0, 0, 0.1),
	)
	ctx := context.Background()

	before := Window{Since: base.Add(-time.Hour), Until: boundary}
	after := Window{Since: boundary}

	beforeEdges, err := r.RoutingFlow(ctx, before, 10)
	if err != nil {
		t.Fatalf("before window: %v", err)
	}
	afterEdges, err := r.RoutingFlow(ctx, after, 10)
	if err != nil {
		t.Fatalf("after window: %v", err)
	}

	count := func(edges []RoutingEdge) int64 {
		var n int64
		for _, e := range edges {
			n += e.Requests
		}
		return n
	}
	gotBefore, gotAfter := count(beforeEdges), count(afterEdges)
	if gotBefore != 2 {
		t.Errorf("before window has %d requests, want 2", gotBefore)
	}
	if gotAfter != 2 {
		t.Errorf("after window has %d requests, want 2 (incl. the boundary row)", gotAfter)
	}
	if gotBefore+gotAfter != 4 {
		t.Errorf("adjacent windows total %d requests, want exactly 4 — they must tile, not overlap",
			gotBefore+gotAfter)
	}
	// The boundary row must be in the after side, never the before side.
	for _, e := range beforeEdges {
		if e.Model == "onboundary" {
			t.Error("the row exactly on the boundary fell in the before window; Until must be exclusive")
		}
	}

	// And an unbounded window still means "everything since", so no existing
	// caller changed behaviour when Until was added.
	all, err := r.RoutingFlow(ctx, Window{Since: base.Add(-time.Hour)}, 10)
	if err != nil {
		t.Fatalf("unbounded window: %v", err)
	}
	if count(all) != 4 {
		t.Errorf("unbounded window has %d requests, want all 4", count(all))
	}
}

// TestEdgeRateMetrics pins the two formulas the Overview page's numbers mean,
// both recovered from the settled mockup's own figures rather than invented:
// cost-per-1M divides by every token moved, and cache-hit is measured against
// cacheable (prompt) tokens only.
func TestEdgeRateMetrics(t *testing.T) {
	e := RoutingEdge{Measures: Measures{
		CostUSD:          10,
		InputTokens:      100_000,
		OutputTokens:     100_000,
		CacheReadTokens:  700_000,
		CacheWriteTokens: 100_000,
	}}
	e.Tokens = e.InputTokens + e.OutputTokens + e.CacheReadTokens + e.CacheWriteTokens

	// 1M tokens total, $10 → $10/1M.
	if got := e.CostPer1MTokens(); got < 9.999 || got > 10.001 {
		t.Errorf("cost/1M = %.4f, want 10.0 (denominator must include cache tokens)", got)
	}
	// cacheable = cache_read + input = 800k; hit = 700k/800k = 0.875.
	// Output and cache-write tokens must not dilute it.
	if got := e.CacheHitRate(); got < 0.8749 || got > 0.8751 {
		t.Errorf("cache hit = %.4f, want 0.875 (cache_read / (cache_read+input))", got)
	}
	if !e.Cacheable() {
		t.Error("Cacheable() = false on an edge with prompt tokens")
	}

	// A route that only ever errored: real requests, no tokens. Must render as
	// zero rather than dividing by zero, and must report "nothing cacheable"
	// rather than a 0% hit rate.
	broken := RoutingEdge{Measures: Measures{Requests: 23, Errors: 23}}
	if got := broken.CostPer1MTokens(); got != 0 {
		t.Errorf("cost/1M on a token-less edge = %v, want 0", got)
	}
	if got := broken.CacheHitRate(); got != 0 {
		t.Errorf("cache hit on a token-less edge = %v, want 0", got)
	}
	if broken.Cacheable() {
		t.Error("Cacheable() = true on an edge that moved no prompt tokens")
	}
	if got := broken.ErrorRate(); got != 1 {
		t.Errorf("error rate = %v, want 1 for 23/23 failures", got)
	}
}

// TestConfigEpochsCollapsesEditorSaveBursts is the reason ConfigEpochs exists
// in this shape. Arbiter reloads on config file change, so a single editing
// session lands as a burst of epochs seconds apart; the anchor a compare wants
// is where that burst settled, not each individual save.
//
// The shape here mirrors a real run observed in the live store: two tiny epochs
// seconds apart, then the one that served real traffic.
func TestConfigEpochsCollapsesEditorSaveBursts(t *testing.T) {
	base := time.Date(2026, 9, 23, 19, 0, 0, 0, time.UTC)

	withEpoch := func(ts time.Time, epoch string) Event {
		ev := flowEvent(ts, "a", "p", "m", 10, 1, 0, 0, 0.01)
		ev.ConfigEpoch = epoch
		return ev
	}

	var events []Event
	// Burst: two saves seconds apart, tiny traffic each.
	events = append(events, withEpoch(base.Add(6*time.Minute+33*time.Second), "burst1"))
	events = append(events, withEpoch(base.Add(6*time.Minute+37*time.Second), "burst2"))
	// Settled config 90s later, real traffic — same editing session.
	for i := 0; i < 20; i++ {
		events = append(events, withEpoch(base.Add(8*time.Minute+time.Duration(i)*time.Second), "settled"))
	}
	// A genuinely separate change, hours later. Its first request lands BEFORE
	// the settled config's last one: requests already in flight when Arbiter
	// reloads are recorded under the old epoch after the new one has begun
	// serving, so consecutive epochs overlap. This is not a contrived case —
	// it is what the live store looks like at every reload boundary, and a
	// burst-collapse keyed on the end-to-start gap sees a negative gap here,
	// reads it as a burst, and wrongly merges two real changes.
	for i := 0; i < 5; i++ {
		events = append(events, withEpoch(base.Add(6*time.Hour+time.Duration(i)*time.Second), "later"))
	}
	// The straggler under the *old* epoch, after "later" already started.
	events = append(events, withEpoch(base.Add(6*time.Hour+30*time.Second), "settled"))
	r := openSeeded(t, events...)

	anchors, err := r.ConfigEpochs(context.Background(), Window{Since: base.Add(-time.Hour)}, 40)
	if err != nil {
		t.Fatalf("config epochs: %v", err)
	}

	// Expect two anchors: the later change, and the settled end of the burst.
	if len(anchors) != 2 {
		t.Fatalf("got %d anchors, want 2 (one per real change): %+v", len(anchors), anchors)
	}
	// Newest first.
	if anchors[0].Epoch != "later" {
		t.Errorf("first anchor = %q, want %q (newest first)", anchors[0].Epoch, "later")
	}
	if anchors[0].Merged != 1 {
		t.Errorf("the standalone change merged %d epochs, want 1", anchors[0].Merged)
	}

	settled := anchors[1]
	if settled.Epoch != "settled" {
		t.Errorf("burst anchor = %q, want %q — the anchor must be where the burst settled, "+
			"not the first save", settled.Epoch, "settled")
	}
	if settled.Merged != 3 {
		t.Errorf("burst anchor merged %d epochs, want 3 (two saves + the settled one)", settled.Merged)
	}
	// The absorbed saves' requests are accounted for, not discarded. 20 under
	// the settled epoch itself, 2 from the absorbed saves, plus the one
	// in-flight straggler recorded under the settled epoch after "later" began.
	if settled.Requests != 23 {
		t.Errorf("burst anchor covers %d requests, want 23 (20 settled + 2 absorbed + 1 straggler)", settled.Requests)
	}
	// Started is the settled epoch's own start, which is what a compare pivots
	// on — an absorbed save must not drag the anchor earlier.
	if want := base.Add(8 * time.Minute); !settled.Started.Equal(want) {
		t.Errorf("burst anchor starts at %s, want %s (the settled config's first request)",
			settled.Started, want)
	}
}

// TestConfigEpochsSkipsEpochlessRows keeps pre-epoch history out of the anchor
// list: those rows span an arbitrary stretch and never corresponded to a change.
func TestConfigEpochsSkipsEpochlessRows(t *testing.T) {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	noEpoch := flowEvent(base, "a", "p", "m", 10, 1, 0, 0, 0.01) // ConfigEpoch left ""
	withEpoch := flowEvent(base.Add(time.Hour), "a", "p", "m", 10, 1, 0, 0, 0.01)
	withEpoch.ConfigEpoch = "real"

	r := openSeeded(t, noEpoch, withEpoch)
	anchors, err := r.ConfigEpochs(context.Background(), Window{Since: base.Add(-time.Hour)}, 40)
	if err != nil {
		t.Fatalf("config epochs: %v", err)
	}
	if len(anchors) != 1 {
		t.Fatalf("got %d anchors, want 1: %+v", len(anchors), anchors)
	}
	if anchors[0].Epoch != "real" {
		t.Errorf("anchor = %q, want %q", anchors[0].Epoch, "real")
	}
}
