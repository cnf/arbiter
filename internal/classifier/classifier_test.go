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
