package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// --- fakes ---

type fakeUpstream struct {
	calls     []string // provider names, in call order
	sendErr   map[string]error
	streamErr map[string]error
	resp      *types.NormalizedResponse
}

func (f *fakeUpstream) Send(ctx context.Context, route types.Route, req *types.NormalizedRequest) (*types.NormalizedResponse, error) {
	f.calls = append(f.calls, route.Provider)
	if err, ok := f.sendErr[route.Provider]; ok {
		return nil, err
	}
	return f.resp, nil
}

func (f *fakeUpstream) SendStream(ctx context.Context, route types.Route, req *types.NormalizedRequest) (<-chan *types.NormalizedStreamEvent, error) {
	f.calls = append(f.calls, route.Provider)
	if err, ok := f.streamErr[route.Provider]; ok {
		return nil, err
	}
	ch := make(chan *types.NormalizedStreamEvent, 1)
	ch <- &types.NormalizedStreamEvent{Type: "message_stop"}
	close(ch)
	return ch, nil
}

type fakeRouter struct{ route types.Route }

func (f *fakeRouter) Route(ctx context.Context, req *types.NormalizedRequest, signals types.Signals) (types.Route, types.Metadata, error) {
	return f.route, types.Metadata{}, nil
}

type fakeLogger struct{}

func (fakeLogger) LogRouting(context.Context, types.Route, types.Signals, time.Duration) {}
func (fakeLogger) LogGuardrail(context.Context, string, string, bool)                    {}
func (fakeLogger) LogUpstream(context.Context, string, int, time.Duration, types.Usage)  {}
func (fakeLogger) LogUpstreamCooldown(context.Context, string, time.Time, time.Duration, string) {
}
func (fakeLogger) LogError(context.Context, string, error, map[string]interface{}) {}
func (fakeLogger) ExtractTraceID(context.Context) string                           { return "" }
func (fakeLogger) WithTraceID(ctx context.Context, _ string) context.Context       { return ctx }

// --- test setup ---

func testProviders() map[string]types.ProviderConfig {
	return map[string]types.ProviderConfig{
		"primary":   {Name: "primary", Type: "openai", Models: []string{"m-primary"}},
		"fallback1": {Name: "fallback1", Type: "openai", Models: []string{"m-fallback1"}},
		"fallback2": {Name: "fallback2", Type: "openai", Models: []string{"m-fallback2"}},
	}
}

func upstream429(provider string, retryAfter time.Duration) error {
	e := arbitererrors.NewUpstreamError(provider, 429, "rate limited", nil)
	e.RetryAfter = retryAfter
	return e
}

func upstream5xx(provider string) error {
	return arbitererrors.NewUpstreamError(provider, 503, "upstream exploded", nil)
}

// --- tests ---

