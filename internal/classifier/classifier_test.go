package classifier

import (
	"context"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

func req(text string) *types.NormalizedRequest {
	return &types.NormalizedRequest{
		Messages: []types.Message{{Role: "user", Content: []types.ContentBlock{types.TextBlock(text)}}},
	}
}

func TestHeuristicClassifierPicksHighestHitDomain(t *testing.T) {
	hc := NewHeuristicClassifier("domain", "", map[string][]string{
		"code_generation": {"write", "generate", "implement"},
		"chat":            {"hello", "hi"},
	})

	sig, err := hc.Classify(context.Background(), req("please write and implement a function"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "code_generation" {
		t.Fatalf("expected code_generation, got %q", sig.Domain)
	}
	if sig.Confidence <= 0 || sig.Confidence >= 1 {
		t.Fatalf("confidence out of expected (0,1) range: %v", sig.Confidence)
	}
}

func TestHeuristicClassifierZeroHitsZeroConfidence(t *testing.T) {
	hc := NewHeuristicClassifier("domain", "", map[string][]string{
		"chat": {"hello"},
	})
	sig, err := hc.Classify(context.Background(), req("what is the capital of France"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Confidence != 0 || sig.Domain != "" {
		t.Fatalf("expected zero confidence/empty domain, got %+v", sig)
	}
}

func TestHeuristicClassifierEffortAxis(t *testing.T) {
	hc := NewHeuristicClassifier("effort", AxisEffort, map[string][]string{
		"easy": {"quick"},
		"hard": {"complex", "architecture"},
	})

	sig, err := hc.Classify(context.Background(), req("design a complex architecture"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Effort != "hard" {
		t.Fatalf("expected hard, got %q", sig.Effort)
	}
	if sig.Domain != "" {
		t.Fatalf("effort-axis classifier must not fill Domain, got %q", sig.Domain)
	}
}

func TestMergedClassifierUnionsCapabilities(t *testing.T) {
	// Two independent capability-axis instances, as arbiter.yaml might declare
	// for different detector groups — their hits must union, not overwrite.
	vision := NewHeuristicClassifier("vision", AxisCapabilities, map[string][]string{"vision": {"screenshot"}})
	tools := NewHeuristicClassifier("tools", AxisCapabilities, map[string][]string{"tool_use": {"function"}})
	merged := NewMergedClassifier("merged", []Classifier{vision, tools})

	sig, err := merged.Classify(context.Background(), req("call this function using the screenshot"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(sig.RequiredCapabilities) != 2 {
		t.Fatalf("expected both groups unioned, got %+v", sig.RequiredCapabilities)
	}
}

// TestMergedClassifierPerAxisConfidence guards against a merge bug where a
// single global "highest confidence wins" would let a high-confidence domain
// classifier's result also block a lower-confidence effort classifier from
// populating Effort — confidence must be tracked per axis.
func TestMergedClassifierPerAxisConfidence(t *testing.T) {
	// Multiple domain keyword hits -> high confidence on the domain axis.
	domain := NewHeuristicClassifier("domain", "", map[string][]string{
		"code_generation": {"write", "generate", "implement", "refactor"},
	})
	// Single effort keyword hit -> low confidence on the effort axis.
	effort := NewHeuristicClassifier("effort", AxisEffort, map[string][]string{
		"hard": {"complex"},
	})
	merged := NewMergedClassifier("merged", []Classifier{domain, effort})

	sig, err := merged.Classify(context.Background(), req("write generate implement refactor this complex thing"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "code_generation" {
		t.Fatalf("expected code_generation, got %q", sig.Domain)
	}
	if sig.Effort != "hard" {
		t.Fatalf("effort axis starved by higher-confidence domain axis, got %q", sig.Effort)
	}
}

// TestMergedClassifierPropagatesClassifierCalls proves a sub-classifier that
// made its own upstream call (an LLMClassifier) has that call surfaced on
// the merged Signals — this is what lets the pipeline record it, regardless
// of which other classifiers ran alongside it in the same chain.
func TestMergedClassifierPropagatesClassifierCalls(t *testing.T) {
	u := &fakeUpstream{responses: map[string]*types.NormalizedResponse{"primary": reply("code_generation")}}
	llm := NewLLMClassifier("domain-llm", AxisDomain, pinnedResolver(), "classify", u, testProviders(), labels, fakeHeuristic{domain: "chat"}, 0)
	effort := NewHeuristicClassifier("effort", AxisEffort, map[string][]string{"hard": {"complex"}})
	merged := NewMergedClassifier("merged", []Classifier{llm, effort})

	sig, err := merged.Classify(context.Background(), req("a complex request"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "code_generation" {
		t.Errorf("Domain = %q, want code_generation", sig.Domain)
	}
	if sig.Effort != "hard" {
		t.Errorf("Effort = %q, want hard (must survive alongside the LLM classifier)", sig.Effort)
	}
	if len(sig.ClassifierCalls) != 1 || sig.ClassifierCalls[0].Provider != "primary" {
		t.Fatalf("ClassifierCalls = %v, want the LLM classifier's one call propagated", sig.ClassifierCalls)
	}
}
