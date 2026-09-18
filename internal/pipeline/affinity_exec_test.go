package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/classifier"
	"github.com/cnf/arbiter/internal/router"
	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// --- fakes for driving Execute end to end ---

// fakeNormalizer turns the payload into a request whose sole user message is
// the payload text; that's enough to exercise session-key derivation. model,
// when non-empty, is set on the request to exercise the explicit-model
// precedence rule.
type fakeNormalizer struct{ model string }

func (fakeNormalizer) Detect([]byte) (string, error) { return "openai", nil }
func (n fakeNormalizer) ToNormalized(payload []byte, _ string) (*types.NormalizedRequest, error) {
	return &types.NormalizedRequest{
		Model:    n.model,
		Messages: []types.Message{{Role: "user", Content: []types.ContentBlock{types.TextBlock(string(payload))}}},
	}, nil
}

type fakeDenormalizer struct{}

func (fakeDenormalizer) FromNormalized(resp *types.NormalizedResponse, _ string) (interface{}, error) {
	return resp, nil
}

// recordingRouter counts routing invocations so a test can assert an affinity
// hit skipped routing.
type recordingRouter struct {
	calls int
	route types.Route
}

func (r *recordingRouter) Route(context.Context, *types.NormalizedRequest, types.Signals) (types.Route, types.Metadata, error) {
	r.calls++
	return r.route, types.Metadata{}, nil
}

// affinityTestResolver configures "auto" and "manual" as force aliases (force
// nothing, like the README's full-auto example) so these tests can use
// non-literal model names as affinity-pin placeholders without tripping the
// unknown-model rejection — REQUIREMENTS.md §1 only allows a real declared
// model or a configured alias in req.Model, and an arbitrary unconfigured
// string is neither.
func affinityTestResolver() *router.AliasResolver {
	return router.NewAliasResolver(map[string]router.Alias{
		"auto":   {Name: "auto", Force: map[string][]string{}},
		"manual": {Name: "manual", Force: map[string][]string{}},
	}, testProviders(), nil, nil)
}

func newAffinityPipeline(rr *recordingRouter, fu *fakeUpstream, n fakeNormalizer, fallbacks []string, ttl time.Duration) *Pipeline {
	return NewPipeline(
		nil, n, fakeDenormalizer{},
		nil, rr, fu,
		testProviders(), fallbacks, nil, nil, fakeLogger{}, ttl, affinityTestResolver(), nil, nil, nil)
}

func primaryRoute() types.Route {
	return types.Route{Provider: "primary", Model: "m-primary", Config: testProviders()["primary"]}
}

func TestAffinityPinRecordedAndReused(t *testing.T) {
	rr := &recordingRouter{route: primaryRoute()}
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	n := fakeNormalizer{model: "auto"}
	p := newAffinityPipeline(rr, fu, n, nil, time.Minute)

	msg := []byte("explain how the custom parser handles nesting")

	// Turn 1: no pin, routing runs, pin recorded against the served provider.
	if _, err := p.Execute(context.Background(), msg, "openai", "t1", ""); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if rr.calls != 1 {
		t.Fatalf("routing calls after turn 1 = %d, want 1", rr.calls)
	}

	// Turn 2: same requested model, so the pin applies and routing is skipped.
	if _, err := p.Execute(context.Background(), msg, "openai", "t2", ""); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if rr.calls != 1 {
		t.Fatalf("routing calls after turn 2 = %d, want still 1 (affinity hit must skip routing)", rr.calls)
	}

	if len(fu.calls) != 2 || fu.calls[0] != "primary" || fu.calls[1] != "primary" {
		t.Fatalf("upstream calls = %v, want [primary primary]", fu.calls)
	}
}

func TestAffinityPinFromHeaderWinsOverContent(t *testing.T) {
	rr := &recordingRouter{route: primaryRoute()}
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	n := fakeNormalizer{model: "auto"}
	p := newAffinityPipeline(rr, fu, n, nil, time.Minute)

	// Same explicit session key, two different short messages — neither would
	// pass the content gate, so only the header makes them the same session.
	if _, err := p.Execute(context.Background(), []byte("hi"), "openai", "t1", "chat-42"); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if _, err := p.Execute(context.Background(), []byte("bye"), "openai", "t2", "chat-42"); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if rr.calls != 1 {
		t.Fatalf("routing calls = %d, want 1 (header key pinned both turns)", rr.calls)
	}
}

func TestAffinitySkippedWhenNoUsableKey(t *testing.T) {
	rr := &recordingRouter{route: primaryRoute()}
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	n := fakeNormalizer{model: "auto"}
	p := newAffinityPipeline(rr, fu, n, nil, time.Minute)

	// Short opener, no header: no key, so every turn routes fresh.
	for i := 0; i < 3; i++ {
		if _, err := p.Execute(context.Background(), []byte("hi"), "openai", "t1", ""); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}
	if rr.calls != 3 {
		t.Fatalf("routing calls = %d, want 3 (no key must mean no pinning)", rr.calls)
	}
}

