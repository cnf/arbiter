package ui

import (
	"sort"

	"github.com/cnf/arbiter/internal/store"
)

// Sankey geometry, computed server-side.
//
// The settled mockup (design/overview-mockups/f3-overview-styled.html) carries
// hardcoded SVG path coordinates that a throwaway Python script produced. This
// file recomputes them in Go so the layout is unit-testable and the page stays
// server-rendered like every other page in the rebuild — the alternative
// (shipping edge JSON and laying out in JS) would put the one piece of real
// arithmetic on this page beyond the reach of `go test`.

// Sankey layout constants, in SVG user units. They match the mockup's own
// geometry so the ported page keeps the proportions that were signed off.
const (
	sankeyWidth   = 1060.0
	sankeyBarW    = 18.0  // node bar thickness
	sankeyLabelW  = 190.0 // click target extending from a bar into its label
	sankeyLeftX   = 190.0 // right edge of the alias column's bars
	sankeyRightX  = 870.0 // left edge of the model column's bars
	sankeyNodeGap = 7.0   // vertical gap between stacked nodes

	// sankeyMinHeight keeps a diagram readable when a window holds only a
	// couple of routes: without it, two nodes render as two hairlines at the
	// top of a tall empty box.
	sankeyMinHeight = 180.0

	// sankeyMaxHeight bounds a busy window. Past this the ribbons are too thin
	// to trace by eye anyway, and the remainder bucket is what carries the
	// tail (see store.MaxRoutingEdges).
	sankeyMaxHeight = 420.0

	// sankeyMinBand is the smallest height a node or ribbon may render at. A
	// route with 3 requests next to one with 5,000 would otherwise compute to
	// a sub-pixel band and vanish — and a route that silently disappears from
	// a diagram whose job is showing where traffic goes is worse than a
	// slightly-inaccurate band. Bands are proportional above this floor.
	sankeyMinBand = 2.0

	// labelMinBand / subMinBand are the band heights at which a node's name and
	// its detail line become drawable.
	//
	// They are the type's own line heights (12px name, 9.5px detail in app.css),
	// with the pair needing room for both. Below the first threshold a node
	// renders as a bar only — its identity is in the hover title and the drawer
	// rather than in text overlapping its neighbour. Live data is what forced
	// this: 18 model nodes in 420px put labels 9px apart.
	labelMinBand = 13.0
	subMinBand   = 26.0
)

// sankeyNode is one endpoint column entry — an alias on the left or a
// provider/model on the right — positioned and sized by request share.
type sankeyNode struct {
	// ID is the stable handle the drawer is fetched by. It is opaque to the
	// template and is what a click sends back to the server.
	ID string

	Label string

	// Side is "alias" or "model", which decides which x the bar sits at.
	Side string

	X      float64
	Y      float64
	Width  float64
	Height float64

	// HitX/HitWidth are the click target, which extends past the bar to cover
	// the label text — a 18px-wide bar is a poor click target, and the mockup
	// makes the whole label row clickable.
	HitX     float64
	HitWidth float64

	// LabelX/TextY/SubY are where the two text lines go. They are computed here
	// rather than in the template because a template doing arithmetic is a
	// template that cannot be tested, and these depend on which side the node
	// is on (model labels run rightwards from the bar, alias labels end at it).
	LabelX float64
	TextY  float64
	SubY   float64

	// SubLine is the pre-rendered secondary line (requests, cost, cache chip),
	// assembled in Go so the strip, the drawer and this label all format money
	// and percentages the same way.
	SubLine string

	// ShowLabel and ShowSub gate the two text lines independently.
	//
	// A band proportional to a tiny route can be a few pixels tall, and two
	// lines of 12px type do not fit in it — drawn anyway they collide with the
	// neighbouring node's, which is how a diagram of 20 routes becomes an
	// illegible pile (observed on live data before the cap was lowered). So a
	// node too short for its label draws none, and a node with room for one line
	// draws only the name. The detail is never lost: it is in the hover title
	// and the drawer, both of which are reachable from the bar itself.
	ShowLabel bool
	ShowSub   bool

	// Color is the provider's ribbon colour for a model node, and the neutral
	// tone for an alias node (an alias has no single provider).
	Color string

	// Requests/CostUSD/CacheHit back the node's own labels. CacheHit is -1 when
	// the node moved no cacheable tokens, so the template can omit the chip
	// rather than print a misleading 0%.
	Requests int64
	CostUSD  float64
	CacheHit float64

	// Errored marks a node whose every request failed — misconfiguration debris
	// (an alias whose target is the alias name itself) that exists in the real
	// data and should be visible, not smoothed away.
	Errored bool
}

