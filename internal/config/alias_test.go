package config

import (
	"os"
	"strings"
	"testing"
)

// baseConfig is a loadable config skeleton each alias test appends to.
const baseConfig = `
version: "1.0"
providers:
  claude:
    type: "anthropic"
    endpoint: "https://api.anthropic.com"
    models: ["claude-3-opus", "claude-3-haiku"]
  gpt4:
    type: "openai"
    endpoint: "https://api.openai.com/v1"
    models: ["gpt-4o"]
routers:
  - name: "primary"
    type: "simple"
    config:
      default_provider: "claude"
logging:
  level: "info"
`

func loadConfig(t *testing.T, contents string) error {
	t.Helper()
	path := t.TempDir() + "/lanes.yaml"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := Load(path)
	return err
}

func TestAliasConfigLoadsPinnedAndGroup(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  cheap-claude:
    type: "pinned"
    provider: "claude"
    model: "claude-3-haiku"
  free:
    type: "group"
    members:
      - { provider: "gpt4", model: "gpt-4o" }
      - { provider: "claude", model: "claude-3-opus" }
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestAliasRejectsNameCollidingWithProvider(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  claude:
    type: "pinned"
    provider: "claude"
    model: "claude-3-haiku"
`)
	if err == nil || !strings.Contains(err.Error(), "collides with a configured provider") {
		t.Fatalf("Load: want collision error, got %v", err)
	}
}

func TestAliasPinnedRejectsUndeclaredModel(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  bogus:
    type: "pinned"
    provider: "claude"
    model: "not-a-real-model"
`)
	if err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("Load: want undeclared-model error, got %v", err)
	}
}

func TestAliasGroupRejectsUnconfiguredProvider(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  free:
    type: "group"
    members:
      - { provider: "nope", model: "whatever" }
`)
	if err == nil || !strings.Contains(err.Error(), "is not configured") {
		t.Fatalf("Load: want unconfigured-provider error, got %v", err)
	}
}

func TestAliasGroupRejectsEmptyMembers(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  empty:
    type: "group"
`)
	if err == nil || !strings.Contains(err.Error(), "no members") {
		t.Fatalf("Load: want empty-group error, got %v", err)
	}
}

func TestAliasForceRejectsUnknownAxis(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  coding:
    force: { domainn: ["code_generation"] }
`)
	if err == nil || !strings.Contains(err.Error(), "unknown axis") {
		t.Fatalf("Load: want unknown-axis error, got %v", err)
	}
}

func TestAliasForceAcceptsDeprecatedAxisSpelling(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  coding:
    force: { intent: ["code_generation"] }
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestAliasRejectsForceAndTypeTogether(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  mixed:
    force: { domain: ["code_generation"] }
    type: "pinned"
    provider: "claude"
    model: "claude-3-haiku"
`)
	if err == nil || !strings.Contains(err.Error(), "must not also set type") {
		t.Fatalf("Load: want force+type error, got %v", err)
	}
}

func TestAliasCycleRejected(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  a:
    type: "group"
    members:
      - { provider: "b" }
  b:
    type: "group"
    members:
      - { provider: "a" }
`)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("Load: want cycle error, got %v", err)
	}
}

func TestAliasChainNotACycle(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  inner:
    type: "pinned"
    provider: "claude"
    model: "claude-3-haiku"
  outer:
    type: "group"
    members:
      - { provider: "inner" }
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// TestShippedLanesYAMLLoads keeps the committed lanes.yaml honest against the
// code that parses it — a rename or validation change that breaks the shipped
// example should fail here rather than only at process start.
func TestShippedLanesYAMLLoads(t *testing.T) {
	if _, err := Load("../../lanes.yaml"); err != nil {
		t.Fatalf("Load(../../lanes.yaml): %v", err)
	}
}

func TestAliasRejectsNameCollidingWithDeclaredModel(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  gpt-4o:
    type: "pinned"
    provider: "gpt4"
    model: "gpt-4o"
`)
	if err == nil || !strings.Contains(err.Error(), "collides with a model") {
		t.Fatalf("Load: want model-collision error, got %v", err)
	}
}

func TestClassifierAxisValidation(t *testing.T) {
	err := loadConfig(t, baseConfig+`
classifiers:
  - name: "bogus"
    type: "heuristic"
    axis: "domainn"
    config:
      keywords: { chat: ["hi"] }
`)
	if err == nil || !strings.Contains(err.Error(), "unknown axis") {
		t.Fatalf("Load: want unknown classifier-axis error, got %v", err)
	}
}

func TestClassifierCapabilityDetectorRejectsAxis(t *testing.T) {
	err := loadConfig(t, baseConfig+`
classifiers:
  - name: "cap"
    type: "capability_detector"
    axis: "domain"
    config:
      detectors: { vision: ["image"] }
`)
	if err == nil || !strings.Contains(err.Error(), "must not set axis") {
		t.Fatalf("Load: want capability_detector axis error, got %v", err)
	}
}

func TestModelCatalogLoads(t *testing.T) {
	err := loadConfig(t, baseConfig+`
model_catalog:
  - provider: "claude"
    model: "claude-3-haiku"
    input_cost_per_mtok: 0.25
    output_cost_per_mtok: 1.25
    latency_ms_p50: 900
`)
	if err != nil {
		t.Fatalf("Load: want catalog to load, got %v", err)
	}
}

func TestModelCatalogRejectsUnconfiguredProvider(t *testing.T) {
	err := loadConfig(t, baseConfig+`
model_catalog:
  - provider: "nope"
    model: "whatever"
    input_cost_per_mtok: 1
`)
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Load: want unconfigured-provider error, got %v", err)
	}
}

func TestModelCatalogRejectsUndeclaredModel(t *testing.T) {
	err := loadConfig(t, baseConfig+`
model_catalog:
  - provider: "claude"
    model: "not-a-declared-model"
    input_cost_per_mtok: 1
`)
	if err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("Load: want undeclared-model error, got %v", err)
	}
}

func TestAliasGroupAcceptsCostSelectStrategies(t *testing.T) {
	for _, sel := range []string{"cheapest_input", "cheapest_output", "fastest"} {
		err := loadConfig(t, baseConfig+`
aliases:
  budget:
    type: "group"
    select: "`+sel+`"
    members:
      - provider: "claude"
        model: "claude-3-haiku"
`)
		if err != nil {
			t.Errorf("Load with select %q: want success, got %v", sel, err)
		}
	}
}

func TestAliasGroupRejectsUnknownSelect(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  budget:
    type: "group"
    select: "bogus"
    members:
      - provider: "claude"
        model: "claude-3-haiku"
`)
	if err == nil || !strings.Contains(err.Error(), "unknown select") {
		t.Fatalf("Load: want unknown-select error, got %v", err)
	}
}
