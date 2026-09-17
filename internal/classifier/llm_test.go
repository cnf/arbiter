package classifier

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/router"
	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// fakeUpstream is a scriptable upstream.Client: sends[i] is returned for the
// i-th call (by provider), so a test can make one candidate fail and the
// next succeed. Calls records every attempted provider, in order.
type fakeUpstream struct {
	responses map[string]*types.NormalizedResponse
	errs      map[string]error
	calls     []string
}

func (f *fakeUpstream) Send(ctx context.Context, route types.Route, req *types.NormalizedRequest) (*types.NormalizedResponse, error) {
	f.calls = append(f.calls, route.Provider)
	if err, ok := f.errs[route.Provider]; ok {
		return nil, err
	}
	if resp, ok := f.responses[route.Provider]; ok {
		return resp, nil
	}
	return &types.NormalizedResponse{}, nil
}

func (f *fakeUpstream) SendStream(ctx context.Context, route types.Route, req *types.NormalizedRequest) (<-chan *types.NormalizedStreamEvent, <-chan error, error) {
	panic("not used by LLMClassifier")
}

func reply(text string) *types.NormalizedResponse {
	return &types.NormalizedResponse{Content: []types.ContentBlock{types.TextBlock(text)}}
}

func testProviders() map[string]types.ProviderConfig {
	return map[string]types.ProviderConfig{
		"primary":  {Name: "primary", Type: "openai", Models: []string{"m-primary"}},
		"fallback": {Name: "fallback", Type: "openai", Models: []string{"m-fallback"}},
	}
}

// pinnedResolver resolves "classify" to a single pinned alias with no group
// fallback siblings.
func pinnedResolver() *router.AliasResolver {
	aliases := map[string]router.Alias{
		"classify": {Name: "classify", Type: "pinned", Provider: "primary", Model: "m-primary"},
	}
	return router.NewAliasResolver(aliases, testProviders(), nil, nil)
}

// groupResolver resolves "classify" to a group alias whose first-listed
// member is "primary" and whose sibling is "fallback" — GroupFallbacks
// returns the unselected member as LLMClassifier's own fallback candidate.
func groupResolver() *router.AliasResolver {
	aliases := map[string]router.Alias{
		"classify": {
			Name: "classify", Type: "group",
			Members: []router.AliasMember{
				{Provider: "primary", Model: "m-primary"},
				{Provider: "fallback", Model: "m-fallback"},
			},
		},
	}
	pick := func(members []router.AliasMember, _ string, _ router.CostLatencyLookup) router.AliasMember {
		return members[0] // deterministic: always "primary"
	}
	return router.NewAliasResolver(aliases, testProviders(), pick, nil)
}

// fakeHeuristic is a trivial Classifier standing in for the wrapped fallback
// — always returns the same fixed Signals, so a test can tell whether
// LLMClassifier actually deferred to it.
type fakeHeuristic struct{ domain string }

func (f fakeHeuristic) Classify(context.Context, *types.NormalizedRequest) (types.Signals, error) {
	return types.Signals{Domain: f.domain, Confidence: 0.5}, nil
}

func testReq() *types.NormalizedRequest {
	return &types.NormalizedRequest{Messages: []types.Message{{Role: "user", Content: []types.ContentBlock{types.TextBlock("please fix this bug")}}}}
}

// bareLabels is the original list form: names with no rubric description, so
// every pre-rubric config keeps loading and behaving identically.
var bareLabels = []types.Label{{Name: "code_generation"}, {Name: "chat"}}

// rubricLabels is the map form: the same names, each carrying the description
// that gives the model the category's boundary — the whole point of the rubric.
var rubricLabels = []types.Label{
	{Name: "code_generation", Description: "the user wants code written, modified, refactored, or reviewed."},
	{Name: "chat", Description: "greeting or small talk with no artifact expected."},
}

