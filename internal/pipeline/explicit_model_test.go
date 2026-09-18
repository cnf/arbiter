package pipeline

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// An explicitly-named model must never be silently substituted.
//
// The user's spec: "asking for a specifc 429ed model should send a 429 to the
// client. it's 429'ed no need to hammer it again, let the client fail."
//
// Before this, an explicit model whose provider was rate-limited was quietly
// served by a fallback provider — the client asked for one model and got
// another's answer, with a 200 and no indication.
//
// The scoping matters as much as the behaviour: every NON-explicit route must
// keep falling through exactly as before, since there Arbiter made the choice
// and any equivalent model satisfies the request.

// rateLimitedUpstream returns a 429 for every send, counting the calls.
type rateLimitedUpstream struct {
	calls int
}

func (u *rateLimitedUpstream) Send(ctx context.Context, route types.Route, req *types.NormalizedRequest) (*types.NormalizedResponse, error) {
	u.calls++
	return nil, arbitererrors.NewUpstreamError(route.Provider, http.StatusTooManyRequests, "rate limited", nil)
}

func (u *rateLimitedUpstream) SendStream(ctx context.Context, route types.Route, req *types.NormalizedRequest) (<-chan *types.NormalizedStreamEvent, <-chan error, error) {
	u.calls++
	return nil, nil, arbitererrors.NewUpstreamError(route.Provider, http.StatusTooManyRequests, "rate limited", nil)
}

// okUpstream succeeds, recording which model it was asked for — that is how a
// test tells "served the explicit model" from "served a fallback".
type okUpstream struct {
	served []string
}

func (u *okUpstream) Send(ctx context.Context, route types.Route, req *types.NormalizedRequest) (*types.NormalizedResponse, error) {
	u.served = append(u.served, route.Provider+"/"+route.Model)
	return &types.NormalizedResponse{Content: []types.ContentBlock{{Type: "text", Text: "ok"}}, Model: route.Model}, nil
}

func (u *okUpstream) SendStream(ctx context.Context, route types.Route, req *types.NormalizedRequest) (<-chan *types.NormalizedStreamEvent, <-chan error, error) {
	u.served = append(u.served, route.Provider+"/"+route.Model)
	ch := make(chan *types.NormalizedStreamEvent)
	close(ch)
	return ch, nil, nil
}

func reqFor(model string) *types.NormalizedRequest {
	return &types.NormalizedRequest{
		Model:    model,
		Messages: []types.Message{{Role: "user", Content: []types.ContentBlock{{Type: "text", Text: "hello there"}}}},
	}
}

// TestExplicitModelInCooldownReturns429 proves Path 1: when the named model's
// provider is already cooling down, the client gets a 429 and the upstream is
// never contacted.
func TestExplicitModelInCooldownReturns429(t *testing.T) {
	up := &rateLimitedUpstream{}
	p := NewPipeline(nil, nil, nil, nil, nil, up, testProviders(), nil, nil, nil, fakeLogger{}, 0, nil, nil, nil, nil)

	// Put the explicit model's provider into cooldown.
	p.markCooldown("primary", time.Now().Add(time.Minute))

	route := types.Route{Provider: "primary", Model: "m-primary", Config: testProviders()["primary"], ExplicitModel: true}
	_, _, _, _, err := p.tryUpstream(context.Background(), route, reqFor("m-primary"))
	if err == nil {
		t.Fatal("expected a 429 for an explicit model in cooldown")
	}

	var ue *arbitererrors.UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("error is %T, want *UpstreamError", err)
	}
	if ue.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", ue.StatusCode)
	}
	if up.calls != 0 {
		t.Errorf("upstream was contacted %d time(s); a cooling-down provider must not be hammered", up.calls)
	}
}

// TestExplicitModelInCooldownDoesNotFallThrough is the other half of Path 1: the
// fallback chain must not be consulted, because that is what silently substituted
// a different model.
func TestExplicitModelInCooldownDoesNotFallThrough(t *testing.T) {
	up := &okUpstream{}
	providers := testProviders()
	p := NewPipeline(nil, nil, nil, nil, nil, up, providers, []string{"fallback1"}, nil, nil, fakeLogger{}, 0, nil, nil, nil, nil)
	p.markCooldown("primary", time.Now().Add(time.Minute))

	route := types.Route{Provider: "primary", Model: "m-primary", Config: providers["primary"], ExplicitModel: true}
	if _, _, _, _, err := p.tryUpstream(context.Background(), route, reqFor("m-primary")); err == nil {
		t.Fatal("expected an error, not a fallback")
	}
	if len(up.served) != 0 {
		t.Errorf("a fallback served the request: %v — an explicit model must not be substituted", up.served)
	}
}

