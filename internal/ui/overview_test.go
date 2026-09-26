package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// The Overview page's tests (#54). They replace the old pivot explorer's
// (ranking-by-metric, header/cell column counts) entirely: that page is gone,
// and its parameters — ?dim=, ?metric= — no longer exist, so the assertions had
// no surface left to hold onto.

// testWindow is wide enough to cover timeAt()'s fixed date, which is in the
// past. The page's own default is 24h — deliberately short, so that "what is
// happening now" is the first answer — and every test that seeds at timeAt()
// must therefore ask for a window that reaches it, or get a correctly empty page.
const testWindow = "20000h"

// flowEventFor builds a client request on one route, with the token counters the
// page's two rate metrics divide by.
func flowEventFor(ts time.Time, alias, provider, model string, in, out, cacheRead int64, cost float64, status int) store.Event {
	ev := store.Event{
		TraceID: "t", Provider: provider, Model: model, AliasUsed: alias,
		Ts: ts, LatencyMs: 100, StatusCode: status, Kind: "client",
	}
	ev.Usage.InputTokens = int(in)
	ev.Usage.OutputTokens = int(out)
	ev.Usage.CacheRead = int(cacheRead)
	ev.Usage.CostUSD = cost
	return ev
}

// The page renders a real diagram from real routes: one node per alias and per
// provider/model, one ribbon per route, and the numbers the ticket asks for.
func TestOverviewRendersRoutingFlow(t *testing.T) {
	base := timeAt()
	h, _ := newSeededHandler(t,
		flowEventFor(base, "coding", "claude", "sonnet-5", 100, 10, 900, 1.0, 200),
		flowEventFor(base.Add(time.Second), "coding", "claude", "sonnet-5", 100, 10, 900, 1.0, 200),
		flowEventFor(base.Add(2*time.Second), "arbiter", "openrouter", "deepseek", 500, 50, 100, 0.2, 200),
	)

	body := serve(t, h, "GET", "/admin/ui/overview?since="+testWindow, false).Body.String()

	// Both aliases and both models are present as clickable nodes.
	for _, want := range []string{
		`data-node="alias:coding"`,
		`data-node="alias:arbiter"`,
		`data-node="model:claude/sonnet-5"`,
		`data-node="model:openrouter/deepseek"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the diagram is missing node %s", want)
		}
	}
	// One ribbon per route, each a real path rather than an empty attribute.
	ribbons := regexp.MustCompile(`class="ribbon" d="M[0-9]`).FindAllString(body, -1)
	if len(ribbons) != 2 {
		t.Errorf("got %d drawn ribbons, want 2 routes", len(ribbons))
	}
	// Each node's click target fetches its own drawer.
	if !strings.Contains(body, "/admin/ui/overview/node?") {
		t.Error("nodes do not fetch a drawer fragment")
	}
}

// The KPI strip carries the metrics #54 names, and must not carry the one it
// explicitly rejects.
func TestOverviewKPIStripShowsTheAgreedMetrics(t *testing.T) {
	base := timeAt()
	h, _ := newSeededHandler(t,
		flowEventFor(base, "coding", "claude", "sonnet-5", 100, 10, 900, 1.0, 200),
	)
	body := serve(t, h, "GET", "/admin/ui/overview?since="+testWindow, false).Body.String()

	for _, want := range []string{"Requests", "Cost", "Cost / 1M tok", "Cache hit", "Error rate"} {
		if !strings.Contains(body, want) {
			t.Errorf("the KPI strip is missing %q", want)
		}
	}
	// Cost-per-request is rejected by the ticket: request sizes vary too much
	// for it to mean anything, and showing it invites exactly the wrong
	// comparison.
	if strings.Contains(body, "Cost / request") || strings.Contains(body, "cost per request") {
		t.Error("the page shows a cost-per-request metric, which #54 rejects")
	}
	// The estimate/metered distinction must be visible, not buried: claude
	// figures are API-equivalent estimates against a flat-rate plan.
	if !strings.Contains(body, "est.") {
		t.Error("the cost cell does not mark that some figures are estimates")
	}
}

// Compare mode needs an anchor, and says so rather than quietly showing a
// single-window page — a page that answers a different question than the one
// asked is worse than an error here, since the whole point is attributing a
// change to a cause.
func TestOverviewCompareRequiresAnAnchor(t *testing.T) {
	h, _ := newSeededHandler(t, flowEventFor(timeAt(), "a", "p", "m", 10, 1, 0, 0.1, 200))

	rec := serve(t, h, "GET", "/admin/ui/overview?mode=compare", false)
	if rec.Code != 400 {
		t.Fatalf("compare without an anchor = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "anchor") {
		t.Error("the 400 does not name the missing parameter")
	}
}

// Compare mode renders both windows and a delta per metric.
func TestOverviewCompareShowsDeltas(t *testing.T) {
	base := timeAt()
	anchor := base.Add(time.Hour)

	var events []store.Event
	// Before the anchor: expensive per token.
	for i := 0; i < 2; i++ {
		events = append(events, flowEventFor(base.Add(time.Duration(i)*time.Second),
			"coding", "claude", "sonnet-5", 500, 500, 0, 2.0, 200))
	}
	// After: cheaper per token, and better cached.
	for i := 0; i < 2; i++ {
		events = append(events, flowEventFor(anchor.Add(time.Duration(i)*time.Second),
			"coding", "claude", "sonnet-5", 100, 100, 800, 0.5, 200))
	}
	h, _ := newSeededHandler(t, events...)

	url := "/admin/ui/overview?mode=compare&anchor=" + anchor.Format("2006-01-02T15:04:05") + "&span=fixed&since=1h"
	rec := serve(t, h, "GET", url, false)
	if rec.Code != 200 {
		t.Fatalf("compare = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// compare-on gates the delta rendering.
	if !strings.Contains(body, "compare-on") {
		t.Error("compare mode did not mark the page as compared")
	}
	// A verdict class must be present: the cheaper rate is an improvement, and
	// the page colours from the metric's direction rather than the sign.
	if !strings.Contains(body, `class="delta v1"`) {
		t.Error("no improvement verdict rendered; the per-token rate fell and should read as better")
	}
	// Both window labels are shown, so it is clear what is being compared.
	if !strings.Contains(body, "vs") {
		t.Error("compare mode does not show the two windows it compared")
	}
}

// A bad parameter is a 400 naming it, never a silent fallback to a default.
func TestOverviewRejectsBadParameters(t *testing.T) {
	h, _ := newSeededHandler(t, flowEventFor(timeAt(), "a", "p", "m", 10, 1, 0, 0.1, 200))

	for _, tc := range []struct{ q, want string }{
		{"?since=7d", "since"},     // not a Go duration
		{"?since=-1h", "since"},    // negative
		{"?mode=sideways", "mode"}, // unknown mode
		{"?mode=compare&anchor=nope", "anchor"},
		{"?mode=compare&anchor=2026-09-20T10:00:00&span=diagonal", "span"},
	} {
		rec := serve(t, h, "GET", "/admin/ui/overview"+tc.q, false)
		if rec.Code != 400 {
			t.Errorf("%s = %d, want 400", tc.q, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: the 400 does not name %q", tc.q, tc.want)
		}
	}
}

// The drawer is a server-rendered fragment carrying the same numbers the page
// computed, and a stale node id is a 404 rather than an empty panel — an empty
// panel is a different and misleading claim ("this node has no data").
func TestOverviewNodeDrawer(t *testing.T) {
	base := timeAt()
	h, _ := newSeededHandler(t,
		flowEventFor(base, "coding", "claude", "sonnet-5", 100, 10, 900, 1.0, 200),
		flowEventFor(base.Add(time.Second), "coding", "openrouter", "deepseek", 100, 10, 0, 0.1, 200),
	)

	rec := serve(t, h, "GET", "/admin/ui/overview/node?since="+testWindow+"&id=alias:coding", true)
	if rec.Code != 200 {
		t.Fatalf("drawer = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "coding") {
		t.Error("the drawer does not name the node")
	}
	// An alias's drawer lists where it went — both destinations.
	for _, want := range []string{"claude/sonnet-5", "openrouter/deepseek"} {
		if !strings.Contains(body, want) {
			t.Errorf("the alias drawer does not list destination %q", want)
		}
	}
	if !strings.Contains(body, "Cost / 1M tokens") {
		t.Error("the drawer does not show the per-token rate")
	}

	// A node that is not in this window is a 404.
	rec = serve(t, h, "GET", "/admin/ui/overview/node?since="+testWindow+"&id=alias:nonexistent", true)
	if rec.Code != 404 {
		t.Errorf("unknown node = %d, want 404", rec.Code)
	}
	// A missing id is a 400, not a 404: the request is malformed rather than
	// pointing at something absent.
	rec = serve(t, h, "GET", "/admin/ui/overview/node", true)
	if rec.Code != 400 {
		t.Errorf("missing id = %d, want 400", rec.Code)
	}
}

// A route whose every request failed is misconfiguration debris — an alias whose
// target is the alias name itself, which exists in the real data. It must render
// and be marked, not be smoothed away: that it appears at all is the finding.
func TestOverviewSurfacesAllErroredRoutes(t *testing.T) {
	base := timeAt()
	var events []store.Event
	events = append(events, flowEventFor(base, "coding", "claude", "sonnet-5", 100, 10, 900, 1.0, 200))
	for i := 0; i < 3; i++ {
		// alias "broken" routed to a model named after the alias, every call 500.
		events = append(events, flowEventFor(base.Add(time.Duration(i+1)*time.Second),
			"broken", "claude", "broken", 0, 0, 0, 0, 500))
	}
	h, _ := newSeededHandler(t, events...)

	body := serve(t, h, "GET", "/admin/ui/overview?since="+testWindow, false).Body.String()
	if !strings.Contains(body, `data-node="alias:broken"`) {
		t.Fatal("an all-errored route was dropped from the diagram")
	}
	if !strings.Contains(body, "all-errored") {
		t.Error("an all-errored route is not marked as such")
	}
	// It must not read as free: $0 spend on a broken route is not a bargain.
	if !strings.Contains(body, "every request failed") {
		t.Error("an all-errored node does not say so; $0 cost would read as free")
	}
}

// An empty window says so rather than rendering an empty diagram box, which
// looks broken rather than idle.
func TestOverviewEmptyWindowSaysSo(t *testing.T) {
	h, _ := newSeededHandler(t)
	body := serve(t, h, "GET", "/admin/ui/overview?since=1h", false).Body.String()
	if !strings.Contains(body, "No client traffic") {
		t.Error("an empty window does not say it is empty")
	}
}

// The nav's Overview stat is a real query, not the mockup's "$18" placeholder.
func TestNavOverviewStatIsReal(t *testing.T) {
	h, _ := newSeededHandler(t,
		flowEventFor(timeAt(), "coding", "claude", "sonnet-5", 100, 10, 0, 7.0, 200))

	body := serve(t, h, "GET", "/admin/ui/overview?since="+testWindow, false).Body.String()
	if strings.Contains(body, "$18") {
		t.Error("the nav still shows the mockup's hardcoded $18 placeholder")
	}
	if !strings.Contains(body, "/24h") {
		t.Error("the nav's Overview stat lost its window unit")
	}
}