// The pin holds for as long as the client keeps requesting the same model;
// requesting a different one discards it and routes fresh.
func TestAffinityDifferentRequestedModelDiscardsPin(t *testing.T) {
	rr := &recordingRouter{route: primaryRoute()}
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	msg := []byte("explain how the custom parser handles nesting")

	// Turn 1 with model "auto": pin recorded to primary under "auto".
	n := fakeNormalizer{model: "auto"}
	p := newAffinityPipeline(rr, fu, n, nil, time.Minute)
	if _, err := p.Execute(context.Background(), msg, "openai", "t1", ""); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if rr.calls != 1 {
		t.Fatalf("routing calls = %d, want 1", rr.calls)
	}

	// Turn 2, same conversation and same model: pin applies, no routing.
	if _, err := p.Execute(context.Background(), msg, "openai", "t2", ""); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if rr.calls != 1 {
		t.Fatalf("routing calls = %d, want still 1 (same model keeps the pin)", rr.calls)
	}

	// Turn 3, same conversation but now explicitly requesting another model:
	// the pin must be discarded and routing must run.
	other := newAffinityPipeline(rr, fu, fakeNormalizer{model: "manual"}, nil, time.Minute)
	other.affinity = p.affinity
	if _, err := other.Execute(context.Background(), msg, "openai", "t3", ""); err != nil {
		t.Fatalf("turn 3: %v", err)
	}
	if rr.calls != 2 {
		t.Fatalf("routing calls = %d, want 2 (a different requested model must discard the pin)", rr.calls)
	}
}

func TestAffinityPinnedProviderInCooldownFallsThrough(t *testing.T) {
	rr := &recordingRouter{route: primaryRoute()}
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	n := fakeNormalizer{model: "auto"}
	p := newAffinityPipeline(rr, fu, n, []string{"fallback1"}, time.Minute)

	msg := []byte("explain how the custom parser handles nesting")
	if _, err := p.Execute(context.Background(), msg, "openai", "t1", ""); err != nil {
		t.Fatalf("turn 1: %v", err)
	}

	// Primary is now cooling down; the pin must be treated as a miss, fresh
	// routing must run, and tryUpstream must serve from the fallback.
	p.markCooldown("primary", time.Now().Add(time.Minute))
	if _, err := p.Execute(context.Background(), msg, "openai", "t2", ""); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if rr.calls != 2 {
		t.Fatalf("routing calls = %d, want 2 (cooling pin must fall through)", rr.calls)
	}
	last := fu.calls[len(fu.calls)-1]
	if last != "fallback1" {
		t.Fatalf("last upstream call = %q, want fallback1 (primary is cooling)", last)
	}
}

func TestCacheTTLForUsesProviderOverride(t *testing.T) {
	providers := testProviders()
	providers["primary"] = types.ProviderConfig{Name: "primary", Models: []string{"m"}, CacheTTL: 30 * time.Second}
	p := NewPipeline(nil, nil, nil, nil, nil, nil, providers, nil, nil, nil, fakeLogger{}, time.Minute, nil, nil, nil, nil)

	if got := p.cacheTTLFor("primary"); got != 30*time.Second {
		t.Fatalf("cacheTTLFor(primary) = %v, want the provider override 30s", got)
	}
	if got := p.cacheTTLFor("fallback1"); got != time.Minute {
		t.Fatalf("cacheTTLFor(fallback1) = %v, want the default 1m", got)
	}
}

// TestForceAliasOverridesOnlyNamedAxes verifies that a force-alias overrides
// only the axes it declares, leaving other axes to come from normal
// classification: 'coding' forces domain=code_generation but says nothing
// about effort, so effort must still be classified from the request text.
func TestForceAliasOverridesOnlyNamedAxes(t *testing.T) {
	provs := map[string]types.ProviderConfig{
		"fast":  {Name: "fast", Type: "openai", Models: []string{"fast-model"}},
		"smart": {Name: "smart", Type: "openai", Models: []string{"smart-model"}},
	}
	resolver := router.NewAliasResolver(map[string]router.Alias{
		"coding": {Name: "coding", Force: map[string][]string{"domain": {"code_generation"}}},
	}, provs, nil, nil)

	effort := classifier.NewHeuristicClassifier("effort", classifier.AxisEffort, map[string][]string{
		"easy":   {"quick", "simple"},
		"medium": {"think", "consider"},
		"hard":   {"complex", "architecture"},
	})

	// Only matches if BOTH the forced domain and the classified effort land.
	rules := []router.PolicyRule{
		{When: router.PolicyCondition{Domain: "code_generation", Effort: "medium"}, Provider: "smart"},
		{When: router.PolicyCondition{}, Provider: "fast"},
	}
	policy := router.NewPolicyRouter("test", rules, provs, resolver, nil)

	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	n := fakeNormalizer{model: "coding"}
	p := NewPipeline(nil, n, fakeDenormalizer{}, []classifier.Classifier{effort}, policy, fu, provs, nil, nil, nil, fakeLogger{}, time.Minute, resolver, nil, nil, nil)

	if _, err := p.Execute(context.Background(), []byte("please think about this problem"), "openai", "t1", ""); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(fu.calls) != 1 || fu.calls[0] != "smart" {
		t.Fatalf("upstream calls = %v, want [smart]: domain is forced but effort must still classify to medium", fu.calls)
	}
}

