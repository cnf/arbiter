package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/cnf/arbiter/pkg/types"

	arbitererrors "github.com/cnf/arbiter/pkg/errors"
)

// stoppingRouter always returns an *arbitererrors.StopError, simulating a
// matched `target: {stop: {...}}` rule.
type stoppingRouter struct {
	statusCode int
	message    string
}

func (r *stoppingRouter) Route(ctx context.Context, req *types.NormalizedRequest, signals types.Signals) (types.Route, error) {
	return types.Route{}, arbitererrors.NewStopError(r.statusCode, r.message)
}

// A router's StopError must reach the Execute caller unwrapped — not folded
// into a RoutingError, which would erase the configured status/message and
// make every stop rule look like a generic 500. This is the pipeline half of
// #43 part 2; internal/http's writeArbiterError is the client-facing half.
func TestExecutePropagatesStopErrorUnwrapped(t *testing.T) {
	w := &capturingRecorder{}
	p := NewPipeline(
		nil, fakeNormalizer{}, fakeDenormalizer{},
		nil, &stoppingRouter{statusCode: 406, message: "not like that poopoohead"}, &fakeUpstream{},
		testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)
	p.SetCaptureContent(true)

	_, err := p.Execute(context.Background(), []byte("hello"), "openai", "t1", "")
	if err == nil {
		t.Fatal("Execute: expected an error, got nil")
	}
	// A direct type assertion, not errors.As: RoutingError embeds Unwrap, so
	// errors.As would also succeed on a StopError wrapped inside one — and
	// this test exists to prove the stop comes back UNWRAPPED. The exact
	// shape matters: pipeline.resolveRoute must pass the StopError through,
	// because writeArbiterError's errors.As would otherwise match the nested
	// one and return 406 anyway — masking that a RouterError went to the
	// logs — and the rejection-recording path needs the real type.
	stopErr, ok := err.(*arbitererrors.StopError)
	if !ok {
		t.Fatalf("Execute error = %T (%v), want a bare *arbitererrors.StopError (not wrapped in RoutingError)", err, err)
	}
	if stopErr.StatusCode != 406 || stopErr.Message != "not like that poopoohead" {
		t.Errorf("StopError = {%d, %q}, want {406, \"not like that poopoohead\"}", stopErr.StatusCode, stopErr.Message)
	}
	// #5: a refused request (a stop is a refusal, same as a guardrail
	// rejection or a routing failure) gets a real requests row now — status
	// and error set, content attached the normal way — not a content-only
	// stub under the separate "rejected" owner kind.
	if len(w.events) != 1 {
		t.Fatalf("events = %+v, want exactly 1 — a stop is a refused client request, not an invisible one", w.events)
	}
	ev := w.events[0]
	if ev.StatusCode != 406 {
		t.Errorf("events[0].StatusCode = %d, want 406 (the stop's configured status)", ev.StatusCode)
	}
	if ev.Error != stopErr.Error() {
		t.Errorf("events[0].Error = %q, want %q", ev.Error, stopErr.Error())
	}
	if ev.Content == nil || len(ev.Content.Request) == 0 {
		t.Error("events[0].Content.Request = empty, want the refused request's content captured")
	}
}
