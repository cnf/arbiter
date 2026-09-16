package ui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// seriesGroup is one drawn line: a label a human reads, a colour, and the value
// per x index (null where the group had no traffic in that bucket).
type seriesGroup struct {
	Label  string     `json:"label"`
	Color  string     `json:"color"`
	Values []*float64 `json:"values"`
}

// seriesResponse is what chart.js fetches. It is JSON rather than an inline
// <script> so no store-derived string is ever escaped into a JS context — see
// chart.js's own comment.
type seriesResponse struct {
	X      []int64       `json:"x"`
	Series []seriesGroup `json:"series"`
	YLabel string        `json:"y_label"`

	// Bucket is which granularity was used, so the page can say it and a reader
	// can tell a flat line from a coarse one.
	Bucket string `json:"bucket"`
}

// The palette for the drawn lines. It is a fixed cycle rather than generated
// colours, so the same group keeps the same colour between two views of the same
// window — and it is ordered for distinguishability at a glance, which a hash to
// HSL is not.
var seriesPalette = []string{
	"#4f8cff", "#e0763a", "#3fb950", "#d2a8ff",
	"#f778ba", "#39c5cf", "#d29922", "#ff7b72",
	"#7ee787", "#a5a5ff", "#f0883e", "#8b949e",
}

// SeriesHandler serves GET /admin/ui/overview/series.json: the chart's data.
//
// It is UI-internal and explicitly unstable — the shape can change with the
// chart, which is why it is not under /admin/stats/* with the documented read
// surface. It is registered under the same gate as everything else.
func (h *Handler) SeriesHandler(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeJSONError(w, http.StatusServiceUnavailable,
			"the event store is disabled: set storage.path to record and chart requests")
		return
	}
	q := r.URL.Query()

	dim := store.Dimension(q.Get("dim"))
	metric := store.Metric(q.Get("metric"))
	if dim == "" {
		dim = store.DimProvider
	}
	if metric == "" {
		metric = store.MetricCost
	}
	since := defaultWindow
	if raw := q.Get("since"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			writeJSONError(w, http.StatusBadRequest,
				`since must be a positive Go duration, e.g. "24h" or "168h"`)
			return
		}
		since = d
	}
	if !validDimension(dim) {
		writeJSONError(w, http.StatusBadRequest, "dim must be one of "+joinDimensions(store.Dimensions()))
		return
	}
	if !validMetric(metric) {
		writeJSONError(w, http.StatusBadRequest, "metric must be one of "+joinMetrics(store.Metrics()))
		return
	}

	window := store.Window{Since: time.Now().UTC().Add(-since)}
	bucket := store.BucketFor(window, time.Now().UTC())

	points, err := h.reader.PivotSeries(r.Context(), window, dim, metric, bucket, store.MaxSeriesGroups)
	if err != nil {
		h.logger.LogError(r.Context(), "error", err,
			map[string]interface{}{"phase": "admin_ui_overview_series"})
		writeJSONError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}

	h.writeJSON(w, r, buildSeries(points, metric, bucket, store.MaxSeriesGroups))
}

