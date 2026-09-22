package router

import (
	"context"
	"errors"
	"testing"

	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

func testProviders() map[string]types.ProviderConfig {
	return map[string]types.ProviderConfig{
		"claude": {Name: "claude", Type: "anthropic", Models: []string{"claude-3-opus-20250219"}},
		"gpt4":   {Name: "gpt4", Type: "openai", Models: []string{"gpt-4o"}},
	}
}

func TestPolicyRouterFirstMatchWins(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{Domain: "code_generation"}, Provider: "claude"},
		{When: PolicyCondition{}, Provider: "gpt4"}, // wildcard catch-all
	}
	pr := NewPolicyRouter("test", rules, testProviders(), nil, nil)

	route, _, err := pr.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{Domain: "code_generation"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.Provider != "claude" {
		t.Errorf("provider = %q, want claude", route.Provider)
	}
	if route.Model != "claude-3-opus-20250219" {
		t.Errorf("model = %q, want claude-3-opus-20250219", route.Model)
	}
}

func TestPolicyRouterWildcardFallsThrough(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{Domain: "code_generation"}, Provider: "claude"},
		{When: PolicyCondition{}, Provider: "gpt4"},
	}
	pr := NewPolicyRouter("test", rules, testProviders(), nil, nil)

	route, _, err := pr.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{Domain: "chat"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.Provider != "gpt4" {
		t.Errorf("provider = %q, want gpt4", route.Provider)
	}
}

func TestPolicyRouterNoMatchErrors(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{Domain: "code_generation"}, Provider: "claude"},
	}
	pr := NewPolicyRouter("test", rules, testProviders(), nil, nil)

	_, _, err := pr.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{Domain: "chat"})
	if err == nil {
		t.Fatal("expected error for no matching rule, got nil")
	}
}

func TestPolicyRouterUnconfiguredProviderErrors(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{}, Provider: "nonexistent"},
	}
	pr := NewPolicyRouter("test", rules, testProviders(), nil, nil)

	_, _, err := pr.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{})
	if err == nil {
		t.Fatal("expected error for unconfigured provider, got nil")
	}
}

func TestPolicyRouterCapabilitiesRequireAll(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{Capabilities: []string{"vision", "tool_use"}}, Provider: "gpt4"},
	}
	pr := NewPolicyRouter("test", rules, testProviders(), nil, nil)

	// only one of two required capabilities present -> no match
	_, _, err := pr.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{RequiredCapabilities: []string{"vision"}})
	if err == nil {
		t.Fatal("expected no match with partial capabilities, got a route")
	}

	// both present -> match
	route, _, err := pr.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{RequiredCapabilities: []string{"vision", "tool_use"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.Provider != "gpt4" {
		t.Errorf("provider = %q, want gpt4", route.Provider)
	}
}

func TestPolicyRouterModelOverride(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{}, Provider: "claude", Model: "claude-3-haiku-20250307"},
	}
	pr := NewPolicyRouter("test", rules, testProviders(), nil, nil)

	route, _, err := pr.Route(context.Background(), &types.NormalizedRequest{Model: "claude-3-opus-20250219"}, types.Signals{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// rule's explicit model wins over the request's requested model
	if route.Model != "claude-3-haiku-20250307" {
		t.Errorf("model = %q, want claude-3-haiku-20250307", route.Model)
	}
}

func TestPolicyRouterEffortMatches(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{Domain: "code_generation", Effort: "hard"}, Provider: "claude"},
		{When: PolicyCondition{}, Provider: "gpt4"},
	}
	pr := NewPolicyRouter("test", rules, testProviders(), nil, nil)

	route, _, err := pr.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{Domain: "code_generation", Effort: "hard"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.Provider != "claude" {
		t.Errorf("provider = %q, want claude", route.Provider)
	}
}

func TestPolicyRouterEffortWildcardFallsThrough(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{Domain: "code_generation", Effort: "hard"}, Provider: "claude"},
		{When: PolicyCondition{}, Provider: "gpt4"},
	}
	pr := NewPolicyRouter("test", rules, testProviders(), nil, nil)

	// same domain, different effort -> falls through to the wildcard
	route, _, err := pr.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{Domain: "code_generation", Effort: "easy"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.Provider != "gpt4" {
		t.Errorf("provider = %q, want gpt4", route.Provider)
	}
}

func TestPolicyRouterTargetResolvesPinnedAlias(t *testing.T) {
	aliases := map[string]Alias{
		"cheap-claude": {Name: "cheap-claude", Type: "pinned", Provider: "claude", Model: "claude-3-opus-20250219"},
	}
	resolver := NewAliasResolver(aliases, testProviders(), nil, nil)
	rules := []PolicyRule{
		{When: PolicyCondition{}, Target: "cheap-claude"},
	}
	pr := NewPolicyRouter("test", rules, testProviders(), resolver, nil)

	route, _, err := pr.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.Provider != "claude" || route.Model != "claude-3-opus-20250219" {
		t.Errorf("route = %+v, want claude/claude-3-opus-20250219", route)
	}
}