// sankeyRibbon is one route drawn between an alias node and a model node.
type sankeyRibbon struct {
	// Path is the SVG path's `d` attribute: a closed curve from the alias bar's
	// slice to the model bar's slice.
	Path string

	// Fill is a gradient id reference for a provider-coloured ribbon, or a flat
	// colour for the remainder bucket.
	Fill    string
	Opacity float64

	// From/To are the node IDs this ribbon connects, so hovering or selecting a
	// node can highlight its ribbons without a second lookup.
	From string
	To   string

	Title string // the SVG <title>, i.e. the hover tooltip
}

// sankey is the whole diagram: two node columns, the ribbons between them, and
// the viewBox the template renders at.
type sankey struct {
	Nodes   []sankeyNode
	Ribbons []sankeyRibbon

	Width  float64
	Height float64

	// Empty reports that there was nothing to draw, so the page can say "no
	// traffic in this window" instead of rendering an empty box that looks
	// broken.
	Empty bool
}

// providerColor maps a provider to its ribbon colour.
//
// The palette is the mockup's: claude purple, everything else the accent blue,
// with a neutral grey for the remainder bucket and for an alias node (which has
// no single provider). Colours are literals rather than CSS variables because
// they are also used to build SVG gradient stops, which cannot resolve a
// var() at the point the gradient is defined.
func providerColor(provider string) string {
	switch provider {
	case "claude", "anthropic":
		return "#a996ff"
	case "":
		return "#5a6474"
	default:
		return "#46b4ff"
	}
}

// gradientID is the per-provider ribbon gradient's element id. Ribbons fade from
// the neutral alias side to the provider's colour on the model side, so the eye
// follows traffic left to right.
func gradientID(provider string) string {
	switch provider {
	case "claude", "anthropic":
		return "gF-claude"
	case "":
		return "gF-none"
	default:
		return "gF-other"
	}
}

// aliasLabel renders an alias for display, naming the literal-model case
// explicitly.
//
// A request that named a concrete model has no alias, and the store reports
// that as "". Rendering it as a blank row would read as a data gap; it is
// actually a distinct and common routing path (a client bypassing the router),
// so it gets a name.
func aliasLabel(alias string) string {
	if alias == "" {
		return "(literal model)"
	}
	return alias
}

