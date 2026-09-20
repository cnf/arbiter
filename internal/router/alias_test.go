package router

import (
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

func aliasProviders() map[string]types.ProviderConfig {
	return map[string]types.ProviderConfig{
		"claude": {Name: "claude", Type: "anthropic", Models: []string{"claude-3-opus-20250219", "claude-3-haiku-20250307"}},
		"gpt4":   {Name: "gpt4", Type: "openai", Models: []string{"gpt-4o"}},
		"local":  {Name: "local", Type: "ollama", Models: []string{"llama2"}},
	}
}

func TestAliasResolverForce(t *testing.T) {
	aliases := map[string]Alias{
		"coding": {Name: "coding", Force: map[string][]string{"domain": {"code_generation"}}},
		"pinned": {Name: "pinned", Type: "pinned", Provider: "claude", Model: "claude-3-haiku-20250307"},
	}
	r := NewAliasResolver(aliases, aliasProviders(), nil, nil)

	force, ok := r.Force("coding")
	if !ok {
		t.Fatal("expected coding to be a force alias")
	}
	if force["domain"][0] != "code_generation" {
		t.Errorf("force[domain] = %v, want [code_generation]", force["domain"])
	}

	if _, ok := r.Force("pinned"); ok {
		t.Error("pinned alias must not report a force override")
	}
	if _, ok := r.Force("nonexistent"); ok {
		t.Error("unknown name must not report a force override")
	}
}

func TestAliasResolverResolvePinned(t *testing.T) {
	aliases := map[string]Alias{
		"cheap-claude": {Name: "cheap-claude", Type: "pinned", Provider: "claude", Model: "claude-3-haiku-20250307"},
	}
	r := NewAliasResolver(aliases, aliasProviders(), nil, nil)

	provider, model, ok, err := r.Resolve("cheap-claude")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected cheap-claude to resolve")
	}
	if provider != "claude" || model != "claude-3-haiku-20250307" {
		t.Errorf("resolved (%q, %q), want (claude, claude-3-haiku-20250307)", provider, model)
	}
}

func TestAliasResolverResolveGroup(t *testing.T) {
	aliases := map[string]Alias{
		"free-search": {
			Name: "free-search",
			Type: "group",
			Members: []AliasMember{
				{Provider: "gpt4", Model: "gpt-4o"},
				{Provider: "local", Model: "llama2"},
			},
		},
	}
	// Fixed picker so the test is deterministic.
	pickCalls := 0
	pick := func(members []AliasMember, _ string, _ CostLatencyLookup) AliasMember {
		pickCalls++
		return members[0]
	}
	r := NewAliasResolver(aliases, aliasProviders(), pick, nil)

	provider, model, ok, err := r.Resolve("free-search")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected free-search to resolve")
	}
	if provider != "gpt4" || model != "gpt-4o" {
		t.Errorf("resolved (%q, %q), want (gpt4, gpt-4o)", provider, model)
	}
	if pickCalls != 1 {
		t.Errorf("pick called %d times, want exactly 1", pickCalls)
	}
}

func TestAliasResolverResolveUnknownNameOK(t *testing.T) {
	r := NewAliasResolver(nil, aliasProviders(), nil, nil)

	_, _, ok, err := r.Resolve("not-an-alias")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected ok=false for a name that isn't a configured alias")
	}
}

func TestAliasResolverResolveAliasToAlias(t *testing.T) {
	aliases := map[string]Alias{
		"inner": {Name: "inner", Type: "pinned", Provider: "claude", Model: "claude-3-opus-20250219"},
		"outer": {Name: "outer", Type: "group", Members: []AliasMember{{Provider: "inner"}}},
	}
	pick := func(members []AliasMember, _ string, _ CostLatencyLookup) AliasMember { return members[0] }
	r := NewAliasResolver(aliases, aliasProviders(), pick, nil)

	provider, model, ok, err := r.Resolve("outer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected outer to resolve")
	}
	if provider != "claude" || model != "claude-3-opus-20250219" {
		t.Errorf("resolved (%q, %q), want (claude, claude-3-opus-20250219) via inner", provider, model)
	}
}

func TestAliasResolverResolveAliasToAliasModelOverride(t *testing.T) {
	aliases := map[string]Alias{
		"inner": {Name: "inner", Type: "pinned", Provider: "claude", Model: "claude-3-opus-20250219"},
		// outer's member names inner but overrides its model.
		"outer": {Name: "outer", Type: "group", Members: []AliasMember{{Provider: "inner", Model: "claude-3-haiku-20250307"}}},
	}
	pick := func(members []AliasMember, _ string, _ CostLatencyLookup) AliasMember { return members[0] }
	r := NewAliasResolver(aliases, aliasProviders(), pick, nil)

	provider, model, ok, err := r.Resolve("outer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected outer to resolve")
	}
	if provider != "claude" || model != "claude-3-haiku-20250307" {
		t.Errorf("resolved (%q, %q), want (claude, claude-3-haiku-20250307): member's own model overrides inner's", provider, model)
	}
}

func TestAliasResolverResolveCycleErrors(t *testing.T) {
	aliases := map[string]Alias{
		"a": {Name: "a", Type: "group", Members: []AliasMember{{Provider: "b"}}},
		"b": {Name: "b", Type: "group", Members: []AliasMember{{Provider: "a"}}},
	}
	pick := func(members []AliasMember, _ string, _ CostLatencyLookup) AliasMember { return members[0] }
	r := NewAliasResolver(aliases, aliasProviders(), pick, nil)

	_, _, ok, err := r.Resolve("a")
	if err == nil {
		t.Fatal("expected an error resolving a cycle, got nil")
	}
	if !ok {
		t.Error("a cyclic alias is still a configured alias, so ok should be true even though err is set")
	}
}