func TestFallbackOn429RecordsCooldown(t *testing.T) {
	fu := &fakeUpstream{
		sendErr: map[string]error{"primary": upstream429("primary", 30*time.Second)},
		resp:    &types.NormalizedResponse{},
	}
	p := NewPipeline(nil, nil, nil, nil, &fakeRouter{}, fu, testProviders(), []string{"fallback1", "primary"}, nil, nil, fakeLogger{})
	req := &types.NormalizedRequest{}
	route := types.Route{Provider: "primary", Model: "m-primary", Config: testProviders()["primary"]}

	resp, _, err := p.tryUpstream(context.Background(), route, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != fu.resp {
		t.Fatal("expected the fallback's response")
	}
	// primary attempted once, fallback1 once; "primary" appearing again in
	// the fallback list must NOT cause a second attempt
	if len(fu.calls) != 2 || fu.calls[0] != "primary" || fu.calls[1] != "fallback1" {
		t.Fatalf("calls = %v, want [primary fallback1]", fu.calls)
	}

	// the 429 recorded a cooldown matching the requested Retry-After
	until, cooling := p.onCooldown("primary")
	if !cooling {
		t.Fatal("expected primary to be in cooldown after 429")
	}
	if remaining := time.Until(until); remaining < 29*time.Second || remaining > 30*time.Second {
		t.Fatalf("cooldown remaining = %v, want ~30s", remaining)
	}
}

func Test429WithoutRetryAfterUsesDefaultCooldown(t *testing.T) {
	fu := &fakeUpstream{
		sendErr: map[string]error{"primary": upstream429("primary", 0)},
		resp:    &types.NormalizedResponse{},
	}
	p := NewPipeline(nil, nil, nil, nil, &fakeRouter{}, fu, testProviders(), []string{"fallback1"}, nil, nil, fakeLogger{})
	route := types.Route{Provider: "primary", Config: testProviders()["primary"]}

	if _, _, err := p.tryUpstream(context.Background(), route, &types.NormalizedRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	until, cooling := p.onCooldown("primary")
	if !cooling {
		t.Fatal("expected cooldown even without Retry-After")
	}
	if remaining := time.Until(until); remaining < 4*time.Second || remaining > 5*time.Second {
		t.Fatalf("cooldown remaining = %v, want ~5s default", remaining)
	}
}

func TestCooldownSkipsProviderOnNextRequest(t *testing.T) {
	fu := &fakeUpstream{
		sendErr: map[string]error{"primary": upstream429("primary", time.Minute)},
		resp:    &types.NormalizedResponse{},
	}
	providers := testProviders()
	p := NewPipeline(nil, nil, nil, nil, &fakeRouter{}, fu, providers, []string{"fallback1"}, nil, nil, fakeLogger{})
	route := types.Route{Provider: "primary", Config: providers["primary"]}
	req := &types.NormalizedRequest{}

	// first request: primary 429s, fallback1 saves it
	if _, _, err := p.tryUpstream(context.Background(), route, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// second request: primary must be skipped entirely (cooldown respected)
	if _, _, err := p.tryUpstream(context.Background(), route, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"primary", "fallback1", "fallback1"}
	if len(fu.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", fu.calls, want)
	}
	for i := range want {
		if fu.calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", fu.calls, want)
		}
	}
}

func Test5xxRetriesPerRetryMaxThenFallback(t *testing.T) {
	fu := &fakeUpstream{
		sendErr: map[string]error{"primary": upstream5xx("primary")},
		resp:    &types.NormalizedResponse{},
	}
	providers := testProviders()
	providers["primary"] = types.ProviderConfig{Name: "primary", Type: "openai", Models: []string{"m-primary"}, RetryMax: 2}
	p := NewPipeline(nil, nil, nil, nil, &fakeRouter{}, fu, providers, []string{"fallback1"}, nil, nil, fakeLogger{})
	route := types.Route{Provider: "primary", Config: providers["primary"]}

	resp, _, err := p.tryUpstream(context.Background(), route, &types.NormalizedRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != fu.resp {
		t.Fatal("expected fallback response after exhausted retries")
	}
	want := []string{"primary", "primary", "primary", "fallback1"}
	if len(fu.calls) != len(want) {
		t.Fatalf("calls = %v, want %v (RetryMax=2 means 3 attempts)", fu.calls, want)
	}
	for i := range want {
		if fu.calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", fu.calls, want)
		}
	}
}

func TestNonRetriable429FailsFast(t *testing.T) {
	fu := &fakeUpstream{
		sendErr: map[string]error{"primary": arbitererrors.NewUpstreamError("primary", 400, "bad request", nil)},
	}
	p := NewPipeline(nil, nil, nil, nil, &fakeRouter{}, fu, testProviders(), []string{"fallback1"}, nil, nil, fakeLogger{})
	route := types.Route{Provider: "primary", Config: testProviders()["primary"]}

	_, _, err := p.tryUpstream(context.Background(), route, &types.NormalizedRequest{})
	if err == nil {
		t.Fatal("expected the 400 to propagate")
	}
	if len(fu.calls) != 1 {
		t.Fatalf("calls = %v, want [primary] only (no fallback for 4xx)", fu.calls)
	}
}

func Test429WithoutFallbackPropagates(t *testing.T) {
	fu := &fakeUpstream{
		sendErr: map[string]error{"primary": upstream429("primary", 0)},
	}
	p := NewPipeline(nil, nil, nil, nil, &fakeRouter{}, fu, testProviders(), nil, nil, nil, fakeLogger{})
	route := types.Route{Provider: "primary", Config: testProviders()["primary"]}

	_, _, err := p.tryUpstream(context.Background(), route, &types.NormalizedRequest{})
	var ue *arbitererrors.UpstreamError
	if err == nil {
		t.Fatal("expected 429 to propagate when no fallbacks are configured")
	}
	if !errors.As(err, &ue) || ue.StatusCode != 429 {
		t.Fatalf("error = %v, want UpstreamError 429", err)
	}
}

func TestAllCandidatesInCooldown(t *testing.T) {
	fu := &fakeUpstream{}
	p := NewPipeline(nil, nil, nil, nil, &fakeRouter{}, fu, testProviders(), []string{"fallback1"}, nil, nil, fakeLogger{})
	p.markCooldown("primary", time.Now().Add(time.Minute))
	p.markCooldown("fallback1", time.Now().Add(time.Minute))
	route := types.Route{Provider: "primary", Config: testProviders()["primary"]}

	_, _, err := p.tryUpstream(context.Background(), route, &types.NormalizedRequest{})
	if err == nil {
		t.Fatal("expected an error when every candidate is cooling down")
	}
	if len(fu.calls) != 0 {
		t.Fatalf("calls = %v, want none (all skipped)", fu.calls)
	}
}

func TestStreamFallbackOn429(t *testing.T) {
	fu := &fakeUpstream{
		streamErr: map[string]error{"primary": upstream429("primary", 0)},
	}
	p := NewPipeline(nil, nil, nil, nil, &fakeRouter{}, fu, testProviders(), []string{"fallback1"}, nil, nil, fakeLogger{})
	route := types.Route{Provider: "primary", Config: testProviders()["primary"]}

	_, evtChan, err := p.tryUpstream(context.Background(), route, &types.NormalizedRequest{Stream: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evtChan == nil {
		t.Fatal("expected a stream channel from the fallback")
	}
	if len(fu.calls) != 2 || fu.calls[0] != "primary" || fu.calls[1] != "fallback1" {
		t.Fatalf("calls = %v, want [primary fallback1]", fu.calls)
	}
}

func TestMarkCooldownNeverShortens(t *testing.T) {
	p := &Pipeline{cooldowns: make(map[string]time.Time)}
	long := time.Now().Add(time.Hour)
	short := time.Now().Add(time.Minute)

	p.markCooldown("x", long)
	p.markCooldown("x", short)

	if until, _ := p.onCooldown("x"); !until.Equal(long) {
		t.Fatalf("cooldown until = %v, want the longer %v", until, long)
	}
}
