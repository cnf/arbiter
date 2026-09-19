package classifier

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/router"
	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// fakeDecisionClient is a scriptable upstream.DecisionClient. A route present
// in errs fails; otherwise responses supplies the body. calls records every
// attempted provider, in order, so a test can assert the group-fallback walk.
type fakeDecisionClient struct {
	responses map[string]*types.DecisionResponse
	errs      map[string]error
	calls     []string
	requests  []*types.DecisionRequest
}

func (f *fakeDecisionClient) Decide(ctx context.Context, route types.Route, req *types.DecisionRequest) (*types.DecisionResponse, error) {
	f.calls = append(f.calls, route.Provider)
	f.requests = append(f.requests, req)
	if err, ok := f.errs[route.Provider]; ok {
		return nil, err
	}
	if resp, ok := f.responses[route.Provider]; ok {
		return resp, nil
	}
	return &types.DecisionResponse{}, nil
}

// decisionReply builds an answers body for the named questions.
func decisionReply(answers map[string]types.DecisionAnswer) *types.DecisionResponse {
	return &types.DecisionResponse{
		Model:   "jev-1.13.0",
		Answers: answers,
		Raw:     `{"model":"jev-1.13.0"}`,
	}
}

func choice(name string, confidence float64, probs map[string]float64) types.DecisionAnswer {
	return types.DecisionAnswer{Type: types.DecisionChoice, Choice: name, Confidence: confidence, Probabilities: probs}
}

// decisionsProvider is a provider that speaks the decisions protocol: its
// endpoint is the complete URL, not an API root.
func decisionsProvider(name string) types.ProviderConfig {
	return types.ProviderConfig{
		Name: name, Type: "decisions",
		Endpoint: "https://openrouter.ai/api/alpha/decisions",
		APIKey:   "test-key", Models: []string{"~typesafe/jev-latest"},
	}
}

func decisionsResolver(alias, provider string) *router.AliasResolver {
	providers := map[string]types.ProviderConfig{provider: decisionsProvider(provider)}
	aliases := map[string]router.Alias{
		alias: {Name: alias, Type: "pinned", Provider: provider, Model: "~typesafe/jev-latest"},
	}
	return router.NewAliasResolver(aliases, providers, nil, nil)
}

func domainLabels() []types.Label {
	return []types.Label{
		{Name: "code_generation", Description: "wants code written"},
		{Name: "chat", Description: "small talk"},
		{Name: "none", Description: "nothing fits"},
	}
}

const testProvider = "or-decisions"

// newTestDecisionsClassifier builds a one-question classifier, which is what
// most tests want.
func newTestDecisionsClassifier(t *testing.T, client *fakeDecisionClient) *DecisionsClassifier {
	t.Helper()
	return NewDecisionsClassifier(
		"domain-decisions", decisionsResolver("jev", testProvider), "jev",
		client, map[string]types.ProviderConfig{testProvider: decisionsProvider(testProvider)},
		[]DecisionQuestionConfig{{
			Name: "domain", Axis: AxisDomain, Type: types.DecisionChoice,
			Labels: domainLabels(), Escape: "none", Instructions: "Pick the category.",
		}},
		NewHeuristicClassifier("fb", AxisDomain, nil), 5*time.Second,
	)
}

// twoQuestionClassifier is the multi-axis shape Phase B exists for.
func twoQuestionClassifier(client *fakeDecisionClient) *DecisionsClassifier {
	return NewDecisionsClassifier(
		"multi", decisionsResolver("jev", testProvider), "jev",
		client, map[string]types.ProviderConfig{testProvider: decisionsProvider(testProvider)},
		[]DecisionQuestionConfig{
			{Name: "domain", Axis: AxisDomain, Type: types.DecisionChoice, Labels: domainLabels(), Escape: "none"},
			{Name: "cost_class", Axis: AxisCostClass, Type: types.DecisionChoice, Labels: []types.Label{{Name: "budget"}, {Name: "quality_first"}}},
		},
		NewHeuristicClassifier("fb", AxisDomain, nil), 5*time.Second,
	)
}

