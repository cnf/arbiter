package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// The Overview page (#54): where requests go, and whether a config change made
// that better or worse.
//
// Ported from design/overview-mockups/f3-overview-styled.html, which is this
// page's spec. Nothing of the previous Overview (a dimension×metric pivot table
// with a uPlot chart) survives — the rebuild is greenfield by instruction, so
// the old page's structure and metric choices are not inputs here.
//
// The page has three parts: a KPI strip (one store query), a routing-flow sankey
// (one store query, laid out in sankey.go), and a per-node detail drawer fetched
// as a fragment on click. Compare mode re-renders the whole page against two
// windows rather than toggling client-side visibility, because the deltas are
// computed in the store and there is nothing for the client to recompute.

// overviewDefaultWindow is the trailing window the page opens on.
//
// 24h rather than the 7d the flow query can comfortably serve: the page's first
// job is "what is happening now", and a week-long default hides a change made
// this morning inside six days of prior traffic. The range picker reaches
// further when asked.
const overviewDefaultWindow = 24 * time.Hour

// overviewMaxEpochAnchors bounds the anchor dropdown. Past a few dozen entries a
// select is not a usable way to pick a moment anyway, and the list is already
// collapsed by editing burst (see store.ConfigEpochs).
const overviewMaxEpochAnchors = 30

// overviewMode is how the page is being read: one window, or two compared.
//
// It is *derived* from whether an anchor was given, not set by a separate
// control. An earlier version had a Single/Compare button pair alongside an
// anchor select, which produced three defects at once: picking an anchor in
// single mode did nothing (so the selection looked ignored), clicking Compare
// with no anchor submitted straight to a 400, and after either one the form
// re-rendered without the selection so it was unclear what was on screen. One
// input cannot disagree with itself.
type overviewMode string

const (
	modeSingle  overviewMode = "single"
	modeCompare overviewMode = "compare"
)

// anchorSpan is how the after-window is measured from a compare anchor.
type anchorSpan string

const (
	// spanToNow: anchor → now, with an equal-length window before the anchor.
	// This is the "I changed the config this morning, is it better?" case.
	spanToNow anchorSpan = "to_now"

	// spanFixed: a fixed duration on each side of the anchor, which is what
	// makes two long-settled configs comparable without the after-side growing
	// every hour.
	spanFixed anchorSpan = "fixed"
)

// kpi is one cell of the summary strip: a label, a formatted value, and — in
// compare mode — the movement, already resolved to a verdict so the template
// only picks a class.
type kpi struct {
	Label string
	Value string

	// Note is a qualifier rendered smaller beside the label, used for the cost
	// cell: claude figures are API-equivalent estimates, not metered billing,
	// and the page must never present the two as one undifferentiated number.
	Note string

	// Delta is present only in compare mode.
	Delta    string
	Verdict  int // +1 better, 0 neutral/no movement, -1 worse
	HasDelta bool
}

// flowNodeDetail is the drawer's content for one node.
type flowNodeDetail struct {
	Label string
	Sub   string
	Color string

	Stats []detailStat

	// CacheHit is -1 when the node moved no cacheable tokens, which the drawer
	// renders as an explicit "nothing cacheable here" rather than a 0% gauge
	// implying every read missed.
	CacheHit float64

	// Routes lists the individual routes behind this node, so clicking an alias
	// shows where it actually went (and clicking a model shows who asked for
	// it).
	Routes []routeRow

	// ContentMixDeferred is always true today: the drawer keeps the mockup's
	// content-mix panel as a labelled placeholder. The breakdown needs a
	// byte-length aggregate over content_refs joined by role/block_type, which
	// is a separate piece of work (and on the table PICKUP.md warns is slow).
	// Rendering the placeholder rather than dropping the panel keeps the
	// drawer's three-column shape and says out loud that the number is missing,
	// not zero.
	ContentMixDeferred bool
}

// detailStat is one label/value row in the drawer, with an optional severity so
// a bad error rate reads as bad.
type detailStat struct {
	Label string
	Value string
	Class string // "", "warn", "bad", "good"
}

// routeRow is one route inside a drawer, from the perspective of the node that
// was clicked.
type routeRow struct {
	Label    string
	Requests int64
	Cost     string
	Per1M    string
	Cache    string
	Errors   string
	Bad      bool
}