// TestLLMClassifierSuccessReturnsLabelAndCallInfo proves the happy path:
// the model's reply matches a configured label, Domain is filled with it,
// confidence is 1.0 (a real judgment, not a heuristic guess), and the call
// is recorded with no error.
func TestLLMClassifierSuccessReturnsLabelAndCallInfo(t *testing.T) {
	u := &fakeUpstream{responses: map[string]*types.NormalizedResponse{"primary": reply("code_generation")}}
	c := NewLLMClassifier("t", AxisDomain, pinnedResolver(), "classify", u, testProviders(), bareLabels, "", "", fakeHeuristic{domain: "chat"}, time.Second)

	sig, err := c.Classify(context.Background(), testReq())
	if err != nil {
		t.Fatalf("Classify returned an error: %v (must never happen — see doc comment)", err)
	}
	if sig.Domain != "code_generation" {
		t.Errorf("Domain = %q, want code_generation", sig.Domain)
	}
	if sig.Confidence != 1.0 {
		t.Errorf("Confidence = %v, want 1.0 on a real LLM judgment", sig.Confidence)
	}
	if len(sig.ClassifierCalls) != 1 {
		t.Fatalf("ClassifierCalls = %v, want exactly 1", sig.ClassifierCalls)
	}
	call := sig.ClassifierCalls[0]
	if call.Error != "" || call.Provider != "primary" || call.StatusCode != 200 || call.RawReply != "code_generation" {
		t.Errorf("call = %+v, want a clean success on primary", call)
	}
}

// TestLLMClassifierUpstreamErrorFallsBack proves a failed upstream call
// defers entirely to the wrapped fallback, never returns an error itself,
// and still records the failed attempt's diagnostics.
func TestLLMClassifierUpstreamErrorFallsBack(t *testing.T) {
	u := &fakeUpstream{errs: map[string]error{"primary": arbitererrors.NewUpstreamError("primary", 429, "rate limited", nil)}}
	c := NewLLMClassifier("t", AxisDomain, pinnedResolver(), "classify", u, testProviders(), bareLabels, "", "", fakeHeuristic{domain: "chat"}, time.Second)

	sig, err := c.Classify(context.Background(), testReq())
	if err != nil {
		t.Fatalf("Classify returned an error: %v", err)
	}
	if sig.Domain != "chat" {
		t.Errorf("Domain = %q, want chat (the fallback's answer)", sig.Domain)
	}
	if len(sig.ClassifierCalls) != 1 || sig.ClassifierCalls[0].Error == "" {
		t.Fatalf("ClassifierCalls = %v, want one entry with a recorded error", sig.ClassifierCalls)
	}
	if sig.ClassifierCalls[0].StatusCode != 429 {
		t.Errorf("StatusCode = %d, want 429 (the upstream's own code)", sig.ClassifierCalls[0].StatusCode)
	}
}

// TestLLMClassifierUnparseableReplyFallsBack proves a reply that doesn't
// match any configured label is treated as a failure, not guessed at — same
// "surface ambiguity, never guess" rule the rest of the codebase follows.
func TestLLMClassifierUnparseableReplyFallsBack(t *testing.T) {
	u := &fakeUpstream{responses: map[string]*types.NormalizedResponse{"primary": reply("I'm not sure, maybe coding?")}}
	c := NewLLMClassifier("t", AxisDomain, pinnedResolver(), "classify", u, testProviders(), bareLabels, "", "", fakeHeuristic{domain: "chat"}, time.Second)

	sig, err := c.Classify(context.Background(), testReq())
	if err != nil {
		t.Fatalf("Classify returned an error: %v", err)
	}
	if sig.Domain != "chat" {
		t.Errorf("Domain = %q, want chat (fallback, since the reply matched no label)", sig.Domain)
	}
	if len(sig.ClassifierCalls) != 1 || sig.ClassifierCalls[0].Error == "" {
		t.Fatalf("ClassifierCalls = %v, want the unparseable reply recorded as a failure", sig.ClassifierCalls)
	}
}

// TestLLMClassifierTriesGroupFallbackMember proves a group alias's
// unselected sibling (via GroupFallbacks) is tried when the primary fails,
// succeeding without ever reaching the wrapped heuristic fallback.
func TestLLMClassifierTriesGroupFallbackMember(t *testing.T) {
	u := &fakeUpstream{
		errs:      map[string]error{"primary": arbitererrors.NewUpstreamError("primary", 503, "down", nil)},
		responses: map[string]*types.NormalizedResponse{"fallback": reply("chat")},
	}
	c := NewLLMClassifier("t", AxisDomain, groupResolver(), "classify", u, testProviders(), bareLabels, "", "", fakeHeuristic{domain: "code_generation"}, time.Second)

	sig, err := c.Classify(context.Background(), testReq())
	if err != nil {
		t.Fatalf("Classify returned an error: %v", err)
	}
	if len(u.calls) != 2 || u.calls[0] != "primary" || u.calls[1] != "fallback" {
		t.Fatalf("upstream calls = %v, want [primary fallback]", u.calls)
	}
	if sig.Domain != "chat" {
		t.Errorf("Domain = %q, want chat (the group sibling's answer, not the wrapped heuristic's)", sig.Domain)
	}
}