// buildSeries turns the store's flat (bucket, key, value) rows into the column
// arrays a chart wants.
//
// Two shape decisions worth stating. The x values are the *stored* bucket strings
// converted to a unix second here rather than in the browser, because parsing
// `YYYY-MM-DD HH:MM:SS` is exactly the kind of date handling the `ts` column
// already punishes; and gaps are nulls, not zeros — see seriesGroup.
func buildSeries(points []store.PivotPoint, metric store.Metric, bucket store.Bucket, topN int) seriesResponse {
	// Collect the distinct buckets (already ascending from the query) and the
	// distinct keys, preserving the query's group order.
	var (
		buckets []string
		keys    []string
		seenKey = map[string]bool{}
		index   = map[string]int{}
	)
	for _, p := range points {
		if _, ok := index[p.Bucket]; !ok {
			index[p.Bucket] = len(buckets)
			buckets = append(buckets, p.Bucket)
		}
		if !seenKey[p.Key] {
			seenKey[p.Key] = true
			keys = append(keys, p.Key)
		}
	}

	resp := seriesResponse{
		X:      make([]int64, 0, len(buckets)),
		Bucket: string(bucket),
		YLabel: metricLabel(metric),
	}
	for _, b := range buckets {
		resp.X = append(resp.X, bucketUnix(b, bucket))
	}

	// Fold everything past the cap into one "other" line rather than dropping it,
	// so the chart's total still matches the table's headline.
	shown := keys
	var folded []string
	if len(keys) > topN {
		shown = keys[:topN]
		folded = keys[topN:]
	}
	shownSet := map[string]bool{}
	for _, k := range shown {
		shownSet[k] = true
	}

	values := map[string][]*float64{}
	for _, k := range shown {
		values[k] = make([]*float64, len(buckets))
	}
	other := make([]*float64, len(buckets))
	for _, p := range points {
		i, ok := index[p.Bucket]
		if !ok {
			continue
		}
		if shownSet[p.Key] {
			v := p.Value
			values[p.Key][i] = &v
			continue
		}
		if other[i] == nil {
			zero := 0.0
			other[i] = &zero
		}
		*other[i] += p.Value
	}

	for i, k := range shown {
		resp.Series = append(resp.Series, seriesGroup{
			Label:  k,
			Color:  seriesPalette[i%len(seriesPalette)],
			Values: values[k],
		})
	}
	if len(folded) > 0 {
		resp.Series = append(resp.Series, seriesGroup{
			Label:  "other (" + plural(len(folded), "group") + ")",
			Color:  seriesPalette[len(seriesPalette)-1],
			Values: other,
		})
	}
	return resp
}

// bucketUnix converts a bucket key into a unix second.
//
// The layout differs by granularity: a daily bucket is a bare date with no time
// part, and parsing it with a datetime layout fails. Both layouts are exact, so
// the parse cannot silently succeed on the wrong shape.
func bucketUnix(bucket string, b store.Bucket) int64 {
	layout := "2006-01-02 15:04:05"
	if b == store.BucketDay {
		layout = "2006-01-02"
	}
	t, err := time.Parse(layout, bucket)
	if err != nil {
		// A bucket key the query produced and this cannot parse is a bug, not a
		// data problem: the keys are generated by the SQL above. Returning 0
		// would silently pile every point at the epoch, so it is left as a
		// sentinel far outside any real range and the chart shows a gap.
		return -1
	}
	return t.UTC().Unix()
}

// metricLabel names a metric for the y-axis.
func metricLabel(m store.Metric) string {
	switch m {
	case store.MetricRequests:
		return "requests"
	case store.MetricCost:
		return "cost (USD)"
	case store.MetricTokens:
		return "tokens"
	case store.MetricLatency:
		return "avg latency (ms)"
	case store.MetricErrorRate:
		return "error rate"
	default:
		return string(m)
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// writeJSON writes a JSON body. This package has no JSON writer of its own: the
// chart endpoint is the only one, and it is fetched by script, so its successes
// and failures are both JSON while every page's are HTML.
func (h *Handler) writeJSON(w http.ResponseWriter, r *http.Request, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.logger.LogError(r.Context(), "warn", err,
			map[string]interface{}{"phase": "admin_ui_series_encode"})
	}
}

// writeJSONError is the failure half of the same contract.
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// seriesURL builds the chart's data URL, carrying the current selection so the
// chart and the table always describe the same window and group-by. It is a
// template func rather than a precomputed field because both the overview page
// and its htmx fragment render the chart node.
func seriesURL(d store.Dimension, m store.Metric, since string) string {
	v := url.Values{}
	v.Set("dim", string(d))
	v.Set("metric", string(m))
	if since != "" {
		v.Set("since", since)
	}
	return "/admin/ui/overview/series.json?" + v.Encode()
}