// TestExplicitModelLive429Returns429 proves Path 2: a live 429 on an explicit
// model goes back to the client rather than to the fallback chain.
func TestExplicitModelLive429Returns429(t *testing.T) {
	up := &rateLimitedUpstream{}
	providers := testProviders()
	p := NewPipeline(nil, nil, nil, nil, nil, up, providers, []string{"fallback1"}, nil, nil, fakeLogger{}, 0, nil, nil, nil, nil)

	route := types.Route{Provider: "primary", Model: "m-primary", Config: providers["primary"], ExplicitModel: true}
	_, _, _, _, err := p.tryUpstream(context.Background(), route, reqFor("m-primary"))
	if err == nil {
		t.Fatal("expected a 429")
	}
	var ue *arbitererrors.UpstreamError
	if !errors.As(err, &ue) || ue.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want a 429 UpstreamError", err)
	}
	// Only the explicit provider was tried — no fallback contact.
	if up.calls != 1 {
		t.Errorf("upstream contacted %d time(s), want 1 (the explicit provider only)", up.calls)
	}
}

// TestExplicitModelLive429StillRecordsCooldown proves the cooldown is still
// marked, so unrelated non-explicit traffic routes around the provider normally.
// The refusal is scoped to this client's explicit request, not to the provider.
func TestExplicitModelLive429StillRecordsCooldown(t *testing.T) {
	up := &rateLimitedUpstream{}
	providers := testProviders()
	p := NewPipeline(nil, nil, nil, nil, nil, up, providers, nil, nil, nil, fakeLogger{}, 0, nil, nil, nil, nil)

	route := types.Route{Provider: "primary", Model: "m-primary", Config: providers["primary"], ExplicitModel: true}
	_, _, _, _, _ = p.tryUpstream(context.Background(), route, reqFor("m-primary"))

	if _, cooling := p.onCooldown("primary"); !cooling {
		t.Error("a 429 on an explicit model did not record a cooldown; other traffic would keep hammering the provider")
	}
}

// TestExplicitModel429StillRetries proves a retry is not treated as substitution:
// retry_max attempts are still made against the SAME provider before the 429 is
// surfaced. Otherwise an explicit model would give up more eagerly than any other
// route, which is the opposite of the intent.
func TestExplicitModel429StillRetries(t *testing.T) {
	up := &rateLimitedUpstream{}
	providers := testProviders()
	primary := providers["primary"]
	primary.RetryMax = 2 // 3 attempts total
	providers["primary"] = primary

	p := NewPipeline(nil, nil, nil, nil, nil, up, providers, nil, nil, nil, fakeLogger{}, 0, nil, nil, nil, nil)
	route := types.Route{Provider: "primary", Model: "m-primary", Config: primary, ExplicitModel: true}

	if _, _, _, _, err := p.tryUpstream(context.Background(), route, reqFor("m-primary")); err == nil {
		t.Fatal("expected a 429 after exhausting retries")
	}
	if up.calls != 3 {
		t.Errorf("upstream contacted %d time(s), want 3 (retry_max 2 + the first attempt)", up.calls)
	}
}