func TestAliasResolverForceAliasCannotBeATarget(t *testing.T) {
	aliases := map[string]Alias{
		"coding": {Name: "coding", Force: map[string][]string{"domain": {"code_generation"}}},
	}
	r := NewAliasResolver(aliases, aliasProviders(), nil, nil)

	_, _, ok, err := r.Resolve("coding")
	if err == nil {
		t.Fatal("expected an error resolving a force-alias as a target")
	}
	if !ok {
		t.Error("coding is a configured alias, so ok should be true even though err is set")
	}
}

func TestAliasResolverForceEmptyIsStillAForceAlias(t *testing.T) {
	// An empty force block is the "full auto" alias: it names no axis
	// overrides, but it is still a force alias — it must not be treated as a
	// pinned/group target (which would make a client naming it an error).
	aliases := map[string]Alias{
		"auto": {Name: "auto", Force: map[string][]string{}},
	}
	r := NewAliasResolver(aliases, aliasProviders(), nil, nil)

	force, ok := r.Force("auto")
	if !ok {
		t.Fatal("an empty force block is still a force alias")
	}
	if len(force) != 0 {
		t.Errorf("force = %v, want empty", force)
	}
	if _, _, _, err := r.Resolve("auto"); err == nil {
		t.Error("resolving a force alias as a concrete target should error")
	}
}

func TestAliasResolverGroupSelectCheapest(t *testing.T) {
	aliases := map[string]Alias{
		"budget": {
			Name:   "budget",
			Type:   "group",
			Select: "cheapest_input",
			Members: []AliasMember{
				{Provider: "claude", Model: "claude-3-opus-20250219"},
				{Provider: "claude", Model: "claude-3-haiku-20250307"},
			},
		},
	}
	cat := NewStaticCatalog([]types.ModelCost{
		{Provider: "claude", Model: "claude-3-opus-20250219", InputCostPerMTok: 15.0},
		{Provider: "claude", Model: "claude-3-haiku-20250307", InputCostPerMTok: 0.25},
	})
	r := NewAliasResolver(aliases, aliasProviders(), nil, cat)

	_, model, ok, err := r.Resolve("budget")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected budget to resolve")
	}
	if model != "claude-3-haiku-20250307" {
		t.Errorf("select: cheapest_input picked %q, want the haiku model", model)
	}
}

func TestAliasResolverGroupFallbacks(t *testing.T) {
	aliases := map[string]Alias{
		"free-search": {
			Name: "free-search",
			Type: "group",
			Members: []AliasMember{
				{Provider: "gpt4", Model: "gpt-4o"},
				{Provider: "local", Model: "llama2"},
			},
		},
	}
	r := NewAliasResolver(aliases, aliasProviders(), nil, nil)

	fallbacks := r.GroupFallbacks("free-search", AliasMember{Provider: "gpt4", Model: "gpt-4o"})
	if len(fallbacks) != 1 {
		t.Fatalf("expected 1 fallback (the non-selected member), got %d", len(fallbacks))
	}
	if fallbacks[0].Provider != "local" || fallbacks[0].Model != "llama2" {
		t.Errorf("fallback = %+v, want provider=local model=llama2", fallbacks[0])
	}

	// A non-group name yields nothing.
	if got := r.GroupFallbacks("nonexistent", AliasMember{}); got != nil {
		t.Errorf("expected nil fallbacks for unknown name, got %v", got)
	}
}

// TestAliasResolverGroupOrderedDegradesInDeclaredOrder is the load-bearing test
// for select: "ordered". Asserting only the primary would pass even if the
// fallback chain were shuffled, and the ordering IS the feature — so this
// asserts the primary AND the full degradation sequence.
func TestAliasResolverGroupOrderedDegradesInDeclaredOrder(t *testing.T) {
	aliases := map[string]Alias{
		"tiered": {
			Name:   "tiered",
			Type:   "group",
			Select: "ordered",
			Members: []AliasMember{
				{Provider: "gpt4", Model: "gpt-4o"},
				{Provider: "local", Model: "llama2"},
				{Provider: "claude", Model: "claude-3-haiku-20250307"},
			},
		},
	}
	r := NewAliasResolver(aliases, aliasProviders(), nil, nil)

	provider, model, ok, err := r.Resolve("tiered")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected tiered to resolve")
	}
	if provider != "gpt4" || model != "gpt-4o" {
		t.Fatalf("ordered primary = %s/%s, want gpt4/gpt-4o (first-listed)", provider, model)
	}

	// The degradation path must be the remaining members, in declared order.
	fallbacks := r.GroupFallbacks("tiered", AliasMember{Provider: provider, Model: model})
	if len(fallbacks) != 2 {
		t.Fatalf("expected 2 fallbacks, got %d: %+v", len(fallbacks), fallbacks)
	}
	want := []AliasMember{
		{Provider: "local", Model: "llama2"},
		{Provider: "claude", Model: "claude-3-haiku-20250307"},
	}
	for i, w := range want {
		if fallbacks[i].Provider != w.Provider || fallbacks[i].Model != w.Model {
			t.Errorf("fallback[%d] = %s/%s, want %s/%s (declared order)",
				i, fallbacks[i].Provider, fallbacks[i].Model, w.Provider, w.Model)
		}
	}
}
