package router

import (
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

func testCatalog() StaticCatalog {
	return NewStaticCatalog([]types.ModelCost{
		{Provider: "cheap", Model: "small", InputCostPerMTok: 0.10, OutputCostPerMTok: 0.40, LatencyMsP50: 900},
		{Provider: "pricey", Model: "large", InputCostPerMTok: 3.00, OutputCostPerMTok: 15.0, LatencyMsP50: 200},
	})
}

var costMembers = []AliasMember{
	{Provider: "pricey", Model: "large"},
	{Provider: "cheap", Model: "small"},
}

func TestSelectCheapestInput(t *testing.T) {
	got := selectMember(costMembers, "cheapest_input", testCatalog())
	if got.Provider != "cheap" {
		t.Errorf("cheapest_input picked %q, want cheap", got.Provider)
	}
}

func TestSelectCheapestOutput(t *testing.T) {
	got := selectMember(costMembers, "cheapest_output", testCatalog())
	if got.Provider != "cheap" {
		t.Errorf("cheapest_output picked %q, want cheap", got.Provider)
	}
}

func TestSelectFastest(t *testing.T) {
	got := selectMember(costMembers, "fastest", testCatalog())
	if got.Provider != "pricey" {
		t.Errorf("fastest picked %q, want pricey (lower latency)", got.Provider)
	}
}

func TestSelectUnknownCostRanksLast(t *testing.T) {
	// "mystery" has no catalog row; it must not win a cheapest strategy even
	// though it is listed first.
	members := []AliasMember{
		{Provider: "mystery", Model: "unknown"},
		{Provider: "cheap", Model: "small"},
	}
	got := selectMember(members, "cheapest_input", testCatalog())
	if got.Provider != "cheap" {
		t.Errorf("cheapest_input picked %q, want cheap (unknown-cost must rank last)", got.Provider)
	}
}

func TestSelectAllUnknownFallsBackToFirstListed(t *testing.T) {
	members := []AliasMember{
		{Provider: "mystery", Model: "one"},
		{Provider: "mystery", Model: "two"},
	}
	got := selectMember(members, "cheapest_input", testCatalog())
	if got.Provider != "mystery" || got.Model != "one" {
		t.Errorf("with no catalog coverage, want first-listed (mystery, one), got (%s, %s)", got.Provider, got.Model)
	}
}

func TestSelectTieBreaksFirstListed(t *testing.T) {
	cat := NewStaticCatalog([]types.ModelCost{
		{Provider: "a", Model: "x", InputCostPerMTok: 1.0},
		{Provider: "b", Model: "y", InputCostPerMTok: 1.0},
	})
	got := selectMember([]AliasMember{{Provider: "a", Model: "x"}, {Provider: "b", Model: "y"}}, "cheapest_input", cat)
	if got.Provider != "a" {
		t.Errorf("tie should break to first-listed (a), got %q", got.Provider)
	}
}

func TestSelectNilCatalogDegradesToFirstListed(t *testing.T) {
	got := selectMember(costMembers, "cheapest_input", nil)
	if got.Provider != "pricey" {
		t.Errorf("nil catalog should degrade to first-listed (pricey), got %q", got.Provider)
	}
}

func TestSelectUnknownStrategyFallsBackToFirstListed(t *testing.T) {
	got := selectMember(costMembers, "bogus", testCatalog())
	if got.Provider != "pricey" {
		t.Errorf("unknown strategy should fall back to first-listed (pricey), got %q", got.Provider)
	}
}

func TestSelectEmptyStrategyIsRandom(t *testing.T) {
	// Empty select means the default; it must resolve to one of the members.
	got := selectMember(costMembers, "", testCatalog())
	if got.Provider != "pricey" && got.Provider != "cheap" {
		t.Errorf("empty select returned an unexpected member: %+v", got)
	}
}

func TestSelectEmptyMembersReturnsZero(t *testing.T) {
	if got := selectMember(nil, "cheapest_input", testCatalog()); got != (AliasMember{}) {
		t.Errorf("empty member list should yield zero AliasMember, got %+v", got)
	}
}

func TestSelectOrderedPicksFirstListed(t *testing.T) {
	// "ordered" is the strategy that needs no catalog: the declared order IS
	// the preference. costMembers lists the pricey member first, so ordered
	// must pick it even though a cost strategy would pick "cheap".
	//
	// NOTE: this passes with the `case "ordered"` dispatch removed, because
	// `default:` already returns members[0] — so it documents the behavior but
	// does NOT prove the case exists. TestOrderedIsARegisteredSelectStrategy in
	// internal/config is the test that actually gates the feature.
	got := selectMember(costMembers, "ordered", testCatalog())
	if got.Provider != "pricey" {
		t.Errorf("ordered picked %q, want pricey (first-listed, regardless of cost)", got.Provider)
	}
}

func TestSelectOrderedIgnoresCatalog(t *testing.T) {
	// A nil catalog must not change an ordered pick — unlike every other
	// non-random strategy, ordered has no catalog dependency at all.
	withCat := selectMember(costMembers, "ordered", testCatalog())
	withoutCat := selectMember(costMembers, "ordered", nil)
	if withCat != withoutCat {
		t.Errorf("ordered differs by catalog presence: %+v vs %+v", withCat, withoutCat)
	}
}
