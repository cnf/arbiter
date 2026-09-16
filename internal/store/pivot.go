package store

import (
	"context"
	"fmt"
)

// Dimension is a group-by axis for the pivot: which column's values become the
// rows of the table. It is a string type rather than an interface or a set of
// callbacks so a handler can round-trip it through a query parameter safely.
type Dimension string

// Metric is what a pivot row is *measured* by, and also what the rows are
// ordered by: the biggest spenders, the slowest providers, the most error-prone
// epochs.
type Metric string

// The dimensions the pivot can group by. Each is a value an operator would
// plausibly ask "where did my money/compute go" about.
const (
	DimProvider Dimension = "provider"
	DimModel    Dimension = "model"
	DimAlias    Dimension = "alias"
	DimEpoch    Dimension = "epoch"
	DimDomain   Dimension = "domain"
	DimEffort   Dimension = "effort"
	DimStatus   Dimension = "status"
	DimFormat   Dimension = "format"
)

// The metrics a pivot row carries. Every row carries *all* of them, so the table
// can show the whole picture without a second query; the chosen metric only
// decides the ordering. See PivotTotals.
const (
	MetricRequests  Metric = "requests"
	MetricCost      Metric = "cost"
	MetricTokens    Metric = "tokens"
	MetricLatency   Metric = "latency"
	MetricErrorRate Metric = "error_rate"
)

// Dimensions lists the supported dimensions, for a UI to render choices from
// rather than hardcoding a list that can drift.
func Dimensions() []Dimension {
	return []Dimension{DimProvider, DimModel, DimAlias, DimEpoch, DimDomain, DimEffort, DimStatus, DimFormat}
}

// Metrics lists the supported metrics, same reason.
func Metrics() []Metric {
	return []Metric{MetricRequests, MetricCost, MetricTokens, MetricLatency, MetricErrorRate}
}

// dimensionSQL maps a dimension to the expression that produces its group key.
//
// This map is the entire safety story for the pivot: no user-supplied string
// ever reaches the query. A dimension selects one of these fixed expressions by
// map lookup, and the expression is the only thing concatenated into SQL — every
// value stays a ? bind. A miss returns ErrUnknownDimension rather than a
// default, because silently grouping by something else would answer a question
// nobody asked.
//
// The COALESCE(NULLIF(...)) wrappers matter: NULL is common in domain, effort,
// alias_used and config_epoch (any request that predates the column, or a
// literal model name, or a classifier that did not fire), and a bare GROUP BY
// would collapse those into a blank row that reads as a rendering bug. Each
// gets a parenthesised label instead — and since the label *is* the group key,
// it must be the same expression a UI compares against.
var dimensionSQL = map[Dimension]string{
	DimProvider: "provider",
	DimModel:    "provider || '/' || model",
	DimAlias:    "COALESCE(NULLIF(alias_used, ''), '(literal)')",
	DimEpoch:    "COALESCE(NULLIF(config_epoch, ''), '(none)')",
	DimDomain:   "COALESCE(NULLIF(domain, ''), '(unclassified)')",
	DimEffort:   "COALESCE(NULLIF(effort, ''), '(unclassified)')",
	DimStatus:   "CAST(status_code AS TEXT)",
	DimFormat:   "format",
}

// metricSQL maps a metric to its *ordering* expression. Every projection below
// is computed for every row regardless, so the ordering is the only thing a
// metric decides.
//
// The CASTs are load-bearing and not cosmetic: mixing an INTEGER sum with a REAL
// average in one projection makes sqlite's column affinity for the result
// ambiguous, and a scan into a float64 can then fail. Casting every aggregate to
// REAL makes the shapes uniform.
//
// MetricErrorRate guards its division: an empty group has COUNT(*) = 0, and
// sqlite would return NULL for 0/0 where the caller expects a number.
var metricSQL = map[Metric]string{
	MetricRequests:  "CAST(COUNT(*) AS REAL)",
	MetricCost:      "COALESCE(SUM(cost_usd), 0)",
	MetricTokens:    "CAST(COALESCE(SUM(input_tokens) + SUM(output_tokens), 0) AS REAL)",
	MetricLatency:   "CAST(COALESCE(AVG(latency_ms), 0) AS REAL)",
	MetricErrorRate: "COALESCE(CAST(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END) AS REAL) / NULLIF(COUNT(*), 0), 0)",
}

