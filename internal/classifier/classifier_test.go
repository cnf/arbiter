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
	llm := NewLLMClassifier("domain-llm", AxisDomain, pinnedResolver(), "classify", u, testProviders(), bareLabels, "", "", fakeHeuristic{domain: "chat"}, 0)
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

// TestMergedClassifierZeroConfidenceFillsNoAxis pins a behaviour that is easy
// to trip over: the merge only fills an axis when the score is strictly above
// zero, so a verdict reported at confidence 0.00 fills NOTHING.
//
// That is the right default — "no confidence" and "no signal" should look the
// same, or a classifier that is completely unsure would overwrite a better
// answer — but it means a decisions model answering a question with a 0.00
// probability produces no axis value at all rather than a value at zero
// confidence. A zero-probability choice is not expected in practice (a
// distribution is peaked on something), which is exactly why it is worth
// pinning rather than discovering later.
func TestMergedClassifierZeroConfidenceFillsNoAxis(t *testing.T) {
	zero := fakeAxisClassifier{domain: "chat", confidence: 0}
	merged := NewMergedClassifier("merged", []Classifier{zero})

	sig, err := merged.Classify(context.Background(), req("anything"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "" {
		t.Fatalf("Domain = %q, want empty — a zero-confidence verdict must not fill an axis", sig.Domain)
	}
}

// fakeAxisClassifier reports fixed axis values with fixed confidences, so a
// merge can be tested without an upstream call.
type fakeAxisClassifier struct {
	domain, costClass string
	confidence        float64
	axisConfidence    map[string]float64
}

func (f fakeAxisClassifier) Classify(context.Context, *types.NormalizedRequest) (types.Signals, error) {
	return types.Signals{
		Domain: f.domain, CostClass: f.costClass,
		Confidence: f.confidence, AxisConfidence: f.axisConfidence,
	}, nil
}

// The whole point of AxisConfidence: a classifier that fills two axes from one
// call must be scored on each axis's OWN confidence. Without this, a 0.98
// domain verdict would also win cost_class at 0.98 and beat a legitimate
// 0.70 classifier that actually looked at cost_class.
func TestMergedClassifierScoresEachAxisOnItsOwnConfidence(t *testing.T) {
	// One call, two answers: very sure about domain, much less sure about
	// cost_class.
	multi := fakeAxisClassifier{
		domain: "code_generation", costClass: "budget",
		confidence:     0.98, // the highest per-axis value
		axisConfidence: map[string]float64{AxisDomain: 0.98, AxisCostClass: 0.61},
	}
	// A dedicated cost_class classifier, less sure overall but surer than the
	// multi-axis call's cost_class answer.
	costOnly := fakeAxisClassifier{costClass: "quality_first", confidence: 0.70}

	merged := NewMergedClassifier("merged", []Classifier{multi, costOnly})
	sig, err := merged.Classify(context.Background(), req("anything"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if sig.Domain != "code_generation" {
		t.Fatalf("Domain = %q, want the multi-axis call's 0.98 verdict", sig.Domain)
	}
	// 0.70 must beat the multi-axis call's 0.61 on that axis — even though the
	// multi-axis call reports 0.98 overall.
	if sig.CostClass != "quality_first" {
		t.Fatalf("CostClass = %q, want quality_first (0.70 must beat 0.61 on that axis, not 0.98)", sig.CostClass)
	}
	// The deciding scores are carried forward so a reader can see how sure each
	// axis's winner was.
	if sig.AxisConfidence[AxisCostClass] != 0.70 {
		t.Fatalf("AxisConfidence[cost_class] = %v, want the winning 0.70", sig.AxisConfidence[AxisCostClass])
	}
}

// A classifier with no per-axis map (every pre-existing type) must be scored on
// its overall Confidence, exactly as it was before AxisConfidence existed.
func TestMergedClassifierFallsBackToOverallConfidence(t *testing.T) {
	weak := fakeAxisClassifier{domain: "chat", confidence: 0.30}
	strong := fakeAxisClassifier{domain: "code_generation", confidence: 0.80}

	merged := NewMergedClassifier("merged", []Classifier{weak, strong})
	sig, err := merged.Classify(context.Background(), req("anything"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "code_generation" {
		t.Fatalf("Domain = %q, want the higher overall confidence to win", sig.Domain)
	}
}

// A merge of one-axis classifiers must not grow an AxisConfidence map — the
// field is additive, and populating it where nothing needed it would be a
// silent change to what every existing classifier reports.
func TestMergedClassifierLeavesAxisConfidenceNilWhenUnused(t *testing.T) {
	a := fakeAxisClassifier{domain: "chat", confidence: 0.5}
	b := fakeAxisClassifier{costClass: "budget", confidence: 0.5}

	merged := NewMergedClassifier("merged", []Classifier{a, b})
	sig, err := merged.Classify(context.Background(), req("anything"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.AxisConfidence != nil {
		t.Fatalf("AxisConfidence = %v, want nil when no classifier reported per-axis values", sig.AxisConfidence)
	}
}
