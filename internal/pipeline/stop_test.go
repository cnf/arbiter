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

func (r *stoppingRouter) Route(ctx context.Context, req *types.NormalizedRequest, signals types.Signals) (types.Route, types.Metadata, error) {
	return types.Route{}, types.Metadata{}, arbitererrors.NewStopError(r.statusCode, r.message)
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
	// A stop is a rejection, not a completed request: nothing may be recorded
	// as a normal event, but the refusal's content must still be captured
	// under the rejected owner so the operator can see what was refused.
	if len(w.events) != 0 {
		t.Errorf("events = %+v, want none — a stop is a rejection, not a completed request row", w.events)
	}
	if len(w.rejected) != 1 {
		t.Fatalf("rejected = %+v, want exactly 1 rejection recorded", w.rejected)
	}
	if len(w.rejected[0].Request) == 0 {
		t.Error("rejected[0].Request = empty, want the refused request's content captured")
	}
}
