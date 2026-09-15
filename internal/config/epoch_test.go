package config

import "testing"

// The epoch is the join key that lets recorded requests be attributed to the
// configuration that produced them, so its two load-bearing properties are
// stability (same effective config -> same epoch) and sensitivity (a
// behavior-bearing change -> a different epoch).

func TestEpochStableAcrossLoads(t *testing.T) {
	a := loadConfigOK(t, baseConfig).Epoch()
	b := loadConfigOK(t, baseConfig).Epoch()
	if a == "" {
		t.Fatal("epoch is empty")
	}
	if a != b {
		t.Fatalf("the same config produced different epochs: %q vs %q", a, b)
	}
}

// Map ordering must not leak into the hash: Go randomizes map iteration, and
// the config is full of maps (providers, headers, keywords). A stable epoch
// across repeated loads of identical input is what proves the marshaling is
// order-independent.
func TestEpochStableForMapHeavyConfig(t *testing.T) {
	cfg := baseConfig + `
classifiers:
  - name: "dom"
    type: "heuristic"
    config:
      keywords:
        chat: ["hello", "hi"]
        code_generation: ["write a function", "refactor"]
        analysis: ["compare", "explain"]
`
	first := loadConfigOK(t, cfg).Epoch()
	for i := 0; i < 20; i++ {
		if got := loadConfigOK(t, cfg).Epoch(); got != first {
			t.Fatalf("epoch changed on iteration %d: %q vs %q", i, got, first)
		}
	}
}

func TestEpochChangesWithBehaviorBearingFields(t *testing.T) {
	base := loadConfigOK(t, baseConfig).Epoch()

	cases := map[string]string{
		"model list": `
version: "1.0"
providers:
  claude:
    type: "anthropic"
    endpoint: "https://api.anthropic.com"
    models: ["claude-3-opus", "claude-3-haiku", "claude-3-sonnet"]
routers:
  - name: "primary"
    type: "simple"
    config:
      default_provider: "claude"
logging:
  level: "info"
`,
		"endpoint": `
version: "1.0"
providers:
  claude:
    type: "anthropic"
    endpoint: "https://api.anthropic.com/v2"
    models: ["claude-3-opus", "claude-3-haiku"]
routers:
  - name: "primary"
    type: "simple"
    config:
      default_provider: "claude"
logging:
  level: "info"
`,
		"catalog row": baseConfig + `
model_catalog:
  - provider: "claude"
    model: "claude-3-haiku"
    input_cost_per_mtok: 0.25
    output_cost_per_mtok: 1.25
`,
		"guardrail": baseConfig + `
guardrails:
  pre:
    - name: "sp"
      type: "system_prompt"
      config: { prompt: "be nice", override: false }
`,
		"routing fallback": baseConfig + `
routing:
  fallback_providers: ["gpt4"]
`,
	}

	for name, body := range cases {
		if got := loadConfigOK(t, body).Epoch(); got == base {
			t.Errorf("changing the %s produced the same epoch %q", name, got)
		}
	}
}

// Rotating an API key changes no routing or classification behavior, so a
// secret rotation must not split the recorded data into two epochs.
func TestEpochIgnoresAPIKeyChanges(t *testing.T) {
	withKey := func(key string) string {
		return `
version: "1.0"
providers:
  claude:
    type: "anthropic"
    endpoint: "https://api.anthropic.com"
    key: "` + key + `"
    models: ["claude-3-haiku"]
routers:
  - name: "primary"
    type: "simple"
    config:
      default_provider: "claude"
logging:
  level: "info"
`
	}
	if a, b := loadConfigOK(t, withKey("key-one")).Epoch(), loadConfigOK(t, withKey("key-two")).Epoch(); a != b {
		t.Fatalf("rotating an API key changed the epoch: %q vs %q", a, b)
	}
}

// A regenerated catalog file is a real change to what routing reads, so it
// must start a new epoch even though lanes.yaml itself is untouched.
func TestEpochReflectsMergedCatalogFile(t *testing.T) {
	main := baseConfig + `
model_catalog_file: "catalog.yaml"
`
	fileV1 := `model_catalog:
  - provider: "claude"
    model: "claude-3-opus"
    input_cost_per_mtok: 1.0
    output_cost_per_mtok: 2.0
`
	fileV2 := `model_catalog:
  - provider: "claude"
    model: "claude-3-opus"
    input_cost_per_mtok: 9.0
    output_cost_per_mtok: 9.0
`
	a, err := loadConfigWithFile(t, main, fileV1)
	if err != nil {
		t.Fatalf("Load v1: %v", err)
	}
	b, err := loadConfigWithFile(t, main, fileV2)
	if err != nil {
		t.Fatalf("Load v2: %v", err)
	}
	if a.Epoch() == b.Epoch() {
		t.Fatalf("a changed catalog file did not change the epoch (%q)", a.Epoch())
	}
}
