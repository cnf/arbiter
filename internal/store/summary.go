package store

import (
	"context"
	"fmt"
)

// WindowSummary is everything the Overview page's KPI strip shows for one
// window, in one query.
//
// It embeds Measures rather than redefining the counters, so "cache hit" and
// "cost per 1M tokens" mean exactly what they mean on a single route — the
// strip and the drawer cannot drift apart on a page that exists to compare
// them.
type WindowSummary struct {
	Measures

	// Sessions is how many distinct conversations the window covers. It is
	// here rather than derived from the flow edges because a session spans
	// routes: summing per-route session counts would multiply-count any
	// conversation that switched model mid-flight, which is the normal case.
	Sessions int64 `json:"sessions"`
}

// SummarizeWindow answers the KPI strip in one round trip.
//
// One query for the whole strip rather than one per number, for two reasons:
// five aggregates over the same rows is four unnecessary scans, and — more
// importantly — five separately-timed queries against a live store can observe
// five slightly different sets of rows, so the strip's own numbers would not
// add up. The distinct-session count rides along as a subquery for the same
// reason.
//
// Only client traffic is counted (kind = 'client'), matching RoutingFlow and
// every other aggregate here: classifier and title-generation calls are
// Arbiter's own machinery, not traffic an operator is evaluating.
func (r *Reader) SummarizeWindow(ctx context.Context, w Window) (WindowSummary, error) {
	where, args := w.tsClause("ts")
	q := `
SELECT
    COUNT(*),
    COALESCE(SUM(cost_usd), 0),
    CAST(COALESCE(SUM(input_tokens), 0)       AS INTEGER),
    CAST(COALESCE(SUM(output_tokens), 0)      AS INTEGER),
    CAST(COALESCE(SUM(cache_read_tokens), 0)  AS INTEGER),
    CAST(COALESCE(SUM(cache_write_tokens), 0) AS INTEGER),
    CAST(COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END), 0) AS INTEGER),
    CAST(COALESCE(AVG(latency_ms), 0)         AS INTEGER),
    CAST(COALESCE(COUNT(DISTINCT CASE WHEN session_key IS NOT NULL AND session_key <> ''
                                      THEN session_key END), 0) AS INTEGER)
FROM requests
WHERE ` + where + ` AND kind = 'client'`

	var s WindowSummary
	err := r.db.QueryRowContext(ctx, q, args...).Scan(
		&s.Requests, &s.CostUSD, &s.InputTokens, &s.OutputTokens,
		&s.CacheReadTokens, &s.CacheWriteTokens, &s.Errors, &s.AvgLatencyMs,
		&s.Sessions)
	if err != nil {
		return WindowSummary{}, fmt.Errorf("summarize window: %w", err)
	}
	s.Tokens = s.InputTokens + s.OutputTokens + s.CacheReadTokens + s.CacheWriteTokens
	return s, nil
}

// Delta is one metric's movement between two windows, carrying both readings so
// a caller never has to re-derive either.
//
// Absolute and relative are both kept because the page offers both and they
// answer different questions: "$40 more" versus "80% more". Relative is the one
// that needs care, hence Meaningful below.
type Delta struct {
	Before float64 `json:"before"`
	After  float64 `json:"after"`

	// Abs is After-Before, in the metric's own unit.
	Abs float64 `json:"abs"`

	// Rel is the fractional change (0.8 = +80%), valid only when Meaningful.
	Rel float64 `json:"rel"`

	// Meaningful reports whether Rel means anything. A percentage change from
	// zero is undefined, not infinite, and rendering "+∞%" or "+100%" for
	// "there was nothing before" is how a comparison page starts lying: the
	// first request on a brand-new route would read as an infinite cost
	// regression. When this is false the page shows the absolute pair and no
	// percentage.
	Meaningful bool `json:"meaningful"`
}

// NewDelta builds a metric's movement between two readings.
func NewDelta(before, after float64) Delta {
	d := Delta{Before: before, After: after, Abs: after - before}
	if before != 0 {
		d.Rel = (after - before) / before
		d.Meaningful = true
	}
	return d
}

// Direction says which way a metric is supposed to move, so a caller can
// colour a delta without re-deriving the semantics at each call site.
//
// It is a tri-state rather than a bool because three cases genuinely exist: a
// cost or error-rate rise is bad, a cache-hit rise is good, and a request-count
// rise is neither — traffic going up is just traffic going up. A bool forces
// that third case to claim a verdict, which is how a page ends up colouring
// "more requests" red.
type Direction int

