package pipeline

import (
	"context"
	"testing"
	"time"

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

func newAffinityPipeline(rr *recordingRouter, fu *fakeUpstream, n fakeNormalizer, fallbacks []string, ttl time.Duration) *Pipeline {
	return NewPipeline(
		nil, n, fakeDenormalizer{},
		nil, rr, fu,
		testProviders(), fallbacks, nil, nil, fakeLogger{}, ttl,
	)
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
	other := newAffinityPipeline(rr, fu, fakeNormalizer{model: "gpt-4o"}, nil, time.Minute)
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
	p := NewPipeline(nil, nil, nil, nil, nil, nil, providers, nil, nil, nil, fakeLogger{}, time.Minute)

	if got := p.cacheTTLFor("primary"); got != 30*time.Second {
		t.Fatalf("cacheTTLFor(primary) = %v, want the provider override 30s", got)
	}
	if got := p.cacheTTLFor("fallback1"); got != time.Minute {
		t.Fatalf("cacheTTLFor(fallback1) = %v, want the default 1m", got)
	}
}