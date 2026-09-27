package ui

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// The Overview page (#54): where requests go, and whether a config change made
// that better or worse.
//
// The page is split across files by what part of it you are looking at:
//
//	overview.go        the Handler entry point, the window/mode constants, and
//	                   the view types the template renders
//	overviewwindow.go  resolving the anchor/window and the since-choices
//	overviewkpi.go     the KPI strip and the before/after delta formatting
//	overviewnode.go    the node drawer — its handler and buildNodeDetail
//	overviewlabel.go   the display-only labels (window, cache, severity, since)
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
