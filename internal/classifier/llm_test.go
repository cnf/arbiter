package classifier

import (
	"context"
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

var labels = []string{"code_generation", "chat"}

// TestLLMClassifierSuccessReturnsLabelAndCallInfo proves the happy path:
// the model's reply matches a configured label, Domain is filled with it,
// confidence is 1.0 (a real judgment, not a heuristic guess), and the call
// is recorded with no error.
func TestLLMClassifierSuccessReturnsLabelAndCallInfo(t *testing.T) {
	u := &fakeUpstream{responses: map[string]*types.NormalizedResponse{"primary": reply("code_generation")}}
	c := NewLLMClassifier("t", AxisDomain, pinnedResolver(), "classify", u, testProviders(), labels, fakeHeuristic{domain: "chat"}, time.Second)

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
	c := NewLLMClassifier("t", AxisDomain, pinnedResolver(), "classify", u, testProviders(), labels, fakeHeuristic{domain: "chat"}, time.Second)

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
	c := NewLLMClassifier("t", AxisDomain, pinnedResolver(), "classify", u, testProviders(), labels, fakeHeuristic{domain: "chat"}, time.Second)

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
	c := NewLLMClassifier("t", AxisDomain, groupResolver(), "classify", u, testProviders(), labels, fakeHeuristic{domain: "code_generation"}, time.Second)

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
	c := NewLLMClassifier("t", AxisDomain, pinnedResolver(), "does-not-exist", u, testProviders(), labels, fakeHeuristic{domain: "chat"}, time.Second)

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
