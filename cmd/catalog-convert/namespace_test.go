package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Namespace on the emitted model name. Arbiter joins a catalog row to a declared
// model on the exact string, so a provider serving its models under a namespace
// (an omniroute-style prefixing proxy, or a self-hosted proxy fronting another
// vendor's API) gets no matches at all without this — the rows are emitted, they
// just never line up with anything declared.

// TestBuildCatalogAppliesNamespace is the core case: bare litellm keys come out
// namespaced, matching how such a provider's models are declared.
func TestBuildCatalogAppliesNamespace(t *testing.T) {
	entries := map[string]litellmEntry{
		"claude-sonnet-5": {LitellmProvider: "anthropic", Mode: "chat", InputCostPerToken: 3e-06},
	}
	m := mapping{
		Providers: map[string]providerMap{
			"claude": {LitellmProvider: "anthropic", Namespace: "claude/"},
		},
	}

	rows, skips := buildCatalog(entries, m, []string{"claude"})
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Model != "claude/claude-sonnet-5" {
		t.Errorf("Model = %q, want claude/claude-sonnet-5", rows[0].Model)
	}
	if rows[0].Provider != "claude" {
		t.Errorf("Provider = %q, want claude", rows[0].Provider)
	}
}

// TestBuildCatalogNamespaceEmptyIsUnchanged guards backward compatibility: an
// absent namespace must leave the emitted name exactly as it was, so every
// existing mapping file produces a byte-identical catalog.
func TestBuildCatalogNamespaceEmptyIsUnchanged(t *testing.T) {
	entries := map[string]litellmEntry{
		"claude-sonnet-5": {LitellmProvider: "anthropic", Mode: "chat"},
	}
	withoutNamespace := mapping{
		Providers: map[string]providerMap{"claude": {LitellmProvider: "anthropic"}},
	}
	withEmptyNamespace := mapping{
		Providers: map[string]providerMap{"claude": {LitellmProvider: "anthropic", Namespace: ""}},
	}

	a, _ := buildCatalog(entries, withoutNamespace, []string{"claude"})
	b, _ := buildCatalog(entries, withEmptyNamespace, []string{"claude"})
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("got %d and %d rows, want 1 each", len(a), len(b))
	}
	if a[0].Model != "claude-sonnet-5" || b[0].Model != "claude-sonnet-5" {
		t.Errorf("Models = %q / %q, want both claude-sonnet-5 (namespace must default to no-op)", a[0].Model, b[0].Model)
	}
}

// TestBuildCatalogNamespaceComposesWithKeyPrefix pins the ordering: the input
// prefix is stripped first, then the output namespace is added. The two fix
// opposite mismatches, so a provider could legitimately need both.
func TestBuildCatalogNamespaceComposesWithKeyPrefix(t *testing.T) {
	entries := map[string]litellmEntry{
		"openrouter/anthropic/claude-3.5-sonnet": {
			LitellmProvider: "openrouter", Mode: "chat",
		},
	}
	m := mapping{
		Providers: map[string]providerMap{
			"proxy": {
				LitellmProvider: "openrouter",
				KeyPrefix:       "openrouter/",
				Namespace:       "proxy/",
			},
		},
	}

	rows, skips := buildCatalog(entries, m, []string{"proxy"})
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	// Strip "openrouter/" -> "anthropic/claude-3.5-sonnet", then add "proxy/".
	want := "proxy/anthropic/claude-3.5-sonnet"
	if rows[0].Model != want {
		t.Errorf("Model = %q, want %q", rows[0].Model, want)
	}
}

// TestBuildCatalogLatencyKeyUsesNamespacedName proves the latency override keys
// stay the same shape as the emitted model. The mapping file already documents
// them as "provider/model as Arbiter sees them", and its example entries are
// already written namespaced — so a namespace must not silently orphan them.
func TestBuildCatalogLatencyKeyUsesNamespacedName(t *testing.T) {
	entries := map[string]litellmEntry{
		"claude-sonnet-5": {LitellmProvider: "anthropic", Mode: "chat"},
	}
	m := mapping{
		Providers: map[string]providerMap{
			"claude": {LitellmProvider: "anthropic", Namespace: "claude/", LatencyMsP50: 900},
		},
		LatencyMsP50: map[string]int{"claude/claude-sonnet-5": 750},
	}

	rows, _ := buildCatalog(entries, m, []string{"claude"})
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].LatencyMsP50 != 750 {
		t.Errorf("LatencyMsP50 = %d, want 750 — the override must match the namespaced emitted name, not the bare one", rows[0].LatencyMsP50)
	}
}