// overviewView is the whole page.
type overviewView struct {
	viewBase

	Mode overviewMode

	// Window is the window actually queried, echoed back so the range pill
	// shows real bounds rather than the requested duration.
	SinceLabel string
	UntilLabel string
	SinceRaw   string

	// Compare-mode inputs, echoed for the form.
	AnchorRaw  string
	AnchorTime time.Time

	// AnchorLocal is the anchor in the layout a datetime-local input needs
	// (no zone, no seconds), so the free-form picker reopens on the value in
	// play rather than blank.
	AnchorLocal string

	Span    anchorSpan
	SpanRaw string

	// AnchorIsListed reports whether the anchor in play is one of the offered
	// config changes. When false the anchor came from the free-form field, and
	// the select must show its neutral option rather than appearing to have
	// selected something.
	AnchorIsListed bool

	// BeforeLabel/AfterLabel describe the two compared windows for the toolbar.
	BeforeLabel string
	AfterLabel  string

	// SpanIgnoresWindow reports that the window duration is not being applied,
	// which happens with span=to_now: both sides are measured from the anchor
	// to now instead. The toolbar disables and annotates the window control
	// rather than leaving it looking live — a control that silently stops
	// mattering is indistinguishable from one that is broken.
	SpanIgnoresWindow bool

	KPIs []kpi

	Flow sankey

	// Epochs are the config-change anchors offered by the picker.
	Epochs []epochChoice

	// Summary/Comparison back the strip. Only one is populated, per Mode.
	Summary    store.WindowSummary
	Comparison store.WindowComparison

	// EdgeCount/FoldedRoutes describe what the diagram is showing, so a capped
	// flow is visibly capped.
	EdgeCount    int
	FoldedRoutes int
}

// epochChoice is one anchor in the picker.
type epochChoice struct {
	Value string // the form value: an RFC3339 timestamp
	Label string
	// Selected marks the anchor currently in play, so the select reopens on it.
	Selected bool
}