func testRequest() *types.NormalizedRequest {
	return &types.NormalizedRequest{
		Messages: []types.Message{{Role: "user", Content: []types.ContentBlock{types.TextBlock("please refactor this")}}},
	}
}

// The choice is the whole point of the primitive: it is constrained to the
// options we sent, so the label fills the axis with a real confidence rather
// than the 1.0 an unparseable-reply parser has to assume.
func TestDecisionsClassifierFillsAxisWithModelConfidence(t *testing.T) {
	client := &fakeDecisionClient{responses: map[string]*types.DecisionResponse{
		testProvider: decisionReply(map[string]types.DecisionAnswer{
			"domain": choice("code_generation", 0.92, map[string]float64{"code_generation": 0.92, "chat": 0.08}),
		}),
	}}
	c := newTestDecisionsClassifier(t, client)

	sig, err := c.Classify(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "code_generation" {
		t.Fatalf("Domain = %q, want code_generation", sig.Domain)
	}
	if sig.Confidence != 0.92 {
		t.Fatalf("Confidence = %v, want the model's own 0.92", sig.Confidence)
	}
	if got := sig.AxisConfidence[AxisDomain]; got != 0.92 {
		t.Fatalf("AxisConfidence[domain] = %v, want 0.92", got)
	}
	if len(sig.ClassifierCalls) != 1 {
		t.Fatalf("ClassifierCalls = %d, want 1", len(sig.ClassifierCalls))
	}
}

// The escape label is sent under ITS OWN NAME and choosing it must fill NO axis
// — so a policy router's rules simply do not match and a chained router takes
// over. This is the decisions equivalent of the LLM classifier's escape
// verdict, and it is the assertion most likely to regress silently.
func TestDecisionsClassifierEscapeFillsNoAxis(t *testing.T) {
	client := &fakeDecisionClient{responses: map[string]*types.DecisionResponse{
		testProvider: decisionReply(map[string]types.DecisionAnswer{
			"domain": choice("none", 0.88, map[string]float64{"none": 0.88, "chat": 0.12}),
		}),
	}}
	c := newTestDecisionsClassifier(t, client)

	sig, err := c.Classify(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "" {
		t.Fatalf("Domain = %q, want empty (escape fills no axis)", sig.Domain)
	}
	// Still a recorded, successful call: a confident "nothing fits" is a real
	// judgement, not a failure.
	if len(sig.ClassifierCalls) != 1 {
		t.Fatalf("ClassifierCalls = %d, want 1 (escape is a successful verdict)", len(sig.ClassifierCalls))
	}
	if sig.ClassifierCalls[0].Error != "" {
		t.Fatalf("escape call recorded an error: %q", sig.ClassifierCalls[0].Error)
	}
}

// The criteria sent must carry every configured label, and the escape label
// must appear under ITS OWN NAME — not duplicated as a second `other` option.
//
// An earlier version emitted both, so a config declaring `none` was sent `none`
// (the operator's rubric) AND `other` ("none of the above apply") — two options
// meaning the same thing with different wording, and a reply of one filled the
// axis while a reply of the other did not. That is the bug this pins.
func TestDecisionsClassifierSendsEscapeUnderItsOwnName(t *testing.T) {
	client := &fakeDecisionClient{responses: map[string]*types.DecisionResponse{
		testProvider: decisionReply(map[string]types.DecisionAnswer{"domain": choice("chat", 0.7, nil)}),
	}}
	c := newTestDecisionsClassifier(t, client)

	if _, err := c.Classify(context.Background(), testRequest()); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(client.requests))
	}
	q, ok := client.requests[0].Questions["domain"]
	if !ok {
		t.Fatal("no question named after the axis was sent")
	}
	if q.Type != types.DecisionChoice {
		t.Fatalf("question type = %q, want choice", q.Type)
	}
	criteria, ok := q.Criteria.(map[string]string)
	if !ok {
		t.Fatalf("criteria is %T, want map[string]string", q.Criteria)
	}
	if _, ok := criteria["other"]; ok {
		t.Fatal("criteria carries `other` alongside a configured escape label — the escape is duplicated")
	}
	if _, ok := criteria["none"]; !ok {
		t.Fatal("the configured escape label `none` is missing from the criteria")
	}
	if criteria["code_generation"] != "wants code written" {
		t.Fatalf("criteria lost the rubric description: %v", criteria["code_generation"])
	}
	// The state is the last user message, byte-identical to what the llm
	// classifier classifies, so the two types are comparable on real traffic.
	if state, _ := client.requests[0].State.(string); state != "please refactor this" {
		t.Fatalf("State = %q, want the last user message", state)
	}
}

