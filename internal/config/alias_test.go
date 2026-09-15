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

// loadConfigWithFile writes lanes.yaml plus a sibling catalog file in the same
// temp dir, so relative resolution of model_catalog_file is exercised.
func loadConfigWithFile(t *testing.T, mainYAML, catalogYAML string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/lanes.yaml", []byte(mainYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(dir+"/catalog.yaml", []byte(catalogYAML), 0o600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	return Load(dir + "/lanes.yaml")
}

func TestModelCatalogFileMergesRows(t *testing.T) {
	cfg, err := loadConfigWithFile(t, baseConfig+`
model_catalog_file: "catalog.yaml"
`, `
model_catalog:
  - provider: "claude"
    model: "claude-3-haiku"
    input_cost_per_mtok: 0.25
    output_cost_per_mtok: 1.25
    latency_ms_p50: 900
  - provider: "claude"
    model: "claude-3-opus"
    input_cost_per_mtok: 15
    output_cost_per_mtok: 75
    latency_ms_p50: 4000
`)
	if err != nil {
		t.Fatalf("Load: want merge to succeed, got %v", err)
	}
	if len(cfg.ModelCatalog) != 2 {
		t.Fatalf("ModelCatalog has %d rows, want 2 from the file", len(cfg.ModelCatalog))
	}
}

// An inline row for a provider/model the file also declares replaces the file
// row wholesale — the file must not contribute any of its own fields.
func TestModelCatalogInlineRowReplacesFileRow(t *testing.T) {
	cfg, err := loadConfigWithFile(t, baseConfig+`
model_catalog:
  - provider: "claude"
    model: "claude-3-haiku"
    input_cost_per_mtok: 0.30
model_catalog_file: "catalog.yaml"
`, `
model_catalog:
  - provider: "claude"
    model: "claude-3-haiku"
    input_cost_per_mtok: 0.25
    output_cost_per_mtok: 1.25
    latency_ms_p50: 900
  - provider: "claude"
    model: "claude-3-opus"
    input_cost_per_mtok: 15
    output_cost_per_mtok: 75
`)
	if err != nil {
		t.Fatalf("Load: want merge to succeed, got %v", err)
	}
	if len(cfg.ModelCatalog) != 2 {
		t.Fatalf("ModelCatalog has %d rows, want 2 (inline haiku + file opus)", len(cfg.ModelCatalog))
	}

	for _, e := range cfg.ModelCatalog {
		if e.Model != "claude-3-haiku" {
			continue
		}
		// The inline row has no output cost or latency; those must be zero,
		// NOT inherited from the file's row for the same model.
		if e.InputCostPerMTok != 0.30 {
			t.Errorf("haiku input cost = %v, want the inline 0.30", e.InputCostPerMTok)
		}
		if e.OutputCostPerMTok != 0 || e.LatencyMsP50 != 0 {
			t.Errorf("haiku = %+v, want file fields cleared: inline row replaces the whole row", e)
		}
		return
	}
	t.Fatal("no row for claude-3-haiku in merged catalog")
}

// A missing catalog file is a hard error, not a silent skip: it usually means
// the generator hasn't run yet, and silently routing on stale/absent costs is
// the failure mode this feature exists to avoid.
func TestModelCatalogFileMissingIsError(t *testing.T) {
	err := loadConfig(t, baseConfig+`
model_catalog_file: "nope.yaml"
`)
	if err == nil || !strings.Contains(err.Error(), "model_catalog_file") {
		t.Fatalf("Load: want missing-file error, got %v", err)
	}
}

// The file's rows get the same validation as inline rows.
func TestModelCatalogFileRowIsValidated(t *testing.T) {
	_, err := loadConfigWithFile(t, baseConfig+`
model_catalog_file: "catalog.yaml"
`, `
model_catalog:
  - provider: "claude"
    model: "not-a-declared-model"
    input_cost_per_mtok: 1
`)
	if err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("Load: want undeclared-model error from the file's row, got %v", err)
	}
}

func TestModelCatalogRejectsDuplicateRow(t *testing.T) {
	err := loadConfig(t, baseConfig+`
model_catalog:
  - provider: "claude"
    model: "claude-3-haiku"
    input_cost_per_mtok: 0.25
  - provider: "claude"
    model: "claude-3-haiku"
    input_cost_per_mtok: 0.30
`)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Load: want duplicate-row error, got %v", err)
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