func TestPolicyRouterTargetResolvesGroupWithFallbacks(t *testing.T) {
	aliases := map[string]Alias{
		"free-search": {
			Name: "free-search",
			Type: "group",
			Members: []AliasMember{
				{Provider: "claude", Model: "claude-3-opus-20250219"},
				{Provider: "gpt4", Model: "gpt-4o"},
			},
		},
	}
	pick := func(members []AliasMember, _ string, _ CostLatencyLookup) AliasMember { return members[0] }
	resolver := NewAliasResolver(aliases, testProviders(), pick, nil)
	rules := []PolicyRule{
		{When: PolicyCondition{}, Target: "free-search"},
	}
	pr := NewPolicyRouter("test", rules, testProviders(), resolver, nil)

	route, _, err := pr.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.Provider != "claude" {
		t.Fatalf("provider = %q, want claude (the picked member)", route.Provider)
	}
	if len(route.Fallbacks) != 1 || route.Fallbacks[0].Provider != "gpt4" {
		t.Errorf("Fallbacks = %+v, want one entry for gpt4 (the unselected member)", route.Fallbacks)
	}
}

func TestPolicyRouterTargetWithoutResolverErrors(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{}, Target: "cheap-claude"},
	}
	pr := NewPolicyRouter("test", rules, testProviders(), nil, nil)

	_, _, err := pr.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{})
	if err == nil {
		t.Fatal("expected an error: a Target rule with no configured resolver")
	}
}

func TestPolicyRouterChainedWithSimpleFallback(t *testing.T) {
	policy := NewPolicyRouter("policy", []PolicyRule{
		{When: PolicyCondition{Domain: "code_generation"}, Provider: "claude"},
	}, testProviders(), nil, nil)
	simple := NewSimpleRouter("fallback", "gpt4", "", testProviders())
	chained := NewChainedRouter("chained", []Router{policy, simple})

	route, _, err := chained.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{Domain: "chat"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.Provider != "gpt4" {
		t.Errorf("provider = %q, want gpt4 (fallen through to SimpleRouter)", route.Provider)
	}
}

// A matched rule's Stop target must short-circuit routing entirely: no
// route, and an *arbitererrors.StopError carrying the configured status and
// message — not a generic "no rule matched" miss.
func TestPolicyRouterStopReturnsStopError(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{Domain: "unmatched"}, Stop: &StopTarget{StatusCode: 406, Message: "not like that"}},
		{When: PolicyCondition{}, Provider: "gpt4"},
	}
	pr := NewPolicyRouter("test", rules, testProviders(), nil, nil)

	_, _, err := pr.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{Domain: "unmatched"})
	var stopErr *arbitererrors.StopError
	if !errors.As(err, &stopErr) {
		t.Fatalf("Route error = %v, want an *arbitererrors.StopError", err)
	}
	if stopErr.StatusCode != 406 || stopErr.Message != "not like that" {
		t.Errorf("StopError = {%d, %q}, want {406, \"not like that\"}", stopErr.StatusCode, stopErr.Message)
	}
}

// A Stop target in the catch-all position (When: {}) is the documented
// "refuse everything that reaches here" shape — it must fire exactly like a
// stop on a specific rule.
func TestPolicyRouterStopInCatchAllPosition(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{Domain: "code_generation"}, Provider: "claude"},
		{When: PolicyCondition{}, Stop: &StopTarget{StatusCode: 400, Message: "no policy for this request"}},
	}
	pr := NewPolicyRouter("test", rules, testProviders(), nil, nil)

	_, _, err := pr.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{Domain: "chat"})
	var stopErr *arbitererrors.StopError
	if !errors.As(err, &stopErr) {
		t.Fatalf("Route error = %v, want an *arbitererrors.StopError", err)
	}
	if stopErr.StatusCode != 400 {
		t.Errorf("StatusCode = %d, want 400", stopErr.StatusCode)
	}
}

// The core #43 requirement: ChainedRouter must not treat a StopError as
// "this router doesn't apply here" and fall through to the next one — an
// operator's explicit refusal must not be quietly overridden by a later
// router's fallback.
func TestChainedRouterDoesNotSwallowStop(t *testing.T) {
	policy := NewPolicyRouter("policy", []PolicyRule{
		{When: PolicyCondition{}, Stop: &StopTarget{StatusCode: 406, Message: "refused"}},
	}, testProviders(), nil, nil)
	simple := NewSimpleRouter("fallback", "gpt4", "", testProviders())
	chained := NewChainedRouter("chained", []Router{policy, simple})

	_, _, err := chained.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{})
	var stopErr *arbitererrors.StopError
	if !errors.As(err, &stopErr) {
		t.Fatalf("Route error = %v, want an *arbitererrors.StopError (not a fallthrough to SimpleRouter)", err)
	}
	if stopErr.StatusCode != 406 {
		t.Errorf("StatusCode = %d, want 406", stopErr.StatusCode)
	}
}

// The ordinary "no rule matched" miss — no Stop involved — must still fall
// through a chain exactly as before; only a Stop is terminal.
func TestChainedRouterStillFallsThroughOnOrdinaryMiss(t *testing.T) {
	policy := NewPolicyRouter("policy", []PolicyRule{
		{When: PolicyCondition{Domain: "code_generation"}, Provider: "claude"},
	}, testProviders(), nil, nil)
	simple := NewSimpleRouter("fallback", "gpt4", "", testProviders())
	chained := NewChainedRouter("chained", []Router{policy, simple})

	route, _, err := chained.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{Domain: "chat"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.Provider != "gpt4" {
		t.Errorf("provider = %q, want gpt4 (fallen through to SimpleRouter)", route.Provider)
	}
}
