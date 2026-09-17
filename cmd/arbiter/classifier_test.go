package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cnf/arbiter/internal/config"
	"github.com/cnf/arbiter/internal/logging"
)

// TestBuildPipelineResolvesLLMClassifierFallbackRegardlessOfOrder proves
// buildClassifiers' two-pass build: an "llm" classifier declared *before*
// the heuristic classifier it names as its fallback must still wire up
// correctly — config.Load only checks the reference exists (internal/config's
// own tests cover that), this is what proves buildPipeline can actually
// resolve it without caring about declaration order.
func TestBuildPipelineResolvesLLMClassifierFallbackRegardlessOfOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "arbiter.yaml")
	if err := os.WriteFile(path, []byte(`
providers:
  claude:
    type: "anthropic"
    endpoint: "https://api.anthropic.com"
    models: ["claude-3-haiku"]
routers:
  - name: "primary"
    type: "simple"
    config:
      default_provider: "claude"
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
      labels: ["code_generation", "chat"]
      fallback: "domain-heuristic"
  - name: "domain-heuristic"
    type: "heuristic"
    config:
      keywords: { code_generation: ["write"] }
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if _, err := buildPipeline(cfg, logging.NewStdoutLogger("error"), nil); err != nil {
		t.Fatalf("buildPipeline: %v (the llm classifier's fallback, declared after it, must still resolve)", err)
	}
}