// TestExplicitModel5xxStillRetries proves the 5xx path is untouched: a transient
// error retries per retry_max regardless of explicitness.
func TestExplicitModel5xxStillRetries(t *testing.T) {
	up := &failUpstream{status: http.StatusInternalServerError}
	providers := testProviders()
	primary := providers["primary"]
	primary.RetryMax = 2
	providers["primary"] = primary

	p := NewPipeline(nil, nil, nil, nil, nil, up, providers, nil, nil, nil, fakeLogger{}, 0, nil, nil, nil, nil)
	route := types.Route{Provider: "primary", Model: "m-primary", Config: primary, ExplicitModel: true}

	_, _, _, _, err := p.tryUpstream(context.Background(), route, reqFor("m-primary"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if up.calls != 3 {
		t.Errorf("upstream contacted %d time(s), want 3 — a 5xx must still retry", up.calls)
	}
}

// --- the regression guards: non-explicit routes are unchanged ---

// TestNonExplicitRouteStillFallsThroughOnCooldown is the scoping guard. A route
// Arbiter chose (an alias, a pin, a policy rule) must keep falling through to a
// fallback when its provider is cooling down — that is the whole point of the
// fallback chain.
func TestNonExplicitRouteStillFallsThroughOnCooldown(t *testing.T) {
	up := &okUpstream{}
	providers := testProviders()
	p := NewPipeline(nil, nil, nil, nil, nil, up, providers, []string{"fallback1"}, nil, nil, fakeLogger{}, 0, nil, nil, nil, nil)
	p.markCooldown("primary", time.Now().Add(time.Minute))

	route := types.Route{Provider: "primary", Model: "m-primary", Config: providers["primary"]} // ExplicitModel false
	if _, _, _, served, err := p.tryUpstream(context.Background(), route, reqFor("auto")); err != nil {
		t.Fatalf("unexpected error: %v — a non-explicit route should fall through", err)
	} else if served.Provider != "fallback1" {
		t.Errorf("served by %q, want fallback1", served.Provider)
	}
}

// TestNonExplicitRouteStillFallsThroughOnLive429 is the same guard for a live 429.
func TestNonExplicitRouteStillFallsThroughOnLive429(t *testing.T) {
	// primary 429s; fallback1 succeeds. Modelled with a per-provider upstream.
	up := &perProviderUpstream{status: map[string]int{"primary": http.StatusTooManyRequests}}
	providers := testProviders()
	p := NewPipeline(nil, nil, nil, nil, nil, up, providers, []string{"fallback1"}, nil, nil, fakeLogger{}, 0, nil, nil, nil, nil)

	route := types.Route{Provider: "primary", Model: "m-primary", Config: providers["primary"]} // ExplicitModel false
	_, _, _, served, err := p.tryUpstream(context.Background(), route, reqFor("auto"))
	if err != nil {
		t.Fatalf("unexpected error: %v — a non-explicit route should fall through", err)
	}
	if served.Provider != "fallback1" {
		t.Errorf("served by %q, want fallback1", served.Provider)
	}
}

// TestGroupMember429StillFallsThroughToSiblings proves an author-declared group
// chain keeps working: a group's unselected members exist precisely so a 429 on
// one member is absorbed by another.
func TestGroupMember429StillFallsThroughToSiblings(t *testing.T) {
	up := &perProviderUpstream{status: map[string]int{"primary": http.StatusTooManyRequests}}
	providers := testProviders()
	p := NewPipeline(nil, nil, nil, nil, nil, up, providers, nil, nil, nil, fakeLogger{}, 0, nil, nil, nil, nil)

	route := types.Route{
		Provider: "primary", Model: "m-primary", Config: providers["primary"],
		Fallbacks: []types.Route{{Provider: "fallback1", Model: "m-fallback", Config: providers["fallback1"]}},
	}
	_, _, _, served, err := p.tryUpstream(context.Background(), route, reqFor("auto"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if served.Provider != "fallback1" {
		t.Errorf("served by %q, want the group's other member", served.Provider)
	}
}

// TestLiteralModelRouteIsMarkedExplicit proves the marker is actually set on the
// path that matters — otherwise the whole change is inert.
func TestLiteralModelRouteIsMarkedExplicit(t *testing.T) {
	providers := testProviders()
	p := NewPipeline(nil, nil, nil, nil, nil, &okUpstream{}, providers, nil, nil, nil, fakeLogger{}, 0, nil, nil, nil, nil)

	route, ok := p.literalModelRoute("m-primary")
	if !ok {
		t.Fatal("expected a literal route for a declared model")
	}
	if !route.ExplicitModel {
		t.Error("literalModelRoute did not mark the route explicit; the 429 behaviour would never trigger")
	}
}

// failUpstream always fails with one status.
type failUpstream struct {
	status int
	calls  int
}

func (u *failUpstream) Send(ctx context.Context, route types.Route, req *types.NormalizedRequest) (*types.NormalizedResponse, error) {
	u.calls++
	return nil, arbitererrors.NewUpstreamError(route.Provider, u.status, "upstream failed", nil)
}

func (u *failUpstream) SendStream(ctx context.Context, route types.Route, req *types.NormalizedRequest) (<-chan *types.NormalizedStreamEvent, <-chan error, error) {
	u.calls++
	return nil, nil, arbitererrors.NewUpstreamError(route.Provider, u.status, "upstream failed", nil)
}

// perProviderUpstream fails only for providers listed in status, succeeding
// otherwise — so a test can drive "primary 429s, fallback works".
type perProviderUpstream struct {
	status map[string]int
	served []string
}

func (u *perProviderUpstream) Send(ctx context.Context, route types.Route, req *types.NormalizedRequest) (*types.NormalizedResponse, error) {
	if code, bad := u.status[route.Provider]; bad {
		return nil, arbitererrors.NewUpstreamError(route.Provider, code, "upstream failed", nil)
	}
	u.served = append(u.served, route.Provider)
	return &types.NormalizedResponse{Content: []types.ContentBlock{{Type: "text", Text: "ok"}}, Model: route.Model}, nil
}

func (u *perProviderUpstream) SendStream(ctx context.Context, route types.Route, req *types.NormalizedRequest) (<-chan *types.NormalizedStreamEvent, <-chan error, error) {
	if code, bad := u.status[route.Provider]; bad {
		return nil, nil, arbitererrors.NewUpstreamError(route.Provider, code, "upstream failed", nil)
	}
	u.served = append(u.served, route.Provider)
	ch := make(chan *types.NormalizedStreamEvent)
	close(ch)
	return ch, nil, nil
}
