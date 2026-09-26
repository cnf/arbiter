package store

import (
	"context"
	"testing"
	"time"
)

// TestSummarizeWindowTotalsMatchTheFlowEdges is the consistency property the
// page depends on: the KPI strip and the sankey are two views of one window, so
// their totals must agree. They come from separate queries, which is exactly how
// they could silently diverge.
func TestSummarizeWindowTotalsMatchTheFlowEdges(t *testing.T) {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	r := openSeeded(t,
		flowEvent(base, "coding", "claude", "sonnet", 100, 10, 900, 50, 1.0),
		flowEvent(base.Add(time.Second), "coding", "claude", "sonnet", 100, 10, 900, 50, 1.0),
		flowEvent(base.Add(2*time.Second), "arbiter", "openrouter", "deepseek", 50, 5, 25, 0, 0.5),
		flowEvent(base.Add(3*time.Second), "", "claude", "opus", 20, 2, 0, 0, 0.25),
	)
	ctx := context.Background()
	w := Window{Since: base.Add(-time.Hour)}

	sum, err := r.SummarizeWindow(ctx, w)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	edges, err := r.RoutingFlow(ctx, w, 24)
	if err != nil {
		t.Fatalf("routing flow: %v", err)
	}

	var edgeTotals Measures
	for _, e := range edges {
		edgeTotals.Add(e.Measures)
	}

	if sum.Requests != edgeTotals.Requests {
		t.Errorf("summary has %d requests, flow edges total %d", sum.Requests, edgeTotals.Requests)
	}
	if sum.Tokens != edgeTotals.Tokens {
		t.Errorf("summary has %d tokens, flow edges total %d", sum.Tokens, edgeTotals.Tokens)
	}
	if sum.CacheReadTokens != edgeTotals.CacheReadTokens {
		t.Errorf("summary has %d cache reads, flow edges total %d",
			sum.CacheReadTokens, edgeTotals.CacheReadTokens)
	}
	if d := sum.CostUSD - edgeTotals.CostUSD; d < -0.0001 || d > 0.0001 {
		t.Errorf("summary cost $%.4f, flow edges total $%.4f", sum.CostUSD, edgeTotals.CostUSD)
	}
	// And the derived rates, which is the part that would drift if the strip
	// computed them from its own columns instead of through Measures.
	if sum.CacheHitRate() != edgeTotals.CacheHitRate() {
		t.Errorf("summary cache hit %.6f, flow edges %.6f — one definition, two answers",
			sum.CacheHitRate(), edgeTotals.CacheHitRate())
	}
}

// TestSummarizeWindowCountsSessionsNotRoutes guards the one counter that cannot
// come from the flow edges: a conversation that switched model mid-flight spans
// routes, so summing per-route session counts would count it twice.
func TestSummarizeWindowCountsSessionsNotRoutes(t *testing.T) {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)

	withSession := func(ts time.Time, key, model string) Event {
		ev := flowEvent(ts, "a", "p", model, 10, 1, 0, 0, 0.1)
		ev.SessionKey = key
		return ev
	}
	r := openSeeded(t,
		// one conversation, two different models
		withSession(base, "sess-1", "m1"),
		withSession(base.Add(time.Second), "sess-1", "m2"),
		// a second conversation
		withSession(base.Add(2*time.Second), "sess-2", "m1"),
		// a request with no session key at all: must not count as one
		flowEvent(base.Add(3*time.Second), "a", "p", "m1", 10, 1, 0, 0, 0.1),
	)

	sum, err := r.SummarizeWindow(context.Background(), Window{Since: base.Add(-time.Hour)})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if sum.Requests != 4 {
		t.Errorf("requests = %d, want 4", sum.Requests)
	}
	if sum.Sessions != 2 {
		t.Errorf("sessions = %d, want 2 (a session spanning two models is one session, "+
			"and a keyless request is none)", sum.Sessions)
	}
}

// TestSummarizeWindowRespectsBothBounds proves the KPI strip can describe a
// closed window, which is what makes it usable for the before-side of a compare.
func TestSummarizeWindowRespectsBothBounds(t *testing.T) {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	boundary := base.Add(10 * time.Minute)
	r := openSeeded(t,
		flowEvent(base, "a", "p", "before", 10, 1, 0, 0, 1.0),
		flowEvent(boundary, "a", "p", "onboundary", 10, 1, 0, 0, 2.0),
		flowEvent(boundary.Add(time.Minute), "a", "p", "after", 10, 1, 0, 0, 4.0),
	)
	ctx := context.Background()

	before, err := r.SummarizeWindow(ctx, Window{Since: base.Add(-time.Hour), Until: boundary})
	if err != nil {
		t.Fatalf("before: %v", err)
	}
	if before.Requests != 1 || before.CostUSD != 1.0 {
		t.Errorf("before window = %d req/$%.2f, want 1/$1.00 (Until is exclusive)",
			before.Requests, before.CostUSD)
	}
	after, err := r.SummarizeWindow(ctx, Window{Since: boundary})
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if after.Requests != 2 || after.CostUSD != 6.0 {
		t.Errorf("after window = %d req/$%.2f, want 2/$6.00 (incl. the boundary row)",
			after.Requests, after.CostUSD)
	}
}