// TestBuildCatalogNamespaceReachesCapabilities proves namespace applies to the
// whole row, not just identity — the capabilities are the reason the join
// matters, so a namespaced row must still carry its modalities.
func TestBuildCatalogNamespaceReachesCapabilities(t *testing.T) {
	yes := true
	entries := map[string]litellmEntry{
		"claude-sonnet-5": {
			LitellmProvider: "anthropic", Mode: "chat",
			SupportsVision: &yes, MaxInputTokens: nump("200000"),
		},
	}
	m := mapping{
		Providers: map[string]providerMap{
			"claude": {LitellmProvider: "anthropic", Namespace: "claude/"},
		},
	}

	rows, _ := buildCatalog(entries, m, []string{"claude"})
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if len(rows[0].InputModalities) != 2 || rows[0].InputModalities[0] != "text" || rows[0].InputModalities[1] != "image" {
		t.Errorf("InputModalities = %v, want [text image]", rows[0].InputModalities)
	}
	if rows[0].MaxInputTokens == nil || *rows[0].MaxInputTokens != 200000 {
		t.Errorf("MaxInputTokens = %v, want 200000", rows[0].MaxInputTokens)
	}
}

// TestBuildCatalogLatencyKeyUsesBareName covers the other spelling: a key
// written against the bare litellm name must keep working once a namespace is
// set, because namespace and latency keys are written independently in the
// mapping file and neither shape can be assumed.
func TestBuildCatalogLatencyKeyUsesBareName(t *testing.T) {
	entries := map[string]litellmEntry{
		"claude-sonnet-5": {LitellmProvider: "anthropic", Mode: "chat"},
	}
	m := mapping{
		Providers: map[string]providerMap{
			"claude": {LitellmProvider: "anthropic", Namespace: "claude/", LatencyMsP50: 900},
		},
		LatencyMsP50: map[string]int{"claude/claude-sonnet-5": 750},
	}

	rows, _ := buildCatalog(entries, m, []string{"claude"})
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	// The emitted name is namespaced, so the lookup key would otherwise become
	// "claude/claude/claude-sonnet-5" and match nothing.
	if rows[0].Model != "claude/claude-sonnet-5" {
		t.Fatalf("Model = %q, want claude/claude-sonnet-5", rows[0].Model)
	}
	if rows[0].LatencyMsP50 != 750 {
		t.Errorf("LatencyMsP50 = %d, want 750 — a bare-name override must not be orphaned by a namespace", rows[0].LatencyMsP50)
	}
}

// TestBuildCatalogLatencyFallsBackToProviderDefault proves the override is
// genuinely optional: with no per-model key at all, the provider default still
// applies and nothing is lost.
func TestBuildCatalogLatencyFallsBackToProviderDefault(t *testing.T) {
	entries := map[string]litellmEntry{
		"claude-sonnet-5": {LitellmProvider: "anthropic", Mode: "chat"},
	}
	m := mapping{
		Providers: map[string]providerMap{
			"claude": {LitellmProvider: "anthropic", Namespace: "claude/", LatencyMsP50: 900},
		},
	}

	rows, _ := buildCatalog(entries, m, []string{"claude"})
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].LatencyMsP50 != 900 {
		t.Errorf("LatencyMsP50 = %d, want the provider default 900", rows[0].LatencyMsP50)
	}
}

// TestMappingAcceptsNamespace proves the strict decoder knows the field. The
// decoder runs with KnownFields(true), so a field missing from the struct is a
// load-time error rather than a silent ignore — which would make every mapping
// that uses namespace: fail outright.
func TestMappingAcceptsNamespace(t *testing.T) {
	var m mapping
	dec := yaml.NewDecoder(strings.NewReader(`
providers:
  claude:
    litellm_provider: anthropic
    namespace: "claude/"
`))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decode mapping with namespace: %v", err)
	}
	if got := m.Providers["claude"].Namespace; got != "claude/" {
		t.Errorf("Namespace = %q, want claude/", got)
	}
}