// PivotRow is one group's totals. It carries every metric rather than one,
// because a table showing cost without the request count behind it is how you
// misread a cheap row that is cheap only because it is rare.
type PivotRow struct {
	Key string `json:"key"`

	Requests  int64   `json:"requests"`
	CostUSD   float64 `json:"cost_usd"`
	AvgCost   float64 `json:"avg_cost_usd"`
	Tokens    int64   `json:"tokens"`
	LatencyMs int64   `json:"avg_latency_ms"`
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"error_rate"`

	// Providers is how many distinct provider/model pairs the group covers. For
	// every dimension except provider/model it is 1, and it is what tells a
	// reader whether "unclassified" is one thing or many.
	Providers int64 `json:"models"`
}

// PivotTotals groups a window by one dimension, ordered by one metric, biggest
// first.
//
// The metric chooses the *ordering* only: every row carries all five metrics, so
// the same query serves any metric switch and the caller can re-sort what it
// already has. That is a deliberate trade — with one query the table is a
// complete picture, at the cost of the row *set* being limited by whatever
// metric ordered it. A caller that wants top-N by a different metric must ask
// again; see the handler, which does.
func (r *Reader) PivotTotals(ctx context.Context, w Window, d Dimension, m Metric, limit int) ([]PivotRow, error) {
	dimExpr, ok := dimensionSQL[d]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownDimension, d)
	}
	metricExpr, ok := metricSQL[m]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownMetric, m)
	}
	if limit <= 0 || limit > MaxPivotLimit {
		limit = MaxPivotLimit
	}

	// The dimension expression is grouped and ordered by its *alias*, so the
	// expression itself is written once and never repeated into the ORDER BY —
	// which keeps the only concatenated strings the two map values above.
	q := `
SELECT ` + dimExpr + ` AS k,
    CAST(COUNT(*) AS REAL),
    COALESCE(SUM(cost_usd), 0),
    COALESCE(AVG(cost_usd), 0),
    CAST(COALESCE(SUM(input_tokens) + SUM(output_tokens), 0) AS REAL),
    CAST(COALESCE(AVG(latency_ms), 0) AS REAL),
    CAST(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END) AS REAL),
    COALESCE(CAST(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END) AS REAL) / NULLIF(COUNT(*), 0), 0),
    CAST(COUNT(DISTINCT provider || '/' || model) AS REAL)
FROM requests
WHERE ts >= ?
GROUP BY k
ORDER BY ` + metricExpr + ` DESC, k ASC
LIMIT ?`

	rows, err := r.db.QueryContext(ctx, q, w.Since, limit)
	if err != nil {
		return nil, fmt.Errorf("pivot totals: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []PivotRow{}
	for rows.Next() {
		var (
			p          PivotRow
			requests   float64
			tokens     float64
			latency    float64
			errs, prod float64
		)
		if err := rows.Scan(&p.Key, &requests, &p.CostUSD, &p.AvgCost, &tokens,
			&latency, &errs, &p.ErrorRate, &prod); err != nil {
			return nil, fmt.Errorf("scan pivot row: %w", err)
		}
		p.Requests = int64(requests)
		p.Tokens = int64(tokens)
		p.LatencyMs = int64(latency)
		p.Errors = int64(errs)
		p.Providers = int64(prod)
		out = append(out, p)
	}
	return out, rows.Err()
}

// MaxPivotLimit caps a pivot. It is higher than the request list's because a
// pivot result is one row per *group*, and seeing every group is the point of
// grouping — truncating a pivot silently would hide the tail that answers "what
// else is in here". The handler reports when it hits this.
const MaxPivotLimit = 500

// Errors a caller can tell apart from a query failure. They exist so a handler
// can answer 400 for a bad parameter and 500 for a real failure — the same split
// the JSON surface already makes for a malformed status code, which is a 400
// rather than a silently ignored filter.
var (
	ErrUnknownDimension = fmt.Errorf("unknown dimension")
	ErrUnknownMetric    = fmt.Errorf("unknown metric")
)
