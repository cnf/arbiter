package router

import (
	"github.com/cnf/arbiter/pkg/types"
)

// CostLatencyLookup answers "what do we know about this provider/model?".
// The bool is false when there is no catalog entry — callers treat that as
// unknown cost (rank last) rather than an error, so a missing row never
// breaks routing.
type CostLatencyLookup interface {
	Lookup(provider, model string) (types.ModelCost, bool)
}

// StaticCatalog is a CostLatencyLookup backed by configured entries. It is
// the only implementation built in Phase 2; an empirical source (observed
// cost/latency from the event store) can implement the same interface later
// with no router changes.
type StaticCatalog map[string]types.ModelCost

// catalogKey namespaces a provider/model pair. Provider and model can both
// contain slashes (e.g. an OpenRouter model id), so a plain string concat
// with a separator that cannot appear in either is required.
func catalogKey(provider, model string) string {
	return provider + "\x00" + model
}

// NewStaticCatalog indexes ModelCost entries by provider+model.
func NewStaticCatalog(entries []types.ModelCost) StaticCatalog {
	c := make(StaticCatalog, len(entries))
	for _, e := range entries {
		c[catalogKey(e.Provider, e.Model)] = e
	}
	return c
}

func (c StaticCatalog) Lookup(provider, model string) (types.ModelCost, bool) {
	mc, ok := c[catalogKey(provider, model)]
	return mc, ok
}

// picker chooses one member from a group's candidates. selectName is the
// group's configured `select:` strategy; lookup may be nil (no catalog
// configured), in which case any cost/latency strategy degrades to
// first-listed rather than erroring.
type picker func(members []AliasMember, selectName string, lookup CostLatencyLookup) AliasMember

// selectMember applies a strategy to `members`. Unknown strategies and
// strategies with no usable catalog data both fall back to the first
// member: deterministic, so a missing catalog row shows up as a routing
// decision to investigate rather than as silent random spread.
func selectMember(members []AliasMember, selectName string, lookup CostLatencyLookup) AliasMember {
	if len(members) == 0 {
		return AliasMember{}
	}
	strategy := selectName
	if strategy == "" {
		strategy = "random"
	}
	switch strategy {
	case "random":
		return randomPick(members)
	case "cheapest_input":
		return bestBy(members, lookup, func(mc types.ModelCost) float64 { return mc.InputCostPerMTok })
	case "cheapest_output":
		return bestBy(members, lookup, func(mc types.ModelCost) float64 { return mc.OutputCostPerMTok })
	case "fastest":
		return bestBy(members, lookup, func(mc types.ModelCost) float64 { return float64(mc.LatencyMsP50) })
	default:
		return members[0]
	}
}

// bestBy returns the member with the lowest `score` whose catalog entry is
// known. Members with no catalog entry are treated as unknown and rank last.
// Ties are broken by declared order (first-listed wins), matching
// PolicyRouter's first-match-wins convention. If no member has a known
// entry, the first member is returned.
func bestBy(members []AliasMember, lookup CostLatencyLookup, score func(types.ModelCost) float64) AliasMember {
	if lookup == nil {
		return members[0]
	}
	best := -1
	var bestScore float64
	for i, m := range members {
		mc, ok := lookup.Lookup(m.Provider, m.Model)
		if !ok {
			continue
		}
		s := score(mc)
		if best == -1 || s < bestScore {
			best = i
			bestScore = s
		}
	}
	if best == -1 {
		return members[0]
	}
	return members[best]
}
