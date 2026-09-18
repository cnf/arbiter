package router

import (
	"context"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

// Capability-guarded rules. A rule can demand that its *target* accept certain
// input modalities, and a rule whose target cannot satisfy that is SKIPPED so
// matching continues — which is what lets a chain read as "send image traffic
// here, everything else there" without the operator writing the negative case.
//
// The rule that matters most: unknown is not permission. A model whose
// modalities are simply unstated must not be routed image traffic, because that
// guess is exactly what this feature exists to stop.

// capCatalog is a CostLatencyLookup that only answers capability questions.
type capCatalog struct {
	modalities map[string][]string
}

func (c capCatalog) Lookup(provider, model string) (types.ModelCost, bool) {
	m, ok := c.modalities[provider+"/"+model]
	if !ok {
		return types.ModelCost{}, false
	}
	return types.ModelCost{Provider: provider, Model: model, InputModalities: m}, true
}

func capReq() *types.NormalizedRequest { return &types.NormalizedRequest{} }

// TestPolicySkipsRuleWhoseTargetLacksModality is the core behavior: the first
// rule matches but its target cannot take the modality, so matching continues
// to the next rule rather than routing there or failing.
func TestPolicySkipsRuleWhoseTargetLacksModality(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{RequiresInputModalities: []string{"image"}}, Provider: "textonly"},
		{When: PolicyCondition{}, Provider: "vision"},
	}
	cat := capCatalog{modalities: map[string][]string{
		"textonly/m": {"text"},
		"vision/m":   {"text", "image"},
	}}
	pr := NewPolicyRouter("test", rules, capProviders(), nil, cat)

	route, _, err := pr.Route(context.Background(), capReq(), types.Signals{})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if route.Provider != "vision" {
		t.Errorf("provider = %q, want vision — the text-only rule must be skipped, not chosen", route.Provider)
	}
}

// TestPolicyUsesRuleWhoseTargetHasModality is the positive half: when the first
// rule's target CAN satisfy the requirement, it is used and matching stops.
func TestPolicyUsesRuleWhoseTargetHasModality(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{RequiresInputModalities: []string{"image"}}, Provider: "vision"},
		{When: PolicyCondition{}, Provider: "fallback"},
	}
	cat := capCatalog{modalities: map[string][]string{
		"vision/m": {"text", "image"},
	}}
	pr := NewPolicyRouter("test", rules, capProviders(), nil, cat)

	route, _, err := pr.Route(context.Background(), capReq(), types.Signals{})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if route.Provider != "vision" {
		t.Errorf("provider = %q, want vision", route.Provider)
	}
}

// TestPolicySkipsRuleWhenModalitiesAreUnknown is the load-bearing one. A model
// with no capability data must NOT satisfy a modality requirement: unknown is
// not permission, and routing image traffic to a model that never said it
// accepts images is the guess this whole feature exists to prevent.
func TestPolicySkipsRuleWhenModalitiesAreUnknown(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{RequiresInputModalities: []string{"image"}}, Provider: "mystery"},
		{When: PolicyCondition{}, Provider: "known"},
	}
	// "mystery" is deliberately absent from the catalog entirely, and "blank"
	// has a row stating nothing — both mean unknown.
	cat := capCatalog{modalities: map[string][]string{
		"known/m": {"text", "image"},
		"blank/m": {},
	}}
	pr := NewPolicyRouter("test", rules, capProviders(), nil, cat)

	route, _, err := pr.Route(context.Background(), capReq(), types.Signals{})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if route.Provider != "known" {
		t.Errorf("provider = %q, want known — a model with no stated modalities must not satisfy an image requirement", route.Provider)
	}
}

// TestPolicySkipsGuardedRuleWithNoCatalog proves an unverifiable guard does not
// silently pass. Without a catalog the requirement cannot be checked, and a
// guard that cannot be verified must not route the request.
func TestPolicySkipsGuardedRuleWithNoCatalog(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{RequiresInputModalities: []string{"image"}}, Provider: "vision"},
		{When: PolicyCondition{}, Provider: "plain"},
	}
	pr := NewPolicyRouter("test", rules, capProviders(), nil, nil) // no catalog

	route, _, err := pr.Route(context.Background(), capReq(), types.Signals{})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if route.Provider != "plain" {
		t.Errorf("provider = %q, want plain — an unverifiable modality guard must not pass", route.Provider)
	}
}

// TestPolicyUnscopedRulesUnaffected proves the field is additive: rules that
// never mention modalities behave exactly as before, catalog or not. Existing
// configs must not change meaning.
func TestPolicyUnscopedRulesUnaffected(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{Domain: "code_generation"}, Provider: "vision"},
	}
	cat := capCatalog{modalities: map[string][]string{
		"vision/m": {"text"}, // would fail an image guard; there is none
	}}
	pr := NewPolicyRouter("test", rules, capProviders(), nil, cat)

	route, _, err := pr.Route(context.Background(), capReq(), types.Signals{Domain: "code_generation"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if route.Provider != "vision" {
		t.Errorf("provider = %q, want vision — an unscoped rule must not consult capabilities at all", route.Provider)
	}
}

// TestPolicyErrorsWhenEveryGuardedRuleIsSkipped proves skipping does not turn a
// no-match into a silent success. If every rule is skipped there is nothing to
// route to, and that must surface as the router's usual no-match error so a
// chained fallback router can take over.
func TestPolicyErrorsWhenEveryGuardedRuleIsSkipped(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{RequiresInputModalities: []string{"image"}}, Provider: "textonly"},
	}
	cat := capCatalog{modalities: map[string][]string{
		"textonly/m": {"text"},
	}}
	pr := NewPolicyRouter("test", rules, capProviders(), nil, cat)

	if _, _, err := pr.Route(context.Background(), capReq(), types.Signals{}); err == nil {
		t.Error("Route returned no error, want a no-match error so a chained router can fall through")
	}
}

// capProviders is a minimal provider table matching capCatalog's keys.
func capProviders() map[string]types.ProviderConfig {
	return map[string]types.ProviderConfig{
		"textonly": {Name: "textonly", Type: "openai", Models: []string{"m"}},
		"vision":   {Name: "vision", Type: "openai", Models: []string{"m"}},
		"fallback": {Name: "fallback", Type: "openai", Models: []string{"m"}},
		"mystery":  {Name: "mystery", Type: "openai", Models: []string{"m"}},
		"known":    {Name: "known", Type: "openai", Models: []string{"m"}},
		"blank":    {Name: "blank", Type: "openai", Models: []string{"m"}},
		"plain":    {Name: "plain", Type: "openai", Models: []string{"m"}},
	}
}
