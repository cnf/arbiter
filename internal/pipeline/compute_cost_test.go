package pipeline

import (
	"testing"

	"github.com/cnf/arbiter/internal/router"
	"github.com/cnf/arbiter/pkg/types"
)

// TestComputeCostPricesCacheTokens is the missing end-to-end proof for #18 on
// the arbiter side (catalog-convert emitting the rates was verified
// separately): given a catalog entry that states cache rates, computeCost
// must price usage.CacheRead/CacheWrite at those rates and add them to the
// plain input/output cost, not silently drop them.
func TestComputeCostPricesCacheTokens(t *testing.T) {
	catalog := router.NewStaticCatalog([]types.ModelCost{{
		Provider:              "anthropic",
		Model:                 "claude-sonnet-5",
		InputCostPerMTok:      3,
		OutputCostPerMTok:     15,
		CacheReadCostPerMTok:  0.3,
		CacheWriteCostPerMTok: 3.75,
	}})
	p := &Pipeline{costCatalog: catalog}

	usage := types.Usage{
		InputTokens:  1000,
		OutputTokens: 500,
		CacheRead:    100_000,
		CacheWrite:   10_000,
	}
	got := p.computeCost("anthropic", "claude-sonnet-5", usage)

	// Expected, all in USD, at the per-million-token rates above:
	//   input:       1000       * 3    / 1e6 = 0.003
	//   output:      500        * 15   / 1e6 = 0.0075
	//   cache read:  100000     * 0.3  / 1e6 = 0.03
	//   cache write: 10000      * 3.75 / 1e6 = 0.0375
	want := 0.003 + 0.0075 + 0.03 + 0.0375
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("computeCost = %v, want %v (cache tokens must be priced in)", got, want)
	}
}

// TestComputeCostCacheTokensAreZeroWithNoRates proves a catalog entry that
// states no cache pricing (the pre-#18 shape, and any provider whose upstream
// simply doesn't report cache rates) prices cache tokens at 0 rather than
// erroring or falling back to the plain input rate — an unstated rate must
// stay an explicit zero, never a guess.
func TestComputeCostCacheTokensAreZeroWithNoRates(t *testing.T) {
	catalog := router.NewStaticCatalog([]types.ModelCost{{
		Provider:          "anthropic",
		Model:             "claude-sonnet-5",
		InputCostPerMTok:  3,
		OutputCostPerMTok: 15,
	}})
	p := &Pipeline{costCatalog: catalog}

	usage := types.Usage{InputTokens: 1000, OutputTokens: 500, CacheRead: 100_000, CacheWrite: 10_000}
	got := p.computeCost("anthropic", "claude-sonnet-5", usage)

	want := 0.003 + 0.0075
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("computeCost = %v, want %v (cache tokens must cost 0, not be estimated from the plain rate)", got, want)
	}
}

// TestComputeCostLeavesProviderReportedCostAlone proves a provider that
// already reports a real cost_usd (OpenRouter) is never overridden by the
// catalog math, cache-aware or not.
func TestComputeCostLeavesProviderReportedCostAlone(t *testing.T) {
	catalog := router.NewStaticCatalog([]types.ModelCost{{
		Provider: "openrouter", Model: "x", InputCostPerMTok: 999, CacheReadCostPerMTok: 999,
	}})
	p := &Pipeline{costCatalog: catalog}

	usage := types.Usage{InputTokens: 1000, CacheRead: 1000, CostUSD: 0.0042}
	got := p.computeCost("openrouter", "x", usage)
	if got != 0.0042 {
		t.Errorf("computeCost = %v, want 0.0042 (provider-reported cost must win)", got)
	}
}