// OverviewHandler handles GET /admin/ui/overview.
//
// Two shapes, chosen by `mode`:
//   - single (default): one trailing window, `since` a Go duration.
//   - compare: an `anchor` timestamp with `span` deciding how far each side
//     reaches. The before-window is [anchor-span, anchor) and the after-window
//     is [anchor, anchor+span) — or [anchor, now) when span is "to_now".
//
// A bad parameter is a 400 naming it, never a silent fallback to a default: a
// page that quietly answers a different question than the one asked is worse
// than an error, and this page's whole purpose is attributing a change to a
// cause.
func (h *Handler) OverviewHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	q := r.URL.Query()

	view := overviewView{
		viewBase:  h.base(r.Context(), "Overview"),
		Mode:      modeSingle,
		SinceRaw:  q.Get("since"),
		AnchorRaw: q.Get("anchor"),
		SpanRaw:   q.Get("span"),
		Span:      spanToNow,
	}
	view.Title = "overview"

	since := overviewDefaultWindow
	if raw := q.Get("since"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			h.fail(w, r, http.StatusBadRequest,
				`since must be a positive Go duration, e.g. "24h" or "168h"`)
			return
		}
		since = d
	}
	// SinceRaw is echoed into the window select, so it must hold a value the
	// select actually offers. Empty means "the default", and the default's
	// Duration.String() form ("24h0m0s") matches no option — which left the
	// control showing "last 1h" on a page rendering 24h of data.
	if view.SinceRaw == "" {
		view.SinceRaw = defaultSinceChoice
	}

	// Mode follows the anchor: an anchor means compare, no anchor means single.
	// There is no separate mode parameter to contradict it.
	//
	// Two inputs can supply it — the config-change select (`anchor`) and the
	// free-form datetime picker (`anchor_at`). The picker wins when both are
	// set, because that is the one the operator just typed into: the select
	// still carries its previous value on submit, so preferring it would make
	// the picker appear to do nothing.
	anchorRaw := q.Get("anchor")
	if at := q.Get("anchor_at"); at != "" {
		anchorRaw = at
	}
	if anchorRaw != "" {
		t, err := parseAnchor(anchorRaw)
		if err != nil {
			h.fail(w, r, http.StatusBadRequest,
				"the compare moment must be a timestamp like 2026-09-23T19:08 — got "+anchorRaw)
			return
		}
		view.AnchorTime = t
		view.Mode = modeCompare
	}

	if raw := q.Get("span"); raw != "" {
		switch anchorSpan(raw) {
		case spanToNow:
			view.Span = spanToNow
		case spanFixed:
			view.Span = spanFixed
		default:
			h.fail(w, r, http.StatusBadRequest, `span must be "to_now" or "fixed"`)
			return
		}
	}

	// AnchorRaw is echoed into the form. It is normalised to the picker's own
	// format rather than passed through verbatim, so a hand-typed
	// "2026-09-23T19:08" still matches the option whose value is the RFC3339
	// form — otherwise the select silently reopens on "— pick a change —" and
	// the selection looks ignored.
	if !view.AnchorTime.IsZero() {
		view.AnchorRaw = view.AnchorTime.Format(time.RFC3339)
		// The datetime-local input needs its own layout: no zone, no seconds.
		view.AnchorLocal = view.AnchorTime.Format("2006-01-02T15:04")
	}

	if h.reader == nil {
		h.render(w, r, "overview", "overview-body", view)
		return
	}

	var (
		flowWindow store.Window
		err        error
	)
	switch view.Mode {
	case modeCompare:
		before, after := compareWindows(view.AnchorTime, since, view.Span)
		cmp, cerr := h.reader.CompareWindows(r.Context(), before, after)
		if cerr != nil {
			h.logger.LogError(r.Context(), "error", cerr,
				map[string]interface{}{"phase": "admin_ui_overview_compare"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+cerr.Error())
			return
		}
		view.Comparison = cmp
		view.Summary = cmp.After
		view.KPIs = compareKPIs(cmp)
		view.BeforeLabel = windowLabel(before)
		view.AfterLabel = windowLabel(after)
		// The diagram shows the *after* window: "where does traffic go now",
		// with the strip carrying the movement. Two diagrams side by side was
		// the rejected e-flow-compare direction.
		flowWindow = after
		view.SinceLabel = fmtWindowEdge(after.Since)
		view.UntilLabel = untilLabel(after)
		// With span=to_now the after-side runs to now and the before-side
		// matches its elapsed length, so the window duration is not consulted
		// at all. Saying so is the point: a control that silently stops
		// applying is exactly the confusion this round is fixing.
		view.SpanIgnoresWindow = view.Span == spanToNow
	default:
		flowWindow = store.Window{Since: time.Now().UTC().Add(-since)}
		sum, serr := h.reader.SummarizeWindow(r.Context(), flowWindow)
		if serr != nil {
			h.logger.LogError(r.Context(), "error", serr,
				map[string]interface{}{"phase": "admin_ui_overview_summary"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+serr.Error())
			return
		}
		view.Summary = sum
		view.KPIs = singleKPIs(sum)
		view.SinceLabel = fmtWindowEdge(flowWindow.Since)
		view.UntilLabel = "now"
	}

	edges, err := h.reader.RoutingFlow(r.Context(), flowWindow, store.MaxRoutingEdges)
	if err != nil {
		h.logger.LogError(r.Context(), "error", err,
			map[string]interface{}{"phase": "admin_ui_overview_flow"})
		h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	view.Flow = buildSankey(edges)
	view.EdgeCount = len(edges)
	for _, e := range edges {
		if e.IsRemainder() {
			view.FoldedRoutes = e.FoldedRoutes
		}
	}

	view.Epochs = h.epochChoices(r, view.AnchorTime)
	for _, e := range view.Epochs {
		if e.Selected {
			view.AnchorIsListed = true
		}
	}

	h.render(w, r, "overview", "overview-body", view)
}

// epochChoices builds the anchor picker's options from real config changes.
//
// It degrades to an empty list on error rather than failing the page: the picker
// is a convenience over a free-form datetime input, and losing it should not
// cost the operator the whole Overview.
func (h *Handler) epochChoices(r *http.Request, selected time.Time) []epochChoice {
	// A generous lookback: the picker's whole value is reaching a change that
	// happened before the window currently on screen.
	w := store.Window{Since: time.Now().UTC().Add(-30 * 24 * time.Hour)}
	anchors, err := h.reader.ConfigEpochs(r.Context(), w, overviewMaxEpochAnchors)
	if err != nil {
		h.logger.LogError(r.Context(), "warn", err,
			map[string]interface{}{"phase": "admin_ui_overview_epochs"})
		return nil
	}
	out := make([]epochChoice, 0, len(anchors))
	for _, a := range anchors {
		if a.Started.IsZero() {
			continue
		}
		label := fmt.Sprintf("%s · %s · %s req",
			a.Started.Format("Jan 02 15:04"), shortEpoch(a.Epoch), fmtTokens(a.Requests))
		if a.Merged > 1 {
			// Saying so matters: the operator made several saves and this is
			// the one that actually served traffic.
			label += fmt.Sprintf(" · settled after %d saves", a.Merged)
		}
		// Selection is decided on the *formatted* value, not on time.Equal.
		// The option's value is RFC3339, which carries no sub-second part, so a
		// stored timestamp's nanoseconds are lost in the round trip and an
		// Equal comparison against the reparsed value always fails — the select
		// then reopened blank and the choice read as ignored. Seeded test data
		// hid this because its timestamps have zero nanoseconds; live rows do
		// not.
		value := a.Started.Format(time.RFC3339)
		out = append(out, epochChoice{
			Value:    value,
			Label:    label,
			Selected: !selected.IsZero() && selected.Format(time.RFC3339) == value,
		})
	}
	return out
}

// compareWindows builds the before/after pair around an anchor.
//
// The before-side is always a closed window ending exactly at the anchor, so the
// two sides tile without double-counting the request on the boundary (see
// store.Window's own comment). With spanToNow the after-side is open-ended and
// the before-side matches its *elapsed* length, which is what makes the two
// comparable — a fixed-length before against a still-growing after would show a
// spurious volume drop that shrinks as the day goes on.
func compareWindows(anchor time.Time, span time.Duration, mode anchorSpan) (before, after store.Window) {
	if mode == spanToNow {
		elapsed := time.Since(anchor)
		if elapsed <= 0 {
			elapsed = span
		}
		return store.Window{Since: anchor.Add(-elapsed), Until: anchor},
			store.Window{Since: anchor}
	}
	return store.Window{Since: anchor.Add(-span), Until: anchor},
		store.Window{Since: anchor, Until: anchor.Add(span)}
}

// parseAnchor accepts both what the picker emits (RFC3339) and what a raw
// datetime-local input emits (no zone). A zoneless value is read as UTC,
// matching the rest of the UI, which labels every timestamp UTC.
func parseAnchor(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable anchor %q", raw)
}

// singleKPIs is the strip for one window.
//
// The cost cell carries a standing qualifier rather than a footnote alone: the
// claude figures are API-equivalent estimates (the real billing is a flat
// monthly plan), openrouter's are metered, and blending them into one
// unlabelled number is the specific thing #54 says never to do.
func singleKPIs(s store.WindowSummary) []kpi {
	return []kpi{
		{Label: "Requests", Value: fmtTokens(s.Requests)},
		{Label: "Sessions", Value: fmtTokens(s.Sessions)},
		{Label: "Cost", Note: "(partly est.)", Value: fmtUSD(s.CostUSD)},
		{Label: "Cost / 1M tok", Value: fmtUSD(s.CostPer1MTokens())},
		{Label: "Cache hit", Value: cacheLabel(s.Measures)},
		{Label: "Error rate", Value: fmtPct(s.ErrorRate())},
	}
}

// compareKPIs is the strip for two windows: the after-window's value, plus the
// movement and its verdict.
//
// Verdicts come from store.DirectionOf rather than from the sign, so "cache hit
// rose" is green while "cost rose" is red, and request volume gets no colour at
// all — volume moving is not good or bad news.
func compareKPIs(c store.WindowComparison) []kpi {
	cell := func(label, note, value, metric string, d store.Delta, unit deltaUnit) kpi {
		return kpi{
			Label:    label,
			Note:     note,
			Value:    value,
			Delta:    fmtDelta(d, unit),
			Verdict:  d.Verdict(store.DirectionOf(metric)),
			HasDelta: true,
		}
	}
	return []kpi{
		cell("Requests", "", fmtTokens(c.After.Requests), "requests", c.Requests, unitCount),
		cell("Cost", "(partly est.)", fmtUSD(c.After.CostUSD), "cost_usd", c.CostUSD, unitUSD),
		cell("Cost / 1M tok", "", fmtUSD(c.After.CostPer1MTokens()), "cost_per_1m", c.CostPer1M, unitUSD),
		cell("Cache hit", "", cacheLabel(c.After.Measures), "cache_hit_rate", c.CacheHitRate, unitPoints),
		cell("Error rate", "", fmtPct(c.After.ErrorRate()), "error_rate", c.ErrorRate, unitPoints),
		cell("Avg latency", "", fmtDur(c.After.AvgLatencyMs), "avg_latency_ms", c.AvgLatencyMs, unitMs),
	}
}

// deltaUnit decides how a movement is spelled.
type deltaUnit int

const (
	unitCount deltaUnit = iota
	unitUSD
	unitPoints // a rate, so the absolute movement is in percentage *points*
	unitMs
)

// fmtDelta renders a movement, preferring the relative form and falling back to
// the absolute one when there is no meaningful percentage.
//
// A rate's absolute movement is quoted in points ("+16pt"), never in percent:
// "cache hit +21%" is ambiguous between "rose by 21 points" and "rose by 21% of
// its former value", and on this page the difference matters.
func fmtDelta(d store.Delta, unit deltaUnit) string {
	if d.Abs == 0 {
		return "no change"
	}
	if d.Meaningful {
		return fmt.Sprintf("%+.0f%%", d.Rel*100)
	}
	// No prior reading to compare against: quote the absolute arrival instead
	// of a fabricated percentage.
	switch unit {
	case unitUSD:
		return "new: " + fmtUSD(d.After)
	case unitPoints:
		return fmt.Sprintf("new: %.1fpt", d.After*100)
	case unitMs:
		return "new: " + fmtDur(int64(d.After))
	default:
		return fmt.Sprintf("new: %.0f", d.After)
	}
}

// cacheLabel renders a cache-hit rate, distinguishing "0%" from "nothing here
// was cacheable" — the two look identical as a number and mean opposite things.
func cacheLabel(m store.Measures) string {
	if !m.Cacheable() {
		return "—"
	}
	return fmtPct(m.CacheHitRate())
}

// windowLabel describes a window for the toolbar.
func windowLabel(w store.Window) string {
	return fmtWindowEdge(w.Since) + " → " + untilLabel(w)
}

func untilLabel(w store.Window) string {
	if !w.Bounded() {
		return "now"
	}
	return fmtWindowEdge(w.Until)
}

// fmtWindowEdge renders a window bound. It takes a time.Time rather than going
// through fmtClock, which parses the store's own text layout — these bounds are
// computed in Go and were never stored, so there is nothing to parse.
func fmtWindowEdge(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") }

// shortEpoch truncates a config hash for display, same reasoning as shortHash:
// an opaque handle is shown short and carried whole.
func shortEpoch(epoch string) string {
	if len(epoch) <= 8 {
		return epoch
	}
	return epoch[:8]
}

// OverviewNodeHandler handles GET /admin/ui/overview/node?id=…: the drawer for
// one sankey node.
//
// A server-rendered fragment rather than a JS blob in the page, matching
// Discovery's workspace pane (#50): the numbers stay formatted in one place
// instead of duplicating fmtUSD/fmtPct into JavaScript, and the page's initial
// HTML does not carry detail for two dozen nodes nobody clicked.
//
// The node id is the opaque key buildSankey assigned ("alias:coding",
// "model:claude/claude-sonnet-5"), so the drawer re-derives its content from the
// same edge list the diagram was built from — one query, and no risk of the
// drawer describing a different window than the picture.
func (h *Handler) OverviewNodeHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	q := r.URL.Query()
	id := q.Get("id")
	if id == "" {
		h.fail(w, r, http.StatusBadRequest, "id is required")
		return
	}

	since := overviewDefaultWindow
	if raw := q.Get("since"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			h.fail(w, r, http.StatusBadRequest, `since must be a positive Go duration`)
			return
		}
		since = d
	}

	window := store.Window{Since: time.Now().UTC().Add(-since)}
	// In compare mode the diagram shows the after-window, so the drawer must
	// describe that same window or the two would disagree.
	if anchorRaw := q.Get("anchor"); anchorRaw != "" {
		t, err := parseAnchor(anchorRaw)
		if err != nil {
			h.fail(w, r, http.StatusBadRequest, "anchor must be an RFC3339 timestamp")
			return
		}
		span := spanToNow
		if anchorSpan(q.Get("span")) == spanFixed {
			span = spanFixed
		}
		_, after := compareWindows(t, since, span)
		window = after
	}

	if h.reader == nil {
		h.fail(w, r, http.StatusServiceUnavailable, "the event store is disabled")
		return
	}
	edges, err := h.reader.RoutingFlow(r.Context(), window, store.MaxRoutingEdges)
	if err != nil {
		h.logger.LogError(r.Context(), "error", err,
			map[string]interface{}{"phase": "admin_ui_overview_node"})
		h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}

	detail, ok := buildNodeDetail(id, edges)
	if !ok {
		// A stale id (the window moved on, or a hand-edited URL) is a 404 rather
		// than an empty drawer: an empty panel looks like a node with no data,
		// which is a different and misleading claim.
		h.fail(w, r, http.StatusNotFound, "no such node in this window: "+id)
		return
	}
	h.exec(w, r, h.fragments, "fragments", "overview-node", detail)
}

// OverviewNodeCloseHandler handles GET /admin/ui/overview/node/close: the
// drawer's empty state.
//
// A route rather than client-side JS removing the panel, so dismissing the
// drawer is the same kind of operation as opening it — a fragment swap — and the
// empty state's copy has one definition instead of being duplicated into a JS
// string.
func (h *Handler) OverviewNodeCloseHandler(w http.ResponseWriter, r *http.Request) {
	h.exec(w, r, h.fragments, "fragments", "overview-node-empty", nil)
}

// buildNodeDetail assembles one node's drawer from the window's edges.
func buildNodeDetail(id string, edges []store.RoutingEdge) (flowNodeDetail, bool) {
	var (
		d        flowNodeDetail
		agg      store.Measures
		found    bool
		provider string
	)
	isAlias := len(id) > 6 && id[:6] == "alias:"
	key := ""
	if isAlias {
		key = id[6:]
	} else if len(id) > 6 && id[:6] == "model:" {
		key = id[6:]
	} else {
		return d, false
	}
	remainder := key == "__remainder__"

	for _, e := range edges {
		match := false
		switch {
		case remainder:
			match = e.IsRemainder()
		case isAlias:
			match = !e.IsRemainder() && e.Alias == key
		default:
			match = !e.IsRemainder() && e.Provider+"/"+e.Model == key
		}
		if !match {
			continue
		}
		found = true
		provider = e.Provider
		agg.Add(e.Measures)

		// The route rows describe the *other* end: an alias's drawer lists the
		// models it reached, a model's drawer lists who asked for it.
		label := aliasLabel(e.Alias)
		if isAlias {
			label = e.Provider + "/" + e.Model
		}
		if e.IsRemainder() {
			label = fmt.Sprintf("%d folded routes", e.FoldedRoutes)
		}
		row := routeRow{
			Label:    label,
			Requests: e.Requests,
			Cost:     fmtUSD(e.CostUSD),
			Per1M:    fmtUSD(e.CostPer1MTokens()),
			Cache:    cacheLabel(e.Measures),
			Errors:   fmtPct(e.ErrorRate()),
			Bad:      e.Requests > 0 && e.Errors == e.Requests,
		}
		d.Routes = append(d.Routes, row)
	}
	if !found {
		return d, false
	}

	switch {
	case remainder:
		d.Label = "other routes"
		d.Sub = fmt.Sprintf("%s requests across the routes past the diagram's cap",
			fmtTokens(agg.Requests))
	case isAlias:
		d.Label = aliasLabel(key)
		d.Sub = fmt.Sprintf("%s requests · %s · %d destination(s)",
			fmtTokens(agg.Requests), fmtUSD(agg.CostUSD), len(d.Routes))
	default:
		d.Label = key
		d.Sub = fmt.Sprintf("%s requests · %s", fmtTokens(agg.Requests), fmtUSD(agg.CostUSD))
		if provider == "claude" || provider == "anthropic" {
			// Never let an estimate pass as metered spend.
			d.Sub += " (API-equivalent estimate)"
		}
	}
	d.Color = providerColor(provider)
	if isAlias || remainder {
		d.Color = providerColor("")
	}

	d.Stats = []detailStat{
		{Label: "Cost / 1M tokens", Value: fmtUSD(agg.CostPer1MTokens())},
		{Label: "Tokens", Value: fmtTokens(agg.Tokens)},
		{Label: "Avg latency", Value: fmtDur(agg.AvgLatencyMs), Class: latencyClass(agg.AvgLatencyMs)},
		{Label: "Error rate", Value: fmtPct(agg.ErrorRate()), Class: errorClass(agg.ErrorRate())},
		{Label: "Requests", Value: fmtTokens(agg.Requests)},
	}
	d.CacheHit = -1
	if agg.Cacheable() {
		d.CacheHit = agg.CacheHitRate()
	}
	d.ContentMixDeferred = true
	return d, true
}

// errorClass and latencyClass turn a measurement into a severity the template
// only has to name. The thresholds are here rather than in CSS because they are
// judgements about this deployment's traffic, not styling.
func errorClass(rate float64) string {
	switch {
	case rate >= 0.05:
		return "bad"
	case rate > 0:
		return "warn"
	default:
		return "good"
	}
}

func latencyClass(ms int64) string {
	switch {
	case ms >= 30_000:
		return "warn"
	default:
		return ""
	}
}

// overviewURL builds a link back to the page with one parameter changed, used by
// the toolbar's own controls so no template assembles a query string by hand.
func overviewURL(mode, since, anchor, span string) string {
	v := url.Values{}
	if mode != "" {
		v.Set("mode", mode)
	}
	if since != "" {
		v.Set("since", since)
	}
	if anchor != "" {
		v.Set("anchor", anchor)
	}
	if span != "" {
		v.Set("span", span)
	}
	if len(v) == 0 {
		return "/admin/ui/overview"
	}
	return "/admin/ui/overview?" + v.Encode()
}

// nodeURL builds the drawer's fetch URL for one node, carrying the window so the
// drawer and the diagram always describe the same traffic.
func nodeURL(id, since, anchor, span string) string {
	v := url.Values{}
	v.Set("id", id)
	if since != "" {
		v.Set("since", since)
	}
	if anchor != "" {
		v.Set("anchor", anchor)
	}
	if span != "" {
		v.Set("span", span)
	}
	return "/admin/ui/overview/node?" + v.Encode()
}

// sinceChoices are the trailing-window presets the range control offers.
func sinceChoices() []string { return []string{"1h", "6h", "24h", "72h", "168h", "720h"} }

// defaultSinceChoice is the option the window select opens on, as the *string
// the select offers* rather than overviewDefaultWindow.String().
//
// Those differ: Duration.String() renders 24h as "24h0m0s", which matches no
// option value, so the select fell back to displaying its first entry ("last
// 1h") while the page rendered 24 hours of data. The two must be the same
// literal, and this constant is checked against overviewDefaultWindow by
// TestDefaultSinceChoiceIsOffered.
const defaultSinceChoice = "24h"

// sinceLabelFor names a window duration for the select, and says what the
// duration *does* in each mode — the same "168h" means "the last 7 days" on a
// single window and "7 days either side of the change" in a compare, and a
// control whose meaning shifts silently under a mode is the one thing a reader
// cannot recover from the page.
func sinceLabelFor(raw string, compare bool) string {
	if compare {
		return raw + " each side"
	}
	return "last " + raw
}

// activeSince renders the window duration for a form value, so the select
// reopens on what is actually in play. An empty raw value means the handler
// applied the default, which is the option literal rather than the Duration's
// own String() form (see defaultSinceChoice).
func activeSince(raw string) string {
	if raw == "" {
		return defaultSinceChoice
	}
	return raw
}

// overviewCostFor24h is the nav bar's Overview stat: real spend over the last
// day, replacing the mockup's hardcoded "$18/24h" placeholder.
//
// It lives here rather than in ui.go's base() because it is this page's own
// number, and because a failure to compute it must not cost the whole nav — the
// caller degrades to a dash.
func (h *Handler) overviewCostFor24h(ctx context.Context) (string, bool) {
	if h.reader == nil {
		return "", false
	}
	sum, err := h.reader.SummarizeWindow(ctx, store.Window{Since: time.Now().UTC().Add(-24 * time.Hour)})
	if err != nil {
		h.logger.LogError(ctx, "warn", err,
			map[string]interface{}{"phase": "admin_ui_nav_overview_cost"})
		return "", false
	}
	// Whole dollars: the nav is a glance, and cents there are noise.
	return "$" + strconv.FormatInt(int64(sum.CostUSD), 10), true
}
