package ui

import (
	"net/http"
	"strconv"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// overviewView is the pivot explorer: headline numbers, then one ranked table
// of whichever dimension the operator picked.
type overviewView struct {
	viewBase

	Overall store.OverallStats

	Dimension store.Dimension
	Metric    store.Metric
	DimRaw    string
	MetricRaw string
	SinceRaw  string
	Since     time.Duration

	Rows []store.PivotRow

	// Dimensions and Metrics come from the store so the chooser cannot drift
	// from what the store can actually group by.
	Dimensions []store.Dimension
	Metrics    []store.Metric

	// SingleValued lists the dimensions whose result has only one group. A pivot
	// over an axis nothing has filled yet renders as one row saying
	// "(unclassified)", which is correct and looks broken — so it is said out
	// loud, with the reason, rather than left for the reader to diagnose.
	SingleValued []store.Dimension

	// Capped reports that the row limit was reached, so a truncated ranking is
	// not read as the whole picture.
	Capped bool
	Limit  int
}

// SingleValuedHint explains a one-group result. The axes come from classifiers,
// and a request naming a concrete model is routed before classification runs —
// so with a client that names its model, domain/effort/cost_class stay empty for
// everything. That is a property of the traffic, not a bug in the page.
const singleValuedHint = "one group — nothing in this window differs on that axis. " +
	"Domain, effort and cost class are filled by classifiers, which only run when a request " +
	"is routed by rules rather than by naming a concrete model, so a client that names its " +
	"model leaves them empty."

// OverviewHandler handles GET /admin/ui/overview: headline numbers plus a pivot
// over window × dimension × metric.
//
// A bad dimension or metric is a 400 naming the parameter, never a fallback to a
// default — grouping by something other than what was asked for answers a
// question nobody put.
func (h *Handler) OverviewHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	q := r.URL.Query()

	view := overviewView{
		viewBase:   h.base("Overview"),
		Dimension:  store.DimProvider,
		Metric:     store.MetricCost,
		DimRaw:     q.Get("dim"),
		MetricRaw:  q.Get("metric"),
		SinceRaw:   q.Get("since"),
		Since:      defaultWindow,
		Dimensions: store.Dimensions(),
		Metrics:    store.Metrics(),
		Limit:      store.MaxPivotLimit,
	}
	view.Title = "overview"

	if raw := q.Get("dim"); raw != "" {
		view.Dimension = store.Dimension(raw)
	}
	if raw := q.Get("metric"); raw != "" {
		view.Metric = store.Metric(raw)
	}
	if raw := q.Get("since"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			h.fail(w, r, http.StatusBadRequest,
				`since must be a positive Go duration, e.g. "24h" or "168h"`)
			return
		}
		view.Since = d
	}
	if !validDimension(view.Dimension) {
		h.fail(w, r, http.StatusBadRequest, "dim must be one of "+joinDimensions(store.Dimensions()))
		return
	}
	if !validMetric(view.Metric) {
		h.fail(w, r, http.StatusBadRequest, "metric must be one of "+joinMetrics(store.Metrics()))
		return
	}

	if h.reader != nil {
		w0 := store.Window{Since: time.Now().UTC().Add(-view.Since)}

		overall, err := h.reader.Overall(r.Context(), w0)
		if err != nil {
			h.logger.LogError(r.Context(), "error", err,
				map[string]interface{}{"phase": "admin_ui_overview_overall"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
		view.Overall = overall

		rows, err := h.reader.PivotTotals(r.Context(), w0, view.Dimension, view.Metric, view.Limit)
		if err != nil {
			// A bad dimension/metric was already refused above, so reaching here
			// is a real failure.
			h.logger.LogError(r.Context(), "error", err,
				map[string]interface{}{"phase": "admin_ui_overview_pivot"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
		view.Rows = rows
		view.Capped = len(rows) == view.Limit
		view.SingleValued = h.singleValuedDimensions(r, w0, view.Dimension)
	}

	h.render(w, r, "overview", "pivot-table", view)
}

// singleValuedDimensions reports which dimensions have only one group in this
// window, so the page can explain a one-row result instead of presenting it as
// a finding. It asks the store rather than guessing from the config, because the
// answer depends on the traffic, not the setup.
//
// It is bounded: one cheap aggregate per dimension, over the same window, at
// most len(Dimensions()). An error on any one of them is not fatal — the page is
// still useful without the explanation — so it degrades to reporting nothing.
func (h *Handler) singleValuedDimensions(r *http.Request, w store.Window, current store.Dimension) []store.Dimension {
	var out []store.Dimension
	for _, d := range store.Dimensions() {
		if d == current {
			// The chosen dimension's own row count is already visible as
			// len(Rows); asking again would be a second query for the same
			// answer.
			continue
		}
		rows, err := h.reader.PivotTotals(r.Context(), w, d, store.MetricRequests, 2)
		if err != nil {
			h.logger.LogError(r.Context(), "warn", err,
				map[string]interface{}{"phase": "admin_ui_overview_singleton", "dim": string(d)})
			continue
		}
		if len(rows) == 1 {
			out = append(out, d)
		}
	}
	return out
}

func validDimension(d store.Dimension) bool {
	for _, known := range store.Dimensions() {
		if d == known {
			return true
		}
	}
	return false
}

func validMetric(m store.Metric) bool {
	for _, known := range store.Metrics() {
		if m == known {
			return true
		}
	}
	return false
}

func joinDimensions(ds []store.Dimension) string { return joinStrings(toStringsDim(ds)) }
func joinMetrics(ms []store.Metric) string       { return joinStrings(toStringsMetric(ms)) }

func toStringsDim(ds []store.Dimension) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, string(d))
	}
	return out
}

func toStringsMetric(ms []store.Metric) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, string(m))
	}
	return out
}

func joinStrings(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// pivotLimitNote renders the cap, for the template.
func pivotLimitNote(limit int) string { return strconv.Itoa(limit) }