// TestExplicitModelPrecedenceOverAffinityPin verifies that an explicit
// req.Model naming a configured model routes straight to that provider's
// model, even when the conversation already has an affinity pin recorded
// under a different requested model.
func TestExplicitModelPrecedenceOverAffinityPin(t *testing.T) {
	provs := map[string]types.ProviderConfig{
		"claude": {Name: "claude", Type: "anthropic", Models: []string{"claude-3-opus"}},
		"gpt4":   {Name: "gpt4", Type: "openai", Models: []string{"gpt-4o"}},
	}
	// "auto" must be a configured alias (the README's own full-auto example:
	// force nothing, classify + rules) — an arbitrary unconfigured string in
	// req.Model is not valid client input (REQUIREMENTS.md §1), so this test
	// exercises the real full-auto path rather than a typo/garbage model.
	resolver := router.NewAliasResolver(map[string]router.Alias{
		"auto": {Name: "auto", Force: map[string][]string{}},
	}, provs, nil, nil)
	policy := router.NewPolicyRouter("test", []router.PolicyRule{
		{When: router.PolicyCondition{}, Provider: "claude"},
	}, provs, resolver, nil)

	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	msg := []byte("explain how the custom parser handles nesting")

	// Turn 1: model "auto" (the full-auto alias, not a literal configured
	// model) routes via the policy router and pins the conversation to claude
	// under "auto".
	pAuto := NewPipeline(nil, fakeNormalizer{model: "auto"}, fakeDenormalizer{}, nil, policy, fu, provs, nil, nil, nil, fakeLogger{}, time.Minute, resolver, nil, nil, nil)
	if _, err := pAuto.Execute(context.Background(), msg, "openai", "t1", ""); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if len(fu.calls) != 1 || fu.calls[0] != "claude" {
		t.Fatalf("turn 1 upstream calls = %v, want [claude]", fu.calls)
	}

	// Turn 2: same conversation, but req.Model explicitly names gpt-4o — a
	// declared model of a different provider. That must win over the pin,
	// sending the request to gpt4.
	pGPT := NewPipeline(nil, fakeNormalizer{model: "gpt-4o"}, fakeDenormalizer{}, nil, policy, fu, provs, nil, nil, nil, fakeLogger{}, time.Minute, nil, nil, nil, nil)
	pGPT.affinity = pAuto.affinity
	if _, err := pGPT.Execute(context.Background(), msg, "openai", "t2", ""); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if len(fu.calls) != 2 || fu.calls[1] != "gpt4" {
		t.Fatalf("upstream calls = %v, want [claude gpt4]: explicit model must override the affinity pin", fu.calls)
	}
	if fu.models[1] != "gpt-4o" {
		t.Fatalf("turn 2 model = %q, want gpt-4o", fu.models[1])
	}
}

// TestUnknownModelRejected verifies that a model the client sent which
// matches no configured provider's declared Models and no configured alias
// (a typo, a stale name, or outright garbage) is rejected outright rather
// than classified/routed as if it meant something. REQUIREMENTS.md §1 makes
// the model field's two valid shapes exhaustive — a real declared model or a
// configured alias — so anything else is invalid client input, not a signal
// to route around.
func TestUnknownModelRejected(t *testing.T) {
	provs := map[string]types.ProviderConfig{
		"claude": {Name: "claude", Type: "anthropic", Models: []string{"claude-3-opus"}},
	}
	policy := router.NewPolicyRouter("test", []router.PolicyRule{
		{When: router.PolicyCondition{}, Provider: "claude"},
	}, provs, nil, nil)

	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	n := fakeNormalizer{model: "anthropic/booboo"}
	p := NewPipeline(nil, n, fakeDenormalizer{}, nil, policy, fu, provs, nil, nil, nil, fakeLogger{}, time.Minute, nil, nil, nil, nil)

	_, err := p.Execute(context.Background(), []byte("hello"), "openai", "t1", "")
	var unknownModelErr *arbitererrors.UnknownModelError
	if !errors.As(err, &unknownModelErr) {
		t.Fatalf("execute error = %v, want *UnknownModelError", err)
	}
	if len(fu.calls) != 0 {
		t.Fatalf("upstream calls = %v, want none: a rejected request must never reach upstream", fu.calls)
	}
}
