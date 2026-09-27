package ui

import (
	"fmt"

	"github.com/cnf/arbiter/internal/store"
)

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
