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

func TestHeuristicClassifierPicksHighestHitIntent(t *testing.T) {
	hc := NewHeuristicClassifier("domain", map[string][]string{
		"code_generation": {"write", "generate", "implement"},
		"chat":            {"hello", "hi"},
	})

	sig, err := hc.Classify(context.Background(), req("please write and implement a function"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Intent != "code_generation" {
		t.Fatalf("expected code_generation, got %q", sig.Intent)
	}
	if sig.Confidence <= 0 || sig.Confidence >= 1 {
		t.Fatalf("confidence out of expected (0,1) range: %v", sig.Confidence)
	}
}

func TestHeuristicClassifierZeroHitsZeroConfidence(t *testing.T) {
	hc := NewHeuristicClassifier("domain", map[string][]string{
		"chat": {"hello"},
	})
	sig, err := hc.Classify(context.Background(), req("what is the capital of France"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Confidence != 0 || sig.Intent != "" {
		t.Fatalf("expected zero confidence/empty intent, got %+v", sig)
	}
}

func TestMergedClassifierUnionsCapabilities(t *testing.T) {
	domain := NewHeuristicClassifier("domain", map[string][]string{"code_generation": {"write"}})
	capability := NewHeuristicClassifier("capability", map[string][]string{"vision": {"screenshot"}})
	merged := NewMergedClassifier("merged", []Classifier{domain, capability})

	sig, err := merged.Classify(context.Background(), req("write code from this screenshot"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(sig.RequiredCapabilities) != 2 {
		t.Fatalf("expected both groups unioned, got %+v", sig.RequiredCapabilities)
	}
}