// TestLLMClassifierUnknownAliasFallsBack proves a misconfigured/unresolvable
// alias degrades to the fallback rather than panicking or erroring — config
// validation is the load-time backstop for this (see config.go), this is
// the runtime one.
func TestLLMClassifierUnknownAliasFallsBack(t *testing.T) {
	u := &fakeUpstream{}
	c := NewLLMClassifier("t", AxisDomain, pinnedResolver(), "does-not-exist", u, testProviders(), bareLabels, "", "", fakeHeuristic{domain: "chat"}, time.Second)

	sig, err := c.Classify(context.Background(), testReq())
	if err != nil {
		t.Fatalf("Classify returned an error: %v", err)
	}
	if sig.Domain != "chat" {
		t.Errorf("Domain = %q, want chat (fallback)", sig.Domain)
	}
	if len(u.calls) != 0 {
		t.Errorf("upstream calls = %v, want none (never reached — the alias doesn't resolve)", u.calls)
	}
}

// TestSystemPromptCarriesRubricAndKeepsReplyContract proves the prompt's three
// parts: the framing sentence, each label with its description, and the reply
// contract last. The descriptions are the point of the feature — a bare label
// gives the model no boundary.
func TestSystemPromptCarriesRubricAndKeepsReplyContract(t *testing.T) {
	c := NewLLMClassifier("t", AxisDomain, pinnedResolver(), "classify", &fakeUpstream{}, testProviders(), rubricLabels, "", "", fakeHeuristic{}, time.Second)
	p := c.systemPrompt()

	if !strings.Contains(p, "the user wants code written, modified, refactored, or reviewed.") {
		t.Errorf("prompt does not carry the code_generation rubric:\n%s", p)
	}
	if !strings.Contains(p, "greeting or small talk with no artifact expected.") {
		t.Errorf("prompt does not carry the chat rubric:\n%s", p)
	}
	if !strings.Contains(p, "code_generation:") {
		t.Errorf("prompt does not render the label with its description:\n%s", p)
	}
	// The reply contract is not overridable, so it must be present verbatim even
	// though instructions was empty here.
	if !strings.Contains(p, "Reply with the single matching word and nothing else") {
		t.Errorf("prompt is missing the fixed reply contract:\n%s", p)
	}
}

// TestSystemPromptIsStableAcrossCalls is the sorting rule: Go map iteration is
// randomized, so an unsorted label set would emit different prompt bytes on
// every call and make the logged prompt impossible to diff. Note this buys no
// prompt cache hit — a prompt this short is below every provider's minimum
// cacheable length — so the property being asserted is stability, not caching.
func TestSystemPromptIsStableAcrossCalls(t *testing.T) {
	// Shuffled input order, as a map iteration would deliver it.
	shuffled := []types.Label{
		{Name: "discovery"}, {Name: "chat"}, {Name: "code_generation"}, {Name: "reasoning"},
	}
	first := NewLLMClassifier("t", AxisDomain, pinnedResolver(), "classify", &fakeUpstream{}, testProviders(), shuffled, "", "", fakeHeuristic{}, time.Second).systemPrompt()
	for i := 0; i < 20; i++ {
		got := NewLLMClassifier("t", AxisDomain, pinnedResolver(), "classify", &fakeUpstream{}, testProviders(), shuffled, "", "", fakeHeuristic{}, time.Second).systemPrompt()
		if got != first {
			t.Fatalf("prompt changed between calls (iteration %d):\n%q\nvs\n%q", i, got, first)
		}
	}
	// And the order is actually sorted, not merely stable.
	if strings.Index(first, "chat") > strings.Index(first, "code_generation") {
		t.Errorf("labels are not sorted in the prompt:\n%s", first)
	}
}

