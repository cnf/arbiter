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

func TestHeuristicClassifierDifficultyAxis(t *testing.T) {
	hc := NewHeuristicClassifier("effort", AxisDifficulty, map[string][]string{
		"easy": {"quick"},
		"hard": {"complex", "architecture"},
	})

	sig, err := hc.Classify(context.Background(), req("design a complex architecture"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Difficulty != "hard" {
		t.Fatalf("expected hard, got %q", sig.Difficulty)
	}
	if sig.Domain != "" {
		t.Fatalf("difficulty-axis classifier must not fill Domain, got %q", sig.Domain)
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

// An initialized-but-empty capability set is the merge's way of saying
// "classification ran and nothing matched". Keeping it non-nil is what lets a
// stored row distinguish that from a request classification never touched.
func TestMergedClassifierNoCapabilitiesIsEmptyNotNil(t *testing.T) {
	hc := NewHeuristicClassifier("domain", AxisDomain, map[string][]string{"chat": {"hello"}})
	merged := NewMergedClassifier("merged", []Classifier{hc})

	sig, err := merged.Classify(context.Background(), req("what is the capital of France"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.RequiredCapabilities == nil {
		t.Fatal("RequiredCapabilities is nil, want an empty non-nil slice: a merge that ran must not record the same value as one that never did")
	}
	if len(sig.RequiredCapabilities) != 0 {
		t.Fatalf("RequiredCapabilities = %v, want empty", sig.RequiredCapabilities)
	}
}

// TestMergedClassifierPerAxisConfidence guards against a merge bug where a
// single global "highest confidence wins" would let a high-confidence domain
// classifier's result also block a lower-confidence difficulty classifier from
// populating Difficulty — confidence must be tracked per axis.
func TestMergedClassifierPerAxisConfidence(t *testing.T) {
	// Multiple domain keyword hits -> high confidence on the domain axis.
	domain := NewHeuristicClassifier("domain", "", map[string][]string{
		"code_generation": {"write", "generate", "implement", "refactor"},
	})
	// Single difficulty keyword hit -> low confidence on the difficulty axis.
	effort := NewHeuristicClassifier("effort", AxisDifficulty, map[string][]string{
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
	if sig.Difficulty != "hard" {
		t.Fatalf("difficulty axis starved by higher-confidence domain axis, got %q", sig.Difficulty)
	}
}

// TestMergedClassifierPropagatesClassifierCalls proves a sub-classifier that
// made its own upstream call (an LLMClassifier) has that call surfaced on
// the merged Signals — this is what lets the pipeline record it, regardless
// of which other classifiers ran alongside it in the same chain.
func TestMergedClassifierPropagatesClassifierCalls(t *testing.T) {
	u := &fakeUpstream{responses: map[string]*types.NormalizedResponse{"primary": reply("code_generation")}}
	llm := NewLLMClassifier("domain-llm", AxisDomain, pinnedResolver(), &Target{Alias: "classify"}, u, testProviders(), bareLabels, "", "", fakeHeuristic{domain: "chat"}, 0)
	effort := NewHeuristicClassifier("effort", AxisDifficulty, map[string][]string{"hard": {"complex"}})
	merged := NewMergedClassifier("merged", []Classifier{llm, effort})

	sig, err := merged.Classify(context.Background(), req("a complex request"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "code_generation" {
		t.Errorf("Domain = %q, want code_generation", sig.Domain)
	}
	if sig.Difficulty != "hard" {
		t.Errorf("Difficulty = %q, want hard (must survive alongside the LLM classifier)", sig.Difficulty)
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

// TestMergedClassifierGatedLLMSkippedWhenAxisSet proves the core of
// only_if_unset: a gated llm classifier after a heuristic that already filled
// the domain axis is NOT called — no upstream call, no cost, no ClassifierCalls
// entry. The fake upstream counts calls, so a skip is provable, not inferred.
func TestMergedClassifierGatedLLMSkippedWhenAxisSet(t *testing.T) {
	u := &fakeUpstream{responses: map[string]*types.NormalizedResponse{"primary": reply("code_generation")}}
	llm := NewLLMClassifierFull("domain-llm", AxisDomain, pinnedResolver(), &Target{Alias: "classify"}, u, testProviders(), bareLabels, "", "", fakeHeuristic{domain: "chat"}, 0, 0, true)
	heuristic := NewHeuristicClassifier("domain-heuristic", "", map[string][]string{"code_generation": {"write"}})

	// Heuristic first (fills domain -> the gated llm must be skipped).
	merged := NewMergedClassifier("merged", []Classifier{heuristic, llm})
	sig, err := merged.Classify(context.Background(), req("please write a function"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "code_generation" {
		t.Fatalf("Domain = %q, want the heuristic's code_generation", sig.Domain)
	}
	if len(u.calls) != 0 {
		t.Fatalf("gated llm classifier made %d upstream calls, want 0 (axis already set)", len(u.calls))
	}
	if len(sig.ClassifierCalls) != 0 {
		t.Fatalf("ClassifierCalls = %v, want none — a skipped classifier leaves no trace", sig.ClassifierCalls)
	}
}

// TestMergedClassifierGatedLLMRunsWhenAxisEmpty is the complement: the gated
// llm classifier fires when the axis is still empty after earlier classifiers
// (e.g. the heuristic didn't match this request).
func TestMergedClassifierGatedLLMRunsWhenAxisEmpty(t *testing.T) {
	u := &fakeUpstream{responses: map[string]*types.NormalizedResponse{"primary": reply("code_generation")}}
	llm := NewLLMClassifierFull("domain-llm", AxisDomain, pinnedResolver(), &Target{Alias: "classify"}, u, testProviders(), bareLabels, "", "", fakeHeuristic{domain: "chat"}, 0, 0, true)
	heuristic := NewHeuristicClassifier("domain-heuristic", "", map[string][]string{"code_generation": {"write"}})

	// Heuristic first but it does NOT match (no "write") -> axis stays empty,
	// so the gated llm must run.
	merged := NewMergedClassifier("merged", []Classifier{heuristic, llm})
	sig, err := merged.Classify(context.Background(), req("what is the capital of France"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "code_generation" {
		t.Fatalf("Domain = %q, want the gated llm's code_generation", sig.Domain)
	}
	if len(u.calls) != 1 {
		t.Fatalf("gated llm classifier made %d upstream calls, want 1 (axis was empty)", len(u.calls))
	}
	if len(sig.ClassifierCalls) != 1 {
		t.Fatalf("ClassifierCalls = %v, want the one llm call", sig.ClassifierCalls)
	}
}

// TestMergedClassifierGatedRunsWhenOnlyOtherAxisSet proves gating is per-axis:
// a gated domain classifier must still run when an earlier classifier filled
// only the difficulty axis — domain is still empty, so the call is justified.
func TestMergedClassifierGatedRunsWhenOnlyOtherAxisSet(t *testing.T) {
	u := &fakeUpstream{responses: map[string]*types.NormalizedResponse{"primary": reply("code_generation")}}
	llm := NewLLMClassifierFull("domain-llm", AxisDomain, pinnedResolver(), &Target{Alias: "classify"}, u, testProviders(), bareLabels, "", "", fakeHeuristic{domain: "chat"}, 0, 0, true)
	effort := NewHeuristicClassifier("effort", AxisDifficulty, map[string][]string{"hard": {"complex"}})

	merged := NewMergedClassifier("merged", []Classifier{effort, llm})
	sig, err := merged.Classify(context.Background(), req("a complex design"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "code_generation" {
		t.Fatalf("Domain = %q, want the gated llm (domain still empty)", sig.Domain)
	}
	if sig.Difficulty != "hard" {
		t.Fatalf("Difficulty = %q, want hard — the difficulty axis was set independently", sig.Difficulty)
	}
	if len(u.calls) != 1 {
		t.Fatalf("gated llm made %d calls, want 1 (only difficulty was set, not domain)", len(u.calls))
	}
}

// TestMergedClassifierGatedDecisionsSkippedWhenAllAxesSet proves a gated
// decisions classifier (filling several axes from one call) is skipped only
// when EVERY axis it could answer is already set.
func TestMergedClassifierGatedDecisionsSkippedWhenAllAxesSet(t *testing.T) {
	client := &fakeDecisionClient{responses: map[string]*types.DecisionResponse{
		testProvider: decisionReply(map[string]types.DecisionAnswer{"domain": choice("code_generation", 0.9, nil)}),
	}}
	dc := NewDecisionsClassifierFull(
		"domain-decisions", decisionsResolver("jev", testProvider), &Target{Alias: "jev"},
		client, map[string]types.ProviderConfig{testProvider: decisionsProvider(testProvider)},
		[]DecisionQuestionConfig{{Name: "domain", Axis: AxisDomain, Type: types.DecisionChoice, Labels: domainLabels(), Escape: "none"}},
		fakeHeuristic{domain: "chat"}, 0, 0, true,
	)
	// A heuristic fills domain first -> the gated decisions classifier must be
	// skipped (its only axis is already set).
	heuristic := NewHeuristicClassifier("domain-heuristic", "", map[string][]string{"code_generation": {"write"}})
	merged := NewMergedClassifier("merged", []Classifier{heuristic, dc})
	sig, err := merged.Classify(context.Background(), req("please write a function"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "code_generation" {
		t.Fatalf("Domain = %q, want the heuristic's code_generation", sig.Domain)
	}
	if len(client.calls) != 0 {
		t.Fatalf("gated decisions classifier made %d calls, want 0 (axis already set)", len(client.calls))
	}
}

// TestMergedClassifierUngatedStillRuns proves the flag is opt-in: without
// only_if_unset, an llm classifier still runs even when an earlier classifier
// filled the same axis (the historical behavior preserved).
func TestMergedClassifierUngatedStillRuns(t *testing.T) {
	u := &fakeUpstream{responses: map[string]*types.NormalizedResponse{"primary": reply("code_generation")}}
	llm := NewLLMClassifier("domain-llm", AxisDomain, pinnedResolver(), &Target{Alias: "classify"}, u, testProviders(), bareLabels, "", "", fakeHeuristic{domain: "chat"}, 0)
	heuristic := NewHeuristicClassifier("domain-heuristic", "", map[string][]string{"code_generation": {"write"}})

	merged := NewMergedClassifier("merged", []Classifier{heuristic, llm})
	sig, err := merged.Classify(context.Background(), req("please write a function"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(u.calls) != 1 {
		t.Fatalf("ungated llm classifier made %d calls, want 1 (historical behavior: it always runs)", len(u.calls))
	}
	if sig.Domain != "code_generation" {
		t.Fatalf("Domain = %q, want code_generation (both ran; heuristic filled it, llm confirmed)", sig.Domain)
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

// TestMergedClassifierRealValueBeatsUnmatchedRegardlessOfConfidence is the
// core #43 merge rule: an escape verdict (types.UnmatchedValue) must never
// outrank a real value, even when the escape call reported HIGHER confidence
// than the real one. A plain highest-confidence-wins rule would get this
// backwards — "I'm 90% sure nothing fits" must still lose to "I'm 60% sure
// it's code_generation".
func TestMergedClassifierRealValueBeatsUnmatchedRegardlessOfConfidence(t *testing.T) {
	unmatched := fakeAxisClassifier{domain: types.UnmatchedValue, confidence: 0.9}
	real := fakeAxisClassifier{domain: "code_generation", confidence: 0.6}

	merged := NewMergedClassifier("merged", []Classifier{unmatched, real})
	sig, err := merged.Classify(context.Background(), req("anything"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "code_generation" {
		t.Fatalf("Domain = %q, want code_generation — a real value must beat a higher-confidence unmatched verdict", sig.Domain)
	}
}

// The declared-order complement: a real value recorded FIRST must not be
// displaced by a later, higher-confidence escape verdict either — unmatched
// only ever fills an axis that is still completely empty.
func TestMergedClassifierUnmatchedNeverDisplacesEarlierRealValue(t *testing.T) {
	real := fakeAxisClassifier{domain: "code_generation", confidence: 0.4}
	unmatched := fakeAxisClassifier{domain: types.UnmatchedValue, confidence: 0.95}

	merged := NewMergedClassifier("merged", []Classifier{real, unmatched})
	sig, err := merged.Classify(context.Background(), req("anything"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "code_generation" {
		t.Fatalf("Domain = %q, want code_generation — a later unmatched verdict must not displace an earlier real value", sig.Domain)
	}
}

// TestMergedClassifierUnmatchedFillsOnlyTrulyEmptyAxis proves the other half:
// when NO classifier produces a real value, the escape sentinel does fill the
// axis — an unmatched verdict is a real, visible outcome, not silence.
func TestMergedClassifierUnmatchedFillsOnlyTrulyEmptyAxis(t *testing.T) {
	unmatched := fakeAxisClassifier{domain: types.UnmatchedValue, confidence: 0.8}

	merged := NewMergedClassifier("merged", []Classifier{unmatched})
	sig, err := merged.Classify(context.Background(), req("anything"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != types.UnmatchedValue {
		t.Fatalf("Domain = %q, want %q — with nothing else to fill the axis, the escape verdict must be visible", sig.Domain, types.UnmatchedValue)
	}
}

// TestMergedClassifierGatedRunsWhenOnlyUnmatchedRecorded proves an
// only_if_unset classifier still runs after an earlier classifier's escape
// verdict: "nothing fits" is not an answer that should suppress a later,
// more specific classifier from getting its own shot at the axis.
func TestMergedClassifierGatedRunsWhenOnlyUnmatchedRecorded(t *testing.T) {
	unmatched := fakeAxisClassifier{domain: types.UnmatchedValue, confidence: 0.9}
	u := &fakeUpstream{responses: map[string]*types.NormalizedResponse{"primary": reply("code_generation")}}
	llm := NewLLMClassifierFull("domain-llm", AxisDomain, pinnedResolver(), &Target{Alias: "classify"}, u, testProviders(), bareLabels, "", "", fakeHeuristic{domain: "chat"}, 0, 0, true)

	merged := NewMergedClassifier("merged", []Classifier{unmatched, llm})
	sig, err := merged.Classify(context.Background(), req("please write a function"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(u.calls) != 1 {
		t.Fatalf("gated llm classifier made %d upstream calls, want 1 — an unmatched verdict must not count as \"axis set\"", len(u.calls))
	}
	if sig.Domain != "code_generation" {
		t.Fatalf("Domain = %q, want code_generation (the gated classifier's real answer)", sig.Domain)
	}
}
