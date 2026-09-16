package store

import (
	"context"
	"fmt"
	"time"
)

// Bucket is the time granularity a series is grouped at: how wide each point on
// a chart's x-axis is.
type Bucket string

// The bucket sizes the series query can produce.
const (
	BucketMin10 Bucket = "10m"
	BucketHour  Bucket = "1h"
	BucketDay   Bucket = "1d"
)

// Buckets lists the supported granularities.
func Buckets() []Bucket { return []Bucket{BucketMin10, BucketHour, BucketDay} }

// bucketSQL maps a bucket to the expression that produces its key.
//
// This is prefix slicing rather than a date function, and that is load-bearing.
// The ts column is TEXT holding Go's time.Time.String() layout
// (`2026-09-16 05:29:33.123456789 +0000 UTC`), which SQLite's own date and time
// functions cannot parse: strftime('%s', ts) returns NULL for every row, silently.
// The first 19 characters are a fixed-width `YYYY-MM-DD HH:MM:SS` regardless of
// the fractional part, so slicing them is exact and needs no parsing at all.
//
// Each expression is also built so the *result* is a timestamp SQLite and Go can
// both read: the concatenations pad a truncated prefix back out to
// `YYYY-MM-DD HH:MM:SS`, which time.Parse handles with one layout.
var bucketSQL = map[Bucket]string{
	// "2026-09-16 05:2" + "0:00" -> "2026-09-16 05:20:00"
	BucketMin10: `substr(ts, 1, 15) || '0:00'`,
	// "2026-09-16 05" + ":00:00" -> "2026-09-16 05:00:00"
	BucketHour: `substr(ts, 1, 13) || ':00:00'`,
	// "2026-09-16" -> a date, parsed with its own layout by the caller.
	BucketDay: `substr(ts, 1, 10)`,
}

// BucketFor picks a granularity from how wide a window is: finer windows get
// finer buckets, because a 10-minute bucket over a month is thousands of points
// and a daily bucket over an hour is one.
//
// The thresholds land the result in the tens-to-low-hundreds of points for the
// windows the UI offers, which is what a line chart can usefully show. `now` is a
// parameter rather than a call to time.Now so selection is testable without
// freezing the clock for the whole process.
func BucketFor(window Window, now time.Time) Bucket {
	if window.Since.IsZero() {
		// A zero Since means "all time" to the other queries, which for bucket
		// selection is unbounded: take the coarsest rather than the finest.
		return BucketDay
	}
	switch span := now.Sub(window.Since); {
	case span <= 6*time.Hour:
		return BucketMin10
	case span <= 4*24*time.Hour:
		return BucketHour
	default:
		return BucketDay
	}
}

// PivotPoint is one bucket of one group's series.
type PivotPoint struct {
	// Bucket is the bucket's start as `YYYY-MM-DD HH:MM:SS` (or `YYYY-MM-DD`
	// for a daily bucket), which the caller converts to a unix second for the
	// chart and to a label for the tooltip.
	Bucket string  `json:"bucket"`
	Key    string  `json:"key"`
	Value  float64 `json:"value"`
}

// PivotSeries returns one metric per time bucket per group, for the top-N groups
// by that metric over the window.
//
// Two things it deliberately does not do:
//
//   - It does not return every group. A time series with one line per distinct
//     model is unreadable past a handful, so the groups are limited to the top N
//     by the chosen metric and the caller folds the rest into an "other" line.
//   - It does not order buckets in Go. The SQL orders by the bucket key, which is
//     a fixed-width sortable string, and the caller turns it into a unix value.
//
// The group expression is exactly the one PivotTotals groups by, deliberately:
// if the two disagreed, the chart's lines and the table's rows would describe
// different groups with the same names.
func (r *Reader) PivotSeries(ctx context.Context, w Window, d Dimension, m Metric, b Bucket, topN int) ([]PivotPoint, error) {
	dimExpr, ok := dimensionSQL[d]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownDimension, d)
	}
	metricExpr, ok := metricSQL[m]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownMetric, m)
	}
	bucketExpr, ok := bucketSQL[b]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownBucket, b)
	}
	if topN <= 0 || topN > MaxSeriesGroups {
		topN = MaxSeriesGroups
	}

	// The top-N groups are chosen over the whole window, then only their buckets
	// are returned — so a group that is small in total but spiky in one bucket
	// does not appear as a lone spike with no context.
	//
	// dimExpr appears three times: twice in the inner query (grouping and the
	// selected key) and once in the outer one. It must be the same expression in
	// all three, and it is a map value, never user text.
	q := `
WITH top AS (
    SELECT ` + dimExpr + ` AS k
    FROM requests
    WHERE ts >= ?
    GROUP BY k
    ORDER BY ` + metricExpr + ` DESC, k ASC
    LIMIT ?
)
SELECT ` + bucketExpr + ` AS b, ` + dimExpr + ` AS k, ` + metricExpr + ` AS v
FROM requests
WHERE ts >= ? AND ` + dimExpr + ` IN (SELECT k FROM top)
GROUP BY b, k
ORDER BY b ASC, k ASC`

	rows, err := r.db.QueryContext(ctx, q, w.Since, topN, w.Since)
	if err != nil {
		return nil, fmt.Errorf("pivot series: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []PivotPoint{}
	for rows.Next() {
		var p PivotPoint
		if err := rows.Scan(&p.Bucket, &p.Key, &p.Value); err != nil {
			return nil, fmt.Errorf("scan pivot point: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MaxSeriesGroups caps how many lines a series query returns. It is small on
// purpose: the caller folds the remainder into "other", and a chart with more
// lines than this is a chart nobody reads.
const MaxSeriesGroups = 12

// ErrUnknownBucket is a caller-visible sentinel, like the dimension and metric
// ones: a handler turns it into a 400 rather than a 500.
var ErrUnknownBucket = fmt.Errorf("unknown bucket")