// TestDeltaFromZeroHasNoPercentage is the honesty guard on compare mode. A
// percentage change from nothing is undefined, not infinite: rendering "+100%"
// or "+∞%" for a route's first traffic is how a comparison page starts lying.
func TestDeltaFromZeroHasNoPercentage(t *testing.T) {
	d := NewDelta(0, 42)
	if d.Meaningful {
		t.Error("a delta from zero claims a meaningful percentage; it has none")
	}
	if d.Abs != 42 {
		t.Errorf("abs = %v, want 42 — the absolute movement is still real", d.Abs)
	}

	ok := NewDelta(10, 15)
	if !ok.Meaningful {
		t.Error("a delta from a real reading should have a percentage")
	}
	if ok.Rel < 0.4999 || ok.Rel > 0.5001 {
		t.Errorf("rel = %v, want 0.5", ok.Rel)
	}

	// Both zero: no movement, and still no percentage to quote.
	none := NewDelta(0, 0)
	if none.Meaningful || none.Abs != 0 {
		t.Errorf("zero-to-zero delta = %+v, want no movement and no percentage", none)
	}
}

// TestDeltaVerdictFollowsTheMetricNotTheSign pins the direction rule. "Cache
// hit went up" and "cost went up" have the same sign and opposite meanings, and
// "requests went up" has no meaning at all — so the metric declares which.
func TestDeltaVerdictFollowsTheMetricNotTheSign(t *testing.T) {
	up := NewDelta(10, 20)
	down := NewDelta(20, 10)

	// Cost, error rate, latency: less is better.
	if up.Improved(LessIsBetter) {
		t.Error("a cost increase was reported as an improvement")
	}
	if !up.Worsened(LessIsBetter) {
		t.Error("a cost increase was not reported as a regression")
	}
	if !down.Improved(LessIsBetter) {
		t.Error("a cost decrease was not reported as an improvement")
	}
	// Cache hit: more is better.
	if !up.Improved(MoreIsBetter) {
		t.Error("a cache-hit increase was not reported as an improvement")
	}
	if down.Improved(MoreIsBetter) {
		t.Error("a cache-hit decrease was reported as an improvement")
	}
	if !down.Worsened(MoreIsBetter) {
		t.Error("a cache-hit decrease was not reported as a regression")
	}
	// Volume: neither, in either direction. This is the case a bool cannot
	// express and the page must not colour.
	for _, d := range []Delta{up, down} {
		if d.Improved(Neutral) || d.Worsened(Neutral) {
			t.Errorf("a neutral metric moving by %v claimed a verdict", d.Abs)
		}
		if d.Verdict(Neutral) != 0 {
			t.Errorf("neutral verdict = %d, want 0", d.Verdict(Neutral))
		}
	}
	// No movement is not a verdict in any direction.
	flat := NewDelta(10, 10)
	for _, dir := range []Direction{Neutral, LessIsBetter, MoreIsBetter} {
		if flat.Verdict(dir) != 0 {
			t.Errorf("a flat delta claimed verdict %d for direction %v", flat.Verdict(dir), dir)
		}
	}
}

// TestDirectionOfNamesEveryComparedMetric keeps the direction table honest: a
// metric added to WindowComparison without a direction silently becomes Neutral
// and renders uncoloured, which looks like a styling bug rather than a missing
// declaration.
func TestDirectionOfNamesEveryComparedMetric(t *testing.T) {
	for _, tc := range []struct {
		metric string
		want   Direction
	}{
		{"cost_usd", LessIsBetter},
		{"cost_per_1m", LessIsBetter},
		{"error_rate", LessIsBetter},
		{"avg_latency_ms", LessIsBetter},
		{"cache_hit_rate", MoreIsBetter},
		{"requests", Neutral},
		{"tokens", Neutral},
		{"something_new", Neutral},
	} {
		if got := DirectionOf(tc.metric); got != tc.want {
			t.Errorf("DirectionOf(%q) = %v, want %v", tc.metric, got, tc.want)
		}
	}
}