// TestSystemPromptUsesConfiguredInstructions proves `instructions:` replaces the
// default framing while the reply contract survives — the one part a rubric edit
// must not be able to break.
func TestSystemPromptUsesConfiguredInstructions(t *testing.T) {
	c := NewLLMClassifier("t", AxisDomain, pinnedResolver(), "classify", &fakeUpstream{}, testProviders(), bareLabels, "", "Pick the single best category.", fakeHeuristic{}, time.Second)
	p := c.systemPrompt()

	if !strings.Contains(p, "Pick the single best category.") {
		t.Errorf("prompt does not use the configured instructions:\n%s", p)
	}
	if strings.Contains(p, defaultFraming) {
		t.Errorf("prompt still carries the default framing alongside configured instructions:\n%s", p)
	}
	if !strings.Contains(p, "Reply with the single matching word and nothing else") {
		t.Errorf("configured instructions dropped the reply contract:\n%s", p)
	}
}

// TestLLMClassifierEscapeLabelFillsNoAxis is the escape-label behavior: the
// model's "nothing fits" is a successful verdict that fills no axis, so a policy
// router's rules simply do not match rather than the operator having to write a
// rule for a literal "unknown" domain. Confidence stays 1.0 because it IS a real
// judgment, and the call is still recorded.
func TestLLMClassifierEscapeLabelFillsNoAxis(t *testing.T) {
	withEscape := []types.Label{
		{Name: "code_generation"}, {Name: "chat"}, {Name: "none"},
	}
	u := &fakeUpstream{responses: map[string]*types.NormalizedResponse{"primary": reply("none")}}
	c := NewLLMClassifier("t", AxisDomain, pinnedResolver(), "classify", u, testProviders(), withEscape, "none", "", fakeHeuristic{domain: "chat"}, time.Second)

	sig, err := c.Classify(context.Background(), testReq())
	if err != nil {
		t.Fatalf("Classify returned an error: %v", err)
	}
	if sig.Domain != "" {
		t.Errorf("Domain = %q, want empty (the escape verdict fills no axis)", sig.Domain)
	}
	if sig.Confidence != 1.0 {
		t.Errorf("Confidence = %v, want 1.0 (a confident 'nothing fits' is a real judgment)", sig.Confidence)
	}
	if len(sig.ClassifierCalls) != 1 {
		t.Fatalf("ClassifierCalls = %v, want the escape verdict recorded as a call", sig.ClassifierCalls)
	}
	if got := sig.ClassifierCalls[0].Error; got != "" {
		t.Errorf("call error = %q, want empty (escape is a success, not a fallback)", got)
	}
	if got := sig.ClassifierCalls[0].RawReply; got != "none" {
		t.Errorf("RawReply = %q, want the model's own reply preserved", got)
	}
}

// TestLLMClassifierBareNoneIsEscapeWhenConfigured proves a literal "none" reply
// is accepted whenever an escape label is configured, so a rubric that describes
// "nothing fits" without spelling a keyword still yields a successful verdict
// instead of an unparseable one.
func TestLLMClassifierBareNoneIsEscapeWhenConfigured(t *testing.T) {
	withEscape := []types.Label{
		{Name: "code_generation"}, {Name: "chat"}, {Name: "unsure"},
	}
	u := &fakeUpstream{responses: map[string]*types.NormalizedResponse{"primary": reply("none")}}
	c := NewLLMClassifier("t", AxisDomain, pinnedResolver(), "classify", u, testProviders(), withEscape, "unsure", "", fakeHeuristic{domain: "chat"}, time.Second)

	sig, err := c.Classify(context.Background(), testReq())
	if err != nil {
		t.Fatalf("Classify returned an error: %v", err)
	}
	if sig.Domain != "" {
		t.Errorf("Domain = %q, want empty (a bare 'none' is the escape verdict)", sig.Domain)
	}
}

// TestLLMClassifierNoEscapeConfiguredStillFallsBack is the control for the two
// tests above: without an escape label, an off-list reply stays a failed call.
// Otherwise "no escape configured" would silently behave as if every unknown
// reply were a confident verdict.
func TestLLMClassifierNoEscapeConfiguredStillFallsBack(t *testing.T) {
	u := &fakeUpstream{responses: map[string]*types.NormalizedResponse{"primary": reply("none")}}
	c := NewLLMClassifier("t", AxisDomain, pinnedResolver(), "classify", u, testProviders(), bareLabels, "", "", fakeHeuristic{domain: "chat"}, time.Second)

	sig, err := c.Classify(context.Background(), testReq())
	if err != nil {
		t.Fatalf("Classify returned an error: %v", err)
	}
	if sig.Domain != "chat" {
		t.Errorf("Domain = %q, want chat (fallback: no escape label is configured)", sig.Domain)
	}
}
