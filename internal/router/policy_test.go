package router

import (
	"context"
	"testing"

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
		{When: PolicyCondition{Intent: "code_generation"}, Provider: "claude"},
		{When: PolicyCondition{}, Provider: "gpt4"}, // wildcard catch-all
	}
	pr := NewPolicyRouter("test", rules, testProviders())

	route, _, err := pr.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{Intent: "code_generation"})
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
		{When: PolicyCondition{Intent: "code_generation"}, Provider: "claude"},
		{When: PolicyCondition{}, Provider: "gpt4"},
	}
	pr := NewPolicyRouter("test", rules, testProviders())

	route, _, err := pr.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{Intent: "chat"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.Provider != "gpt4" {
		t.Errorf("provider = %q, want gpt4", route.Provider)
	}
}

func TestPolicyRouterNoMatchErrors(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{Intent: "code_generation"}, Provider: "claude"},
	}
	pr := NewPolicyRouter("test", rules, testProviders())

	_, _, err := pr.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{Intent: "chat"})
	if err == nil {
		t.Fatal("expected error for no matching rule, got nil")
	}
}

func TestPolicyRouterUnconfiguredProviderErrors(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{}, Provider: "nonexistent"},
	}
	pr := NewPolicyRouter("test", rules, testProviders())

	_, _, err := pr.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{})
	if err == nil {
		t.Fatal("expected error for unconfigured provider, got nil")
	}
}

func TestPolicyRouterCapabilitiesRequireAll(t *testing.T) {
	rules := []PolicyRule{
		{When: PolicyCondition{Capabilities: []string{"vision", "tool_use"}}, Provider: "gpt4"},
	}
	pr := NewPolicyRouter("test", rules, testProviders())

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
	pr := NewPolicyRouter("test", rules, testProviders())

	route, _, err := pr.Route(context.Background(), &types.NormalizedRequest{Model: "claude-3-opus-20250219"}, types.Signals{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// rule's explicit model wins over the request's requested model
	if route.Model != "claude-3-haiku-20250307" {
		t.Errorf("model = %q, want claude-3-haiku-20250307", route.Model)
	}
}

func TestPolicyRouterChainedWithSimpleFallback(t *testing.T) {
	policy := NewPolicyRouter("policy", []PolicyRule{
		{When: PolicyCondition{Intent: "code_generation"}, Provider: "claude"},
	}, testProviders())
	simple := NewSimpleRouter("fallback", "gpt4", "", testProviders())
	chained := NewChainedRouter("chained", []Router{policy, simple})

	route, _, err := chained.Route(context.Background(), &types.NormalizedRequest{}, types.Signals{Intent: "chat"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.Provider != "gpt4" {
		t.Errorf("provider = %q, want gpt4 (fallen through to SimpleRouter)", route.Provider)
	}
}