// buildSankey lays out a routing-flow diagram from a window's edges.
//
// Layout: each side is a column of nodes stacked in descending request order,
// with heights proportional to request share. A ribbon leaves its alias node at
// a vertical offset proportional to that route's share *of the alias*, and
// arrives at its model node likewise — so a node's bar height always equals the
// sum of its ribbons' widths at that end, which is what makes a sankey readable
// rather than merely decorative.
//
// The remainder edge (store.RoutingEdge.IsRemainder) becomes a single grey node
// on each side. It is not skipped: dropping it would make the columns stop
// summing to the window's totals, and the KPI strip above would then disagree
// with the diagram for no visible reason.
func buildSankey(edges []store.RoutingEdge) sankey {
	if len(edges) == 0 {
		return sankey{Width: sankeyWidth, Height: sankeyMinHeight, Empty: true}
	}

	// Total requests decide the vertical scale. Using requests (not cost) is
	// the mockup's choice and the right one: cost is dominated by one provider
	// and would collapse every other route to a hairline.
	var total int64
	for _, e := range edges {
		total += e.Requests
	}
	if total == 0 {
		return sankey{Width: sankeyWidth, Height: sankeyMinHeight, Empty: true}
	}

	height := sankeyMaxHeight
	if n := float64(len(edges)); n*20 < sankeyMaxHeight {
		height = sankeyMinHeight
		if want := n * 34; want > height {
			height = want
		}
	}

	// Group the edges by each side's key, preserving the busiest-first order
	// the store already applied.
	type group struct {
		key      string
		label    string
		provider string
		edges    []int // indices into edges
		requests int64
		cost     float64
		cacheNum int64
		cacheDen int64
		errors   int64
		remain   bool
	}
	var aliases, models []*group
	aliasBy := map[string]*group{}
	modelBy := map[string]*group{}

	for i, e := range edges {
		aKey, mKey := "alias:"+e.Alias, "model:"+e.Provider+"/"+e.Model
		aLabel, mLabel := aliasLabel(e.Alias), e.Model
		if e.IsRemainder() {
			aKey, mKey = "alias:__remainder__", "model:__remainder__"
			aLabel, mLabel = "other", "other"
		}

		ag, ok := aliasBy[aKey]
		if !ok {
			ag = &group{key: aKey, label: aLabel, remain: e.IsRemainder()}
			aliasBy[aKey] = ag
			aliases = append(aliases, ag)
		}
		mg, ok := modelBy[mKey]
		if !ok {
			mg = &group{key: mKey, label: mLabel, provider: e.Provider, remain: e.IsRemainder()}
			modelBy[mKey] = mg
			models = append(models, mg)
		}
		for _, g := range []*group{ag, mg} {
			g.edges = append(g.edges, i)
			g.requests += e.Requests
			g.cost += e.CostUSD
			g.cacheNum += e.CacheReadTokens
			g.cacheDen += e.CacheReadTokens + e.InputTokens
			g.errors += e.Errors
		}
	}

	// Sort each column by request count, busiest at the top, with the
	// remainder bucket pinned last regardless of size — it is a bucket, not a
	// route, and floating it into the middle of the ranking would read as one.
	sortColumn := func(gs []*group) {
		sort.SliceStable(gs, func(i, j int) bool {
			if gs[i].remain != gs[j].remain {
				return !gs[i].remain
			}
			return gs[i].requests > gs[j].requests
		})
	}
	sortColumn(aliases)
	sortColumn(models)

	// Vertical scale: the available height minus the inter-node gaps, divided
	// by total requests. Computed per column because each column has its own
	// node count and therefore its own total gap.
	scaleFor := func(gs []*group) float64 {
		avail := height - float64(len(gs)-1)*sankeyNodeGap
		if avail < sankeyMinBand*float64(len(gs)) {
			avail = sankeyMinBand * float64(len(gs))
		}
		return avail / float64(total)
	}

	out := sankey{Width: sankeyWidth, Height: height}

	// Place the nodes, and remember each group's vertical cursor so ribbons can
	// stack against it.
	type placed struct {
		y, h   float64
		cursor float64
	}
	place := map[string]*placed{}

	layColumn := func(gs []*group, side string, x float64) {
		scale := scaleFor(gs)
		y := 0.0
		for _, g := range gs {
			h := float64(g.requests) * scale
			if h < sankeyMinBand {
				h = sankeyMinBand
			}
			color := providerColor(g.provider)
			if side == "alias" {
				color = providerColor("")
			}
			if g.remain {
				color = providerColor("")
			}
			cache := -1.0
			if g.cacheDen > 0 {
				cache = float64(g.cacheNum) / float64(g.cacheDen)
			}

			// Label geometry: model labels run rightwards from their bar, alias
			// labels end just before theirs (text-anchor="end" in the template).
			labelX := x + sankeyBarW + 6
			hitX := x
			if side == "alias" {
				labelX = x - 6
				hitX = x - sankeyLabelW
				if hitX < 0 {
					hitX = 0
				}
			}

			// Both text lines sit inside the bar's own band. A band shorter than
			// the type cannot hold them, and drawing them anyway collides with
			// the neighbour below — so each line is gated on the room actually
			// available (see ShowLabel/ShowSub). The thresholds are the line
			// heights the CSS uses, not guesses.
			textY := y + 12
			showLabel := h >= labelMinBand
			showSub := h >= subMinBand
			if !showSub && showLabel {
				// One line only: centre it on the band rather than leaving it
				// hugging the top edge of a short bar.
				textY = y + h/2 + 4
			}

			out.Nodes = append(out.Nodes, sankeyNode{
				ID:        g.key,
				Label:     g.label,
				Side:      side,
				X:         x,
				Y:         y,
				Width:     sankeyBarW,
				Height:    h,
				HitX:      hitX,
				HitWidth:  sankeyLabelW,
				LabelX:    labelX,
				TextY:     textY,
				SubY:      textY + 12,
				SubLine:   nodeSubLine(g.requests, g.cost, cache, g.requests > 0 && g.errors == g.requests),
				ShowLabel: showLabel,
				ShowSub:   showSub,
				Color:     color,
				Requests:  g.requests,
				CostUSD:   g.cost,
				CacheHit:  cache,
				Errored:   g.requests > 0 && g.errors == g.requests,
			})
			place[g.key] = &placed{y: y, h: h, cursor: y}
			y += h + sankeyNodeGap
		}
	}
	layColumn(aliases, "alias", sankeyLeftX)
	layColumn(models, "model", sankeyRightX)

	// Ribbons, drawn in the store's busiest-first order so the largest bands
	// are painted first and the small ones stay visible on top of them.
	for _, ag := range aliases {
		for _, ei := range ag.edges {
			e := edges[ei]
			mKey := "model:" + e.Provider + "/" + e.Model
			if e.IsRemainder() {
				mKey = "model:__remainder__"
			}
			ap, mp := place[ag.key], place[mKey]
			if ap == nil || mp == nil {
				continue
			}

			// A route's band at each end is its share of *that end's* total,
			// so both bars stay fully covered by their own ribbons.
			ah := bandHeight(e.Requests, ag.requests, ap.h)
			mg := modelBy[mKey]
			mh := bandHeight(e.Requests, mg.requests, mp.h)

			y0, y1 := ap.cursor, mp.cursor
			ap.cursor += ah
			mp.cursor += mh

			fill := "url(#" + gradientID(e.Provider) + ")"
			opacity := 0.4
			if e.IsRemainder() {
				fill = providerColor("")
				opacity = 0.24
			} else if e.Provider != "claude" && e.Provider != "anthropic" {
				opacity = 0.3
			}

			out.Ribbons = append(out.Ribbons, sankeyRibbon{
				Path:    ribbonPath(sankeyLeftX, y0, ah, sankeyRightX, y1, mh),
				Fill:    fill,
				Opacity: opacity,
				From:    ag.key,
				To:      mKey,
				Title:   ribbonTitle(e),
			})
		}
	}
	return out
}

// nodeSubLine is a node's secondary label: request count, spend, and the cache
// chip the ticket calls out as this page's primary signal.
//
// Assembled in Go rather than in the template so money and percentages are
// formatted by the same helpers the KPI strip and the drawer use. An all-errored
// node says so instead of quoting $0 spend, which would read as free rather than
// as broken.
func nodeSubLine(requests int64, cost, cacheHit float64, allErrored bool) string {
	if allErrored {
		return fmtTokens(requests) + " req · every request failed"
	}
	s := fmtTokens(requests) + " req"
	if cost > 0 {
		s += " · " + fmtUSD(cost)
	}
	if cacheHit >= 0 {
		s += " · " + fmtPct(cacheHit) + " cache"
	}
	return s
}

// bandHeight is one route's slice of a node's bar: proportional to its share of
// that node's requests, floored so a tiny route on a huge node still draws.
func bandHeight(part, whole int64, barHeight float64) float64 {
	if whole == 0 {
		return sankeyMinBand
	}
	h := float64(part) / float64(whole) * barHeight
	if h < sankeyMinBand {
		return sankeyMinBand
	}
	return h
}
