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
	path := t.TempDir() + "/arbiter.yaml"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := Load(path)
	return err
}

// loadConfigOK writes and loads a config, failing the test on any error, and
// returns the resolved Config.
func loadConfigOK(t *testing.T, contents string) *Config {
	t.Helper()
	path := t.TempDir() + "/arbiter.yaml"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
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

// TestShippedArbiterYAMLLoads keeps the committed arbiter.yaml honest against the
// code that parses it — a rename or validation change that breaks the shipped
// example should fail here rather than only at process start.
func TestShippedArbiterYAMLLoads(t *testing.T) {
	if _, err := Load("../../arbiter.yaml"); err != nil {
		t.Fatalf("Load(../../arbiter.yaml): %v", err)
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
	for _, sel := range []string{"cheapest_input", "cheapest_output", "fastest", "ordered"} {
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

// loadConfigWithFile writes arbiter.yaml plus a sibling catalog file in the same
// temp dir, so relative resolution of model_catalog_file is exercised.
func loadConfigWithFile(t *testing.T, mainYAML, catalogYAML string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/arbiter.yaml", []byte(mainYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(dir+"/catalog.yaml", []byte(catalogYAML), 0o600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	return Load(dir + "/arbiter.yaml")
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

// A generated file is expected to be a superset of what this config declares
// (catalog-convert pulls every model under a litellm_provider, not just the
// ones arbiter.yaml happens to list) — so a file row naming an undeclared
// provider/model is dropped rather than failing config load. The inline block
// gets no such leniency: see TestModelCatalogRejectsUndeclaredModel.
func TestModelCatalogFileRowForUndeclaredModelIsDroppedNotRejected(t *testing.T) {
	cfg, err := loadConfigWithFile(t, baseConfig+`
model_catalog_file: "catalog.yaml"
`, `
model_catalog:
  - provider: "claude"
    model: "claude-3-haiku"
    input_cost_per_mtok: 0.25
  - provider: "claude"
    model: "not-a-declared-model"
    input_cost_per_mtok: 1
  - provider: "not-a-configured-provider"
    model: "whatever"
    input_cost_per_mtok: 1
`)
	if err != nil {
		t.Fatalf("Load: want the undeclared row dropped rather than an error, got %v", err)
	}
	if len(cfg.ModelCatalog) != 1 || cfg.ModelCatalog[0].Model != "claude-3-haiku" {
		t.Fatalf("ModelCatalog = %+v, want only the declared haiku row", cfg.ModelCatalog)
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

// llmClassifierConfig is a minimal valid "llm" classifier plus its heuristic
// fallback and the alias it routes through, appended to baseConfig by the
// tests below (each overrides exactly the field it's testing).
const llmClassifierConfig = `
aliases:
  cheap-classifier:
    type: "pinned"
    provider: "claude"
    model: "claude-3-haiku"
classifiers:
  - name: "domain-heuristic"
    type: "heuristic"
    config:
      keywords: { code_generation: ["write"] }
  - name: "domain-llm"
    type: "llm"
    axis: "domain"
    config:
      alias: "cheap-classifier"
      labels: ["code_generation", "chat"]
      fallback: "domain-heuristic"
`

func TestLLMClassifierLoads(t *testing.T) {
	if err := loadConfig(t, baseConfig+llmClassifierConfig); err != nil {
		t.Fatalf("Load: want a valid llm classifier to load, got %v", err)
	}
}

func TestLLMClassifierRejectsUnknownAlias(t *testing.T) {
	err := loadConfig(t, baseConfig+`
classifiers:
  - name: "domain-heuristic"
    type: "heuristic"
    config:
      keywords: { code_generation: ["write"] }
  - name: "domain-llm"
    type: "llm"
    axis: "domain"
    config:
      alias: "does-not-exist"
      labels: ["code_generation"]
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), "alias") || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Load: want an unconfigured-alias error, got %v", err)
	}
}

func TestLLMClassifierRejectsMissingFallback(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  cheap-classifier:
    type: "pinned"
    provider: "claude"
    model: "claude-3-haiku"
classifiers:
  - name: "domain-llm"
    type: "llm"
    axis: "domain"
    config:
      alias: "cheap-classifier"
      labels: ["code_generation"]
      fallback: "does-not-exist"
`)
	if err == nil || !strings.Contains(err.Error(), "fallback") {
		t.Fatalf("Load: want a missing-fallback error, got %v", err)
	}
}

func TestLLMClassifierRejectsLLMFallback(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  cheap-classifier:
    type: "pinned"
    provider: "claude"
    model: "claude-3-haiku"
classifiers:
  - name: "domain-llm-a"
    type: "llm"
    axis: "domain"
    config:
      alias: "cheap-classifier"
      labels: ["code_generation"]
      fallback: "domain-llm-b"
  - name: "domain-llm-b"
    type: "llm"
    axis: "domain"
    config:
      alias: "cheap-classifier"
      labels: ["code_generation"]
      fallback: "domain-llm-a"
`)
	if err == nil || !strings.Contains(err.Error(), "must not itself be a model-backed classifier") {
		t.Fatalf("Load: want a chained-llm-fallback error, got %v", err)
	}
}

func TestLLMClassifierRejectsEmptyLabels(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  cheap-classifier:
    type: "pinned"
    provider: "claude"
    model: "claude-3-haiku"
classifiers:
  - name: "domain-heuristic"
    type: "heuristic"
    config:
      keywords: { code_generation: ["write"] }
  - name: "domain-llm"
    type: "llm"
    axis: "domain"
    config:
      alias: "cheap-classifier"
      labels: []
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), "labels") {
		t.Fatalf("Load: want an empty-labels error, got %v", err)
	}
}

// llmRubricConfig is the map form of `labels:` — name -> rubric description —
// plus the optional escape and instructions fields.
const llmRubricConfig = `
aliases:
  cheap-classifier:
    type: "pinned"
    provider: "claude"
    model: "claude-3-haiku"
classifiers:
  - name: "domain-heuristic"
    type: "heuristic"
    config:
      keywords: { code_generation: ["write"] }
  - name: "domain-llm"
    type: "llm"
    axis: "domain"
    config:
      alias: "cheap-classifier"
      labels:
        code_generation: "the user wants code written, modified, refactored, or reviewed."
        chat: "greeting or small talk with no artifact expected."
        none: "none of the other categories apply."
      escape: "none"
      instructions: "Pick the category that best describes the request."
      fallback: "domain-heuristic"
`

// TestLLMClassifierLoadsRubricLabels proves the map form of `labels:` is
// accepted, and that escape/instructions load alongside it. The list form is
// covered by TestLLMClassifierLoads, which must keep passing unchanged — the
// map is additive, not a replacement.
func TestLLMClassifierLoadsRubricLabels(t *testing.T) {
	if err := loadConfig(t, baseConfig+llmRubricConfig); err != nil {
		t.Fatalf("Load: want rubric labels to load, got %v", err)
	}
}

// TestLLMClassifierRejectsEscapeNotALabel keeps the escape label honest: it is
// the word the model is told to reply with, so a name the model is never offered
// could only be matched by coincidence.
func TestLLMClassifierRejectsEscapeNotALabel(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  cheap-classifier:
    type: "pinned"
    provider: "claude"
    model: "claude-3-haiku"
classifiers:
  - name: "domain-heuristic"
    type: "heuristic"
    config:
      keywords: { code_generation: ["write"] }
  - name: "domain-llm"
    type: "llm"
    axis: "domain"
    config:
      alias: "cheap-classifier"
      labels: ["code_generation", "chat"]
      escape: "none"
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), "escape") {
		t.Fatalf("Load: want an escape-not-a-label error, got %v", err)
	}
}

// TestLLMClassifierRejectsDuplicateLabel guards the map form's one hazard: two
// keys differing only in case both reach the model as the same word, and the
// reply can then match only one of them.
func TestLLMClassifierRejectsDuplicateLabel(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  cheap-classifier:
    type: "pinned"
    provider: "claude"
    model: "claude-3-haiku"
classifiers:
  - name: "domain-heuristic"
    type: "heuristic"
    config:
      keywords: { code_generation: ["write"] }
  - name: "domain-llm"
    type: "llm"
    axis: "domain"
    config:
      alias: "cheap-classifier"
      labels: ["code_generation", "Code_Generation"]
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("Load: want a duplicate-label error, got %v", err)
	}
}

// TestLLMClassifierRejectsNonStringInstructions keeps a typo'd shape (a list, a
// number) from being silently dropped by the builder — the failure mode where
// validation accepts what construction ignores.
func TestLLMClassifierRejectsNonStringInstructions(t *testing.T) {
	err := loadConfig(t, baseConfig+`
aliases:
  cheap-classifier:
    type: "pinned"
    provider: "claude"
    model: "claude-3-haiku"
classifiers:
  - name: "domain-heuristic"
    type: "heuristic"
    config:
      keywords: { code_generation: ["write"] }
  - name: "domain-llm"
    type: "llm"
    axis: "domain"
    config:
      alias: "cheap-classifier"
      labels: ["code_generation"]
      instructions: ["be careful"]
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), "instructions") {
		t.Fatalf("Load: want an instructions-must-be-a-string error, got %v", err)
	}
}

// TestOrderedIsARegisteredSelectStrategy is the load-bearing test for
// select: "ordered". Without the strategy being registered, config validation
// rejects it outright ("unknown select") — and that is the ONLY way ordered can
// fail, because router.selectMember's `default:` branch already returns
// members[0] for any unrecognized name. A behavior test on the router alone
// therefore cannot distinguish "ordered implemented" from "ordered missing";
// it passes either way. This asserts the part that genuinely gates the feature.
func TestOrderedIsARegisteredSelectStrategy(t *testing.T) {
	if !validSelect("ordered") {
		t.Error("validSelect(\"ordered\") = false, want true — the strategy is not registered, " +
			"so every config using select: \"ordered\" is rejected at load time")
	}
	var found bool
	for _, s := range selectStrategies {
		if s == "ordered" {
			found = true
		}
	}
	if !found {
		t.Errorf("selectStrategies does not contain \"ordered\": %v", selectStrategies)
	}
}
