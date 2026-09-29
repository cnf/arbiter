package ui

import (
	"fmt"
	"net/http"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

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
