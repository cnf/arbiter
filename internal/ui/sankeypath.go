package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/cnf/arbiter/internal/store"
)

// ribbonPath builds the closed SVG path for one route's band.
//
// The shape is the mockup's: a cubic bezier from the alias bar's right edge to
// the model bar's left edge along the band's top, straight down the model end,
// a mirrored bezier back along the bottom, and closed. Control points sit at the
// horizontal midpoint on both curves, which is what gives the S-curve its
// symmetry — the mockup's own generated paths use exactly this construction.
//
// Coordinates are rounded to one decimal: SVG does not need more, and a stable
// short form keeps the rendered HTML diffable when verifying a layout change.
func ribbonPath(x0, y0, h0, x1, y1, h1 float64) string {
	mid := (x0 + x1) / 2
	var b strings.Builder
	b.WriteString("M")
	writePoint(&b, x0, y0)
	b.WriteString(" C")
	writePoint(&b, mid, y0)
	b.WriteString(" ")
	writePoint(&b, mid, y1)
	b.WriteString(" ")
	writePoint(&b, x1, y1)
	b.WriteString(" L")
	writePoint(&b, x1, y1+h1)
	b.WriteString(" C")
	writePoint(&b, mid, y1+h1)
	b.WriteString(" ")
	writePoint(&b, mid, y0+h0)
	b.WriteString(" ")
	writePoint(&b, x0, y0+h0)
	b.WriteString(" Z")
	return b.String()
}

func writePoint(b *strings.Builder, x, y float64) {
	b.WriteString(trimFloat(x))
	b.WriteString(",")
	b.WriteString(trimFloat(y))
}

// trimFloat renders a coordinate with at most one decimal and no trailing
// zero, so paths stay short and comparable.
func trimFloat(v float64) string {
	s := strconv.FormatFloat(v, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}

// ribbonTitle is the route's hover text: where it went, how much traffic, and
// the two numbers this page exists to surface (per-token cost and cache hit).
//
// An all-errored route says so instead of quoting a $0.0000/1M rate that only
// looks like a bargain — that shape is misconfiguration debris (an alias whose
// target is the alias name), and it is present in the real data.
func ribbonTitle(e store.RoutingEdge) string {
	if e.IsRemainder() {
		return fmt.Sprintf("%d other routes · %s · %s requests",
			e.FoldedRoutes, fmtUSD(e.CostUSD), fmtTokens(e.Requests))
	}
	head := fmt.Sprintf("%s → %s/%s · %s requests",
		aliasLabel(e.Alias), e.Provider, e.Model, fmtTokens(e.Requests))
	if e.Requests > 0 && e.Errors == e.Requests {
		return head + " · every request failed"
	}
	tail := fmt.Sprintf(" · %s · %s/1M", fmtUSD(e.CostUSD), fmtUSD(e.CostPer1MTokens()))
	if e.Cacheable() {
		tail += fmt.Sprintf(" · %s cache", fmtPct(e.CacheHitRate()))
	}
	if e.Errors > 0 {
		tail += fmt.Sprintf(" · %s errors", fmtPct(e.ErrorRate()))
	}
	return head + tail
}
