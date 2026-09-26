package ui

import (
	"context"
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

// Compare mode is entered by picking an anchor, not by a separate mode control.
// The earlier shape — a Single/Compare button pair beside an anchor select —
// produced three defects at once, all reported from real use: picking a change
// in single mode did nothing, clicking Compare with no change submitted straight
// to a 400 error page, and the form re-rendered without the selection so it was
// unclear what was on screen.
func TestOverviewModeFollowsTheAnchor(t *testing.T) {
	base := timeAt()
	anchor := base.Add(time.Hour)
	h, _ := newSeededHandler(t,
		flowEventFor(base, "coding", "claude", "sonnet-5", 100, 10, 0, 1.0, 200),
		flowEventFor(anchor.Add(time.Second), "coding", "claude", "sonnet-5", 100, 10, 800, 0.5, 200),
	)

	// An anchor alone switches to compare — no mode parameter needed.
	withAnchor := "/admin/ui/overview?since=" + testWindow +
		"&anchor=" + anchor.Format(time.RFC3339) + "&span=fixed"
	rec := serve(t, h, "GET", withAnchor, false)
	if rec.Code != 200 {
		t.Fatalf("anchored request = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "compare-on") {
		t.Error("an anchor did not put the page into compare mode")
	}

	// No anchor means a single window, and must not 400.
	rec = serve(t, h, "GET", "/admin/ui/overview?since="+testWindow, false)
	if rec.Code != 200 {
		t.Fatalf("unanchored request = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "compare-on") {
		t.Error("a page with no anchor claims to be comparing")
	}

	// There is no mode parameter left to contradict the anchor: an unknown one
	// is ignored rather than accepted as a second source of truth.
	rec = serve(t, h, "GET", "/admin/ui/overview?since="+testWindow+"&mode=compare", false)
	if rec.Code != 200 {
		t.Errorf("a stray mode parameter = %d, want it ignored with 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "compare-on") {
		t.Error("mode=compare without an anchor entered compare mode; the anchor is the only switch")
	}
}

// A selection must survive its own submit. Reported from real use: "pick a
// change gets reset whenever you click apply, makes it seem like it gets
// ignored".
func TestOverviewSelectionsPersistAcrossSubmit(t *testing.T) {
	base := timeAt()
	anchor := base.Add(time.Hour)

	// Sub-second precision on purpose: the store's real timestamps carry
	// nanoseconds, and the picker's option value is RFC3339, which does not.
	// A selection check that round-trips through that format must therefore
	// compare formatted values — comparing time.Equal against the reparsed
	// value always fails, which is precisely the "my pick got reset" bug.
	// Seeded data with clean timestamps hid it.
	nanos := 123456789 * time.Nanosecond

	var events []store.Event
	for i := 0; i < 3; i++ {
		ev := flowEventFor(base.Add(time.Duration(i)*time.Second+nanos), "coding", "claude", "sonnet-5", 100, 10, 0, 1.0, 200)
		ev.ConfigEpoch = "before"
		events = append(events, ev)
	}
	for i := 0; i < 3; i++ {
		ev := flowEventFor(anchor.Add(time.Duration(i)*time.Second+nanos), "coding", "claude", "sonnet-5", 100, 10, 800, 0.5, 200)
		ev.ConfigEpoch = "after"
		events = append(events, ev)
	}
	h, _ := newSeededHandler(t, events...)

	// The window select must mark the window actually in play.
	body := serve(t, h, "GET", "/admin/ui/overview?since=72h", false).Body.String()
	if !strings.Contains(body, `<option value="72h" selected>`) {
		t.Error("the window select did not reopen on the chosen window")
	}

	// And on the default page it must mark the default rather than leaving the
	// browser to display the first option — the bug that made the control look
	// inert: Duration.String() gives "24h0m0s", which matches no option value.
	body = serve(t, h, "GET", "/admin/ui/overview", false).Body.String()
	if !strings.Contains(body, `<option value="`+defaultSinceChoice+`" selected>`) {
		t.Errorf("the default page marks no window option selected, so the control shows the "+
			"wrong value; want %q selected", defaultSinceChoice)
	}

	// An anchor picked from the list must come back selected, so it is visible
	// that it took effect.
	epochs, err := h.reader.ConfigEpochs(context.Background(),
		store.Window{Since: base.Add(-time.Hour)}, 10)
	if err != nil {
		t.Fatalf("epochs: %v", err)
	}
	if len(epochs) == 0 {
		t.Fatal("no config anchors seeded")
	}
	picked := epochs[0]
	body = serve(t, h, "GET", "/admin/ui/overview?since="+testWindow+
		"&anchor="+picked.Started.Format(time.RFC3339)+"&span=fixed", false).Body.String()
	if !strings.Contains(body, `value="`+picked.Started.Format(time.RFC3339)+`" selected`) {
		t.Error("the picked config change did not come back selected; it reads as ignored")
	}

	// The span select must persist too.
	if !strings.Contains(body, `<option value="fixed" selected>`) {
		t.Error("the span select did not reopen on the chosen span")
	}
}

// A free-form moment works, and reopens in the datetime input. Reported: "there
// is no actual date picker. is that intentional?" — it was not.
func TestOverviewAcceptsAFreeFormMoment(t *testing.T) {
	base := timeAt()
	anchor := base.Add(time.Hour)
	h, _ := newSeededHandler(t,
		flowEventFor(base, "coding", "claude", "sonnet-5", 100, 10, 0, 1.0, 200),
		flowEventFor(anchor.Add(time.Second), "coding", "claude", "sonnet-5", 100, 10, 800, 0.5, 200),
	)

	// The layout a datetime-local input actually submits: no zone, no seconds.
	local := anchor.Format("2006-01-02T15:04")
	rec := serve(t, h, "GET", "/admin/ui/overview?since="+testWindow+
		"&anchor_at="+local+"&span=fixed", false)
	if rec.Code != 200 {
		t.Fatalf("free-form moment = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "compare-on") {
		t.Error("a free-form moment did not enter compare mode")
	}
	if !strings.Contains(body, `name="anchor_at" value="`+local+`"`) {
		t.Errorf("the datetime input did not reopen on %q", local)
	}

	// The free-form field wins over a stale select value, because it is the one
	// just typed into — the select still carries its previous value on submit.
	other := base.Add(30 * time.Minute).Format(time.RFC3339)
	body = serve(t, h, "GET", "/admin/ui/overview?since="+testWindow+
		"&anchor="+other+"&anchor_at="+local+"&span=fixed", false).Body.String()
	if !strings.Contains(body, `name="anchor_at" value="`+local+`"`) {
		t.Error("the select overrode the datetime field; the field the operator typed into must win")
	}

	// A malformed moment is a 400 that says what a good one looks like, rather
	// than the previous opaque "anchor must be an RFC3339 timestamp".
	rec = serve(t, h, "GET", "/admin/ui/overview?anchor_at=whenever", false)
	if rec.Code != 400 {
		t.Errorf("malformed moment = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "2026-09-23T19:08") {
		t.Error("the 400 does not show the expected timestamp shape")
	}
}

// The window control's label and liveness must match what it is doing. Reported:
// "can select window: 1h but the range pill will always say -> now, so what does
// last x time actually do? can it or can it not be used in conjunction with the
// other filters?"
func TestOverviewWindowControlSaysWhatItDoes(t *testing.T) {
	base := timeAt()
	anchor := base.Add(time.Hour)
	h, _ := newSeededHandler(t,
		flowEventFor(base, "coding", "claude", "sonnet-5", 100, 10, 0, 1.0, 200),
		flowEventFor(anchor.Add(time.Second), "coding", "claude", "sonnet-5", 100, 10, 800, 0.5, 200),
	)

	// Single window: the duration is a trailing window, labelled "last N".
	body := serve(t, h, "GET", "/admin/ui/overview?since=72h", false).Body.String()
	if !strings.Contains(body, "last 72h") {
		t.Error("single mode does not label the window as a trailing window")
	}

	// span=fixed: the duration applies to both sides, so it stays live and its
	// label changes to say so.
	body = serve(t, h, "GET", "/admin/ui/overview?since=72h&anchor="+
		anchor.Format(time.RFC3339)+"&span=fixed", false).Body.String()
	if !strings.Contains(body, "72h each side") {
		t.Error("compare+fixed does not label the window as applying to each side")
	}
	if strings.Contains(body, "set by the change") {
		t.Error("compare+fixed wrongly marks the window control inert; it does apply")
	}

	// span=to_now: the duration is genuinely not consulted, so the control is
	// disabled and annotated instead of sitting there looking live.
	body = serve(t, h, "GET", "/admin/ui/overview?since=72h&anchor="+
		anchor.Format(time.RFC3339)+"&span=to_now", false).Body.String()
	if !strings.Contains(body, "set by the change") {
		t.Error("span=to_now ignores the window but the control does not say so")
	}
	if !strings.Contains(body, "disabled") {
		t.Error("span=to_now ignores the window but the control is still enabled")
	}
	// The value is still carried, so switching span back does not silently
	// reset the window.
	if !strings.Contains(body, `type="hidden" name="since" value="72h"`) {
		t.Error("the ignored window value is not carried forward, so switching span would lose it")
	}
}

// Both compared windows are named with real bounds, so "what am I looking at"
// is answered on the page rather than inferred. Reported: "because things reset,
// it's not clear what you are actually looking at".
func TestOverviewNamesTheWindowsItShows(t *testing.T) {
	base := timeAt()
	anchor := base.Add(time.Hour)
	h, _ := newSeededHandler(t,
		flowEventFor(base, "coding", "claude", "sonnet-5", 100, 10, 0, 1.0, 200),
		flowEventFor(anchor.Add(time.Second), "coding", "claude", "sonnet-5", 100, 10, 800, 0.5, 200),
	)

	body := serve(t, h, "GET", "/admin/ui/overview?since=1h&anchor="+
		anchor.Format(time.RFC3339)+"&span=fixed", false).Body.String()

	// The anchor is 2026-09-16 06:00; with span=fixed and since=1h the two
	// windows are 05:00→06:00 and 06:00→07:00. Both bounds must be on the page.
	for _, want := range []string{"2026-09-16 05:00", "2026-09-16 06:00", "2026-09-16 07:00"} {
		if !strings.Contains(body, want) {
			t.Errorf("the toolbar does not name window bound %q", want)
		}
	}
	// And the single-window case names its own bounds, ending at "now".
	body = serve(t, h, "GET", "/admin/ui/overview?since=1h", false).Body.String()
	if !strings.Contains(body, "→ now") {
		t.Error("a trailing window does not say it runs to now")
	}
}

// defaultSinceChoice must be a real option and mean the same thing as
// overviewDefaultWindow. They are two literals and drifted apart once already,
// which is what made the window select display "last 1h" on a 24h page.
func TestDefaultSinceChoiceIsOffered(t *testing.T) {
	d, err := time.ParseDuration(defaultSinceChoice)
	if err != nil {
		t.Fatalf("defaultSinceChoice %q is not a duration: %v", defaultSinceChoice, err)
	}
	if d != overviewDefaultWindow {
		t.Errorf("defaultSinceChoice is %v but overviewDefaultWindow is %v — the select would "+
			"open on a window the page is not showing", d, overviewDefaultWindow)
	}
	var found bool
	for _, c := range sinceChoices() {
		if c == defaultSinceChoice {
			found = true
		}
	}
	if !found {
		t.Errorf("defaultSinceChoice %q is not one of the offered options %v",
			defaultSinceChoice, sinceChoices())
	}
}

// A bad parameter is a 400 naming it, never a silent fallback to a default.
func TestOverviewRejectsBadParameters(t *testing.T) {
	h, _ := newSeededHandler(t, flowEventFor(timeAt(), "a", "p", "m", 10, 1, 0, 0.1, 200))

	for _, tc := range []struct{ q, want string }{
		{"?since=7d", "since"},  // not a Go duration
		{"?since=-1h", "since"}, // negative
		{"?anchor=nope", "timestamp"},
		{"?anchor=2026-09-20T10:00:00Z&span=diagonal", "span"},
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

	url := "/admin/ui/overview?anchor=" + anchor.Format("2006-01-02T15:04:05") + "&span=fixed&since=1h"
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