// TestCompareWindowsSeparatesRateFromVolume is the whole point of compare mode:
// a config change that halves the per-token price while traffic triples shows up
// as a raw cost *increase*. Only the rate metric answers "did this help?".
func TestCompareWindowsSeparatesRateFromVolume(t *testing.T) {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	boundary := base.Add(time.Hour)

	var events []Event
	// Before: 2 requests, 1000 tokens each, $1.00 each -> $2.00 / 2000 tok
	// = $1000 per 1M.
	for i := 0; i < 2; i++ {
		events = append(events, flowEvent(base.Add(time.Duration(i)*time.Second),
			"a", "p", "m", 500, 500, 0, 0, 1.0))
	}
	// After: 6 requests, 1000 tokens each, $0.50 each -> $3.00 / 6000 tok
	// = $500 per 1M. Raw cost went UP 50%, rate went DOWN 50%.
	for i := 0; i < 6; i++ {
		events = append(events, flowEvent(boundary.Add(time.Duration(i)*time.Second),
			"a", "p", "m", 500, 500, 0, 0, 0.5))
	}
	r := openSeeded(t, events...)

	cmp, err := r.CompareWindows(context.Background(),
		Window{Since: base.Add(-time.Hour), Until: boundary},
		Window{Since: boundary})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}

	if cmp.Before.Requests != 2 || cmp.After.Requests != 6 {
		t.Fatalf("windows = %d before / %d after, want 2/6",
			cmp.Before.Requests, cmp.After.Requests)
	}
	// Raw cost rose.
	if cmp.CostUSD.Abs <= 0 {
		t.Errorf("raw cost delta = %v, want positive (spend did rise)", cmp.CostUSD.Abs)
	}
	if cmp.CostUSD.Improved(LessIsBetter) {
		t.Error("a raw cost increase was reported as an improvement")
	}
	// The rate fell by half — this is the number that answers the question.
	if cmp.CostPer1M.Rel < -0.5001 || cmp.CostPer1M.Rel > -0.4999 {
		t.Errorf("cost/1M delta = %.4f, want -0.5 (halved)", cmp.CostPer1M.Rel)
	}
	if !cmp.CostPer1M.Improved(LessIsBetter) {
		t.Error("a halved per-token rate was not reported as an improvement")
	}
}

// TestCompareWindowsTracksCacheHitMovement covers the metric the ticket calls
// out as the page's primary exploration goal: a route whose cache hit rate
// collapsed is wasted spend, and the comparison must say so in the right
// direction.
func TestCompareWindowsTracksCacheHitMovement(t *testing.T) {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	boundary := base.Add(time.Hour)

	r := openSeeded(t,
		// Before: 900 cached of 1000 cacheable -> 90%.
		flowEvent(base, "a", "p", "m", 100, 10, 900, 0, 1.0),
		// After: 100 cached of 1000 cacheable -> 10%.
		flowEvent(boundary, "a", "p", "m", 900, 10, 100, 0, 1.0),
	)

	cmp, err := r.CompareWindows(context.Background(),
		Window{Since: base.Add(-time.Hour), Until: boundary},
		Window{Since: boundary})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if cmp.CacheHitRate.Before < 0.8999 || cmp.CacheHitRate.Before > 0.9001 {
		t.Errorf("before cache hit = %.4f, want 0.9", cmp.CacheHitRate.Before)
	}
	if cmp.CacheHitRate.After < 0.0999 || cmp.CacheHitRate.After > 0.1001 {
		t.Errorf("after cache hit = %.4f, want 0.1", cmp.CacheHitRate.After)
	}
	// Cache hit is a more-is-better metric, so this collapse must NOT read as
	// an improvement.
	if cmp.CacheHitRate.Improved(MoreIsBetter) {
		t.Error("a cache-hit collapse was reported as an improvement")
	}
	if cmp.CacheHitRate.Abs >= 0 {
		t.Errorf("cache hit delta = %v, want negative", cmp.CacheHitRate.Abs)
	}
}

// TestCompareWindowsHandlesAnEmptyBeforeSide is the real-world case: an anchor
// at the very start of recorded traffic has nothing before it. The comparison
// must still render, with absolute readings and no fabricated percentages.
func TestCompareWindowsHandlesAnEmptyBeforeSide(t *testing.T) {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	r := openSeeded(t, flowEvent(base, "a", "p", "m", 100, 10, 0, 0, 1.0))

	cmp, err := r.CompareWindows(context.Background(),
		Window{Since: base.Add(-2 * time.Hour), Until: base.Add(-time.Hour)}, // empty
		Window{Since: base.Add(-time.Minute)})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if cmp.Before.Requests != 0 {
		t.Fatalf("before side has %d requests, want 0", cmp.Before.Requests)
	}
	if cmp.After.Requests != 1 {
		t.Fatalf("after side has %d requests, want 1", cmp.After.Requests)
	}
	if cmp.Requests.Meaningful || cmp.CostUSD.Meaningful {
		t.Error("a comparison against an empty window claims meaningful percentages")
	}
	if cmp.CostUSD.Abs != 1.0 {
		t.Errorf("absolute cost movement = %v, want 1.0", cmp.CostUSD.Abs)
	}
	// An empty window's rates are zero, not NaN — a NaN would render as
	// "NaN%" on the page.
	if cmp.Before.CacheHitRate() != 0 || cmp.Before.CostPer1MTokens() != 0 {
		t.Error("an empty window produced a non-zero rate")
	}
}