const (
	// Neutral metrics have no better or worse. The page shows the movement
	// without a verdict colour.
	Neutral Direction = iota
	// LessIsBetter: cost, cost-per-1M, error rate, latency.
	LessIsBetter
	// MoreIsBetter: cache hit rate.
	MoreIsBetter
)

// Verdict is how a delta should read for a metric moving in a given direction:
// +1 better, -1 worse, 0 no verdict (no movement, or a neutral metric).
//
// The page colours from this rather than from the sign of the movement, which is
// what keeps "cache hit went up" green and "cost went up" red without either
// call site restating the rule.
func (d Delta) Verdict(dir Direction) int {
	if d.Abs == 0 || dir == Neutral {
		return 0
	}
	better := d.Abs < 0
	if dir == MoreIsBetter {
		better = d.Abs > 0
	}
	if better {
		return 1
	}
	return -1
}

// Improved reports whether this movement is the good direction for a metric.
func (d Delta) Improved(dir Direction) bool { return d.Verdict(dir) > 0 }

// Worsened reports whether this movement is the bad direction for a metric.
// Neutral metrics and flat deltas are neither.
func (d Delta) Worsened(dir Direction) bool { return d.Verdict(dir) < 0 }

// WindowComparison is two windows and the movement between them — the page's
// compare mode.
//
// Before is the earlier window, After the later one. The deltas are computed
// here rather than in the template so the formulas stay in one place, and
// because "is this better" is a question about the metric, not about the
// number (see Delta.Verdict).
type WindowComparison struct {
	Before WindowSummary `json:"before"`
	After  WindowSummary `json:"after"`

	Requests     Delta `json:"requests"`
	CostUSD      Delta `json:"cost_usd"`
	CostPer1M    Delta `json:"cost_per_1m"`
	CacheHitRate Delta `json:"cache_hit_rate"`
	ErrorRate    Delta `json:"error_rate"`
	Tokens       Delta `json:"tokens"`
	AvgLatencyMs Delta `json:"avg_latency_ms"`
}

// DirectionOf returns which way a named metric is supposed to move.
//
// It lives beside the comparison rather than in the presentation layer so the
// answer is the same everywhere: the KPI strip, a drawer and any future JSON
// consumer all colour "cache hit" the same way, and adding a metric means
// declaring its direction once, here, next to the field.
//
// Requests and tokens are Neutral on purpose — volume going up is neither good
// nor bad, and the page must not imply otherwise.
func DirectionOf(metric string) Direction {
	switch metric {
	case "cost_usd", "cost_per_1m", "error_rate", "avg_latency_ms":
		return LessIsBetter
	case "cache_hit_rate":
		return MoreIsBetter
	default:
		// requests, tokens, and anything unrecognised: no verdict rather than
		// a guessed one.
		return Neutral
	}
}

// CompareWindows summarises two windows and computes the movement between them.
//
// The two windows are queried independently and are expected to be adjacent and
// equal-length (the page builds them that way around a config-change anchor),
// but nothing here enforces that: an operator comparing a busy week to a quiet
// one is asking a legitimate question, and the rate metrics are exactly what
// make windows of different lengths comparable at all.
//
// Cost-per-1M is the metric this exists for. Raw cost moves with volume, so a
// config change that made every call cheaper while traffic doubled shows up as
// a cost *increase* — only the per-token rate answers "did this change help?".
func (r *Reader) CompareWindows(ctx context.Context, before, after Window) (WindowComparison, error) {
	b, err := r.SummarizeWindow(ctx, before)
	if err != nil {
		return WindowComparison{}, fmt.Errorf("compare windows (before): %w", err)
	}
	a, err := r.SummarizeWindow(ctx, after)
	if err != nil {
		return WindowComparison{}, fmt.Errorf("compare windows (after): %w", err)
	}

	return WindowComparison{
		Before:       b,
		After:        a,
		Requests:     NewDelta(float64(b.Requests), float64(a.Requests)),
		CostUSD:      NewDelta(b.CostUSD, a.CostUSD),
		CostPer1M:    NewDelta(b.CostPer1MTokens(), a.CostPer1MTokens()),
		CacheHitRate: NewDelta(b.CacheHitRate(), a.CacheHitRate()),
		ErrorRate:    NewDelta(b.ErrorRate(), a.ErrorRate()),
		Tokens:       NewDelta(float64(b.Tokens), float64(a.Tokens)),
		AvgLatencyMs: NewDelta(float64(b.AvgLatencyMs), float64(a.AvgLatencyMs)),
	}, nil
}