// With NO escape configured, `other` is added: a config that names no escape
// still needs a way for the model to say "none of these fit", or it is forced
// into the closest listed option. And a literal "other" reply is then the
// escape verdict, since it is the option we sent for exactly that purpose.
func TestDecisionsClassifierAddsOtherWhenNoEscapeConfigured(t *testing.T) {
	client := &fakeDecisionClient{responses: map[string]*types.DecisionResponse{
		testProvider: decisionReply(map[string]types.DecisionAnswer{"domain": choice("other", 0.7, nil)}),
	}}
	c := NewDecisionsClassifier(
		"domain-decisions", decisionsResolver("jev", testProvider), "jev",
		client, map[string]types.ProviderConfig{testProvider: decisionsProvider(testProvider)},
		[]DecisionQuestionConfig{{
			Name: "domain", Axis: AxisDomain, Type: types.DecisionChoice,
			Labels: domainLabels(), // Escape deliberately empty
		}},
		NewHeuristicClassifier("fb", AxisDomain, nil), 5*time.Second,
	)

	sig, err := c.Classify(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	criteria, _ := client.requests[0].Questions["domain"].Criteria.(map[string]string)
	if _, ok := criteria["other"]; !ok {
		t.Fatal("no `other` option added for a config with no escape label")
	}
	if sig.Domain != "" {
		t.Fatalf("Domain = %q, want empty — `other` is the escape verdict when no escape label is configured", sig.Domain)
	}
	if sig.ClassifierCalls[0].Error != "" {
		t.Fatalf("the `other` escape was recorded as a failure: %q", sig.ClassifierCalls[0].Error)
	}
}

// A literal "other" must NOT be treated as escape when an escape label is
// configured: the operator named their own option, and accepting a synonym
// would make the axis's emptiness depend on which word the model picked.
func TestDecisionsClassifierOtherIsNotEscapeWhenEscapeConfigured(t *testing.T) {
	client := &fakeDecisionClient{responses: map[string]*types.DecisionResponse{
		testProvider: decisionReply(map[string]types.DecisionAnswer{"domain": choice("other", 0.9, nil)}),
	}}
	c := newTestDecisionsClassifier(t, client) // escape = "none"
	c.fallback = NewHeuristicClassifier("fb", AxisDomain, map[string][]string{"chat": {"refactor"}})

	sig, err := c.Classify(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	// `other` is not a configured option here, so the question is dropped and
	// the fallback supplies the axis — NOT silently read as the escape verdict.
	if sig.Domain != "chat" {
		t.Fatalf("Domain = %q, want the fallback's answer (a stray `other` is not the escape)", sig.Domain)
	}
	if sig.ClassifierCalls[0].Error == "" {
		t.Fatal("a stray `other` reply was not recorded as a dropped question")
	}
}

// Several axes, ONE upstream call. This is the whole reason a decision model
// maps onto Arbiter's multi-axis Signals, and the assertion that would catch a
// regression back to one call per axis.
func TestDecisionsClassifierAsksEveryQuestionInOneCall(t *testing.T) {
	client := &fakeDecisionClient{responses: map[string]*types.DecisionResponse{
		testProvider: decisionReply(map[string]types.DecisionAnswer{
			"domain":     choice("code_generation", 0.98, nil),
			"cost_class": choice("budget", 0.61, nil),
		}),
	}}
	c := twoQuestionClassifier(client)

	sig, err := c.Classify(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("upstream calls = %d, want 1 for two questions", len(client.calls))
	}
	if got := len(client.requests[0].Questions); got != 2 {
		t.Fatalf("questions sent = %d, want 2", got)
	}
	if sig.Domain != "code_generation" || sig.CostClass != "budget" {
		t.Fatalf("axes = (%q, %q), want (code_generation, budget)", sig.Domain, sig.CostClass)
	}
	// The whole point of AxisConfidence: one call, two different confidences.
	if sig.AxisConfidence[AxisDomain] != 0.98 || sig.AxisConfidence[AxisCostClass] != 0.61 {
		t.Fatalf("AxisConfidence = %v, want domain 0.98 and cost_class 0.61", sig.AxisConfidence)
	}
	// Overall Confidence is the most certain thing the call concluded — not the
	// cost_class verdict's 0.61, and not a fabricated 1.0.
	if sig.Confidence != 0.98 {
		t.Fatalf("Confidence = %v, want the highest per-axis value 0.98", sig.Confidence)
	}
}

// A question the model did not answer must not sink the axes it did answer:
// MergedClassifier resolves each axis independently, so a partially-answered
// call is strictly better than the heuristic for the axes it got.
func TestDecisionsClassifierPartialAnswerKeepsAnsweredAxes(t *testing.T) {
	client := &fakeDecisionClient{responses: map[string]*types.DecisionResponse{
		testProvider: decisionReply(map[string]types.DecisionAnswer{
			"domain": choice("code_generation", 0.9, nil),
		}),
	}}
	c := twoQuestionClassifier(client)

	sig, err := c.Classify(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "code_generation" {
		t.Fatalf("Domain = %q, want the answered axis kept", sig.Domain)
	}
	if sig.CostClass != "" {
		t.Fatalf("CostClass = %q, want empty (unanswered, not fabricated)", sig.CostClass)
	}
	if sig.ClassifierCalls[0].Error == "" {
		t.Fatal("a dropped question was not recorded on the call")
	}
	if !strings.Contains(sig.ClassifierCalls[0].Error, "cost_class") {
		t.Fatalf("the dropped question is not named: %q", sig.ClassifierCalls[0].Error)
	}
}

// A choice outside the option set should be impossible (the primitive is
// constrained to the criteria we send). A breach drops that question rather
// than acting on an unknown label.
func TestDecisionsClassifierOffListChoiceDropsQuestion(t *testing.T) {
	client := &fakeDecisionClient{responses: map[string]*types.DecisionResponse{
		testProvider: decisionReply(map[string]types.DecisionAnswer{"domain": choice("banana", 0.9, nil)}),
	}}
	c := newTestDecisionsClassifier(t, client)
	// A fallback that fills the axis, so "fell back" is observable.
	c.fallback = NewHeuristicClassifier("fb", AxisDomain, map[string][]string{"chat": {"refactor"}})

	sig, err := c.Classify(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "chat" {
		t.Fatalf("Domain = %q, want the fallback's answer (chat)", sig.Domain)
	}
	if len(sig.ClassifierCalls) != 1 || sig.ClassifierCalls[0].Error == "" {
		t.Fatalf("off-list choice was not recorded as a failure: %+v", sig.ClassifierCalls)
	}
}

// A transport failure must fall back, and the attempt must still be recorded —
// MergedClassifier aborts the whole merge on a sub-classifier error, so a
// classification failure must never become a request failure.
func TestDecisionsClassifierUpstreamErrorFallsBack(t *testing.T) {
	client := &fakeDecisionClient{errs: map[string]error{
		testProvider: arbitererrors.NewUpstreamError(testProvider, 502, "boom", errors.New("dial")),
	}}
	c := newTestDecisionsClassifier(t, client)
	c.fallback = NewHeuristicClassifier("fb", AxisDomain, map[string][]string{"chat": {"refactor"}})

	sig, err := c.Classify(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Classify must not return an error, got %v", err)
	}
	if sig.Domain != "chat" {
		t.Fatalf("Domain = %q, want the fallback's answer", sig.Domain)
	}
	if len(sig.ClassifierCalls) != 1 {
		t.Fatalf("ClassifierCalls = %d, want the failed attempt recorded", len(sig.ClassifierCalls))
	}
	if got := sig.ClassifierCalls[0].StatusCode; got != 502 {
		t.Fatalf("StatusCode = %d, want the upstream's own 502", got)
	}
	// The prompt and input ride on a FAILED call too: without them, "the
	// endpoint was down" and "the rubric is ambiguous" are indistinguishable.
	if sig.ClassifierCalls[0].Input == "" || sig.ClassifierCalls[0].SystemPrompt == "" {
		t.Fatal("failed call lost its input or question record")
	}
}

// The rationale must name every axis with its probability, not just the winner:
// the probability is the entire reason for using a decision model, and it is
// what makes a low-confidence verdict readable as uncertain rather than wrong.
func TestDecisionsClassifierVerdictNamesEveryAxisAndProbability(t *testing.T) {
	client := &fakeDecisionClient{responses: map[string]*types.DecisionResponse{
		testProvider: decisionReply(map[string]types.DecisionAnswer{
			"domain":     choice("code_generation", 0.92, nil),
			"cost_class": choice("budget", 0.61, nil),
		}),
	}}
	c := twoQuestionClassifier(client)

	sig, err := c.Classify(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	verdict := sig.ClassifierCalls[0].Verdict
	for _, want := range []string{"domain=", "code_generation", "0.92", "cost_class=", "budget", "0.61"} {
		if !strings.Contains(verdict, want) {
			t.Fatalf("Verdict %q does not mention %q", verdict, want)
		}
	}
}

// An escape verdict must read as a decision in the rationale, not as an empty
// string — otherwise "the model said nothing fits" and "we failed to fill the
// axis" look identical to whoever reads the request list.
func TestDecisionsClassifierEscapeIsNamedInTheRationale(t *testing.T) {
	client := &fakeDecisionClient{responses: map[string]*types.DecisionResponse{
		testProvider: decisionReply(map[string]types.DecisionAnswer{"domain": choice("none", 0.88, nil)}),
	}}
	c := newTestDecisionsClassifier(t, client)

	sig, err := c.Classify(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !strings.Contains(sig.ClassifierCalls[0].Verdict, "none of the options fit") {
		t.Fatalf("escape verdict not named in the rationale: %q", sig.ClassifierCalls[0].Verdict)
	}
}

// The recorded prompt must be byte-stable across calls, or one call's evidence
// cannot be diffed against the next. Go map iteration is randomized, so both
// the question set and each question's criteria are real hazards here.
func TestDecisionsClassifierPromptIsStable(t *testing.T) {
	client := &fakeDecisionClient{responses: map[string]*types.DecisionResponse{
		testProvider: decisionReply(map[string]types.DecisionAnswer{
			"domain":     choice("chat", 0.6, nil),
			"cost_class": choice("budget", 0.6, nil),
		}),
	}}
	c := twoQuestionClassifier(client)

	var first string
	for i := 0; i < 8; i++ {
		sig, err := c.Classify(context.Background(), testRequest())
		if err != nil {
			t.Fatalf("Classify: %v", err)
		}
		got := sig.ClassifierCalls[0].SystemPrompt
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("prompt differs between calls:\nfirst: %q\ngot:   %q", first, got)
		}
	}
}
