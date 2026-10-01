package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/router"
	"github.com/cnf/arbiter/internal/translator"
	"github.com/cnf/arbiter/pkg/types"
)

// The client-requested reasoning-effort knob (#79 phase 2): the value the
// client itself sent in `output_config.effort` must be readable as a routing
// signal, and — per the settled design — it must be present on EVERY route
// path, not just the ones that run classification. A literal model name, a
// pinned/group alias and the session-affinity pin all short-circuit before
// classification; the stamp is applied in resolveRoute at each of those exits
// from the value Execute captured once, so none of them silently drops it.
//
// ClientEffort is a request FACT, never a lock: it records what the client
// sent regardless of what a force-alias/policy rule later routes on. The lock
// (overriding what goes upstream) is phase 3 and deliberately not here.

// effortRecordingRouter reports the signals routing was given.
type effortRecordingRouter struct {
	signals types.Signals
}

func (r *effortRecordingRouter) Route(_ context.Context, _ *types.NormalizedRequest, sig types.Signals) (types.Route, error) {
	r.signals = sig
	return primaryRoute(), nil
}

// NewPipeline with the defaults a short-circuit path test needs.
func newEffortPipeline(rr router.Router, resolver *router.AliasResolver) *Pipeline {
	return NewPipeline(
		translator.NewDefaultTranslator(), translator.NewDefaultTranslator(), translator.NewDefaultTranslator(),
		nil, rr, &fakeUpstream{resp: &types.NormalizedResponse{}},
		testProviders(), nil, nil, nil, fakeLogger{}, time.Minute,
		resolver, &capturingWriter{}, nil, nil, nil)
}

// captureMsg builds an OpenAI chat payload carrying the client's reasoning
// effort, the shape a Hermes/Anthropic-style client sends.
func captureMsg(model, effort string) []byte {
	body := `{"model":"` + model + `","max_tokens":10,"messages":[{"role":"user","content":"hello"}]`
	if effort != "" {
		body += `,"output_config":{"effort":"` + effort + `"}`
	}
	return []byte(body + `}`)
}

// The classify+rules path: a request routed through a force alias falls
// through to classification, and carries the client's effort on the signals
// the router matches on.
func TestClientEffortReachesRouterOnClassifyPath(t *testing.T) {
	resolver := router.NewAliasResolver(map[string]router.Alias{
		// force: {} is the "full auto" alias — classify and let the rules decide.
		"auto": {Name: "auto", Force: map[string][]string{}},
	}, testProviders(), nil, nil)
	rr := &effortRecordingRouter{}
	p := newEffortPipeline(rr, resolver)

	if _, err := p.Execute(context.Background(), captureMsg("auto", "high"), "anthropic", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rr.signals.ClientEffort != "high" {
		t.Fatalf("router saw ClientEffort = %q, want high", rr.signals.ClientEffort)
	}
}

// A literal model name short-circuits before classification — the effort must
// still ride along on the returned signals, or a rule could never match on it
// for that traffic. Asserted through resolveRoute directly, since the literal
// path never calls the router.
func TestClientEffortStampedOnLiteralModelPath(t *testing.T) {
	p := newEffortPipeline(&effortRecordingRouter{}, nil)
	req := &types.NormalizedRequest{Model: "m-primary"}

	_, sig, err := p.resolveRoute(context.Background(), req, false, "hash", time.Now(), "high")
	if err != nil {
		t.Fatalf("resolveRoute: %v", err)
	}
	if sig.ClientEffort != "high" {
		t.Fatalf("literal-model path ClientEffort = %q, want high", sig.ClientEffort)
	}
}

// A pinned alias short-circuits with a bare Signals{} — the client effort must
// still be stamped there (this is the exit that silently dropped a sibling
// field before, per the RequestKind precedent).
func TestClientEffortStampedOnPinnedAliasPath(t *testing.T) {
	resolver := router.NewAliasResolver(map[string]router.Alias{
		"strong": {Name: "strong", Type: "pinned", Provider: "primary", Model: "m-primary"},
	}, testProviders(), nil, nil)
	p := newEffortPipeline(&effortRecordingRouter{}, resolver)
	req := &types.NormalizedRequest{Model: "strong"}

	_, sig, err := p.resolveRoute(context.Background(), req, false, "hash", time.Now(), "low")
	if err != nil {
		t.Fatalf("resolveRoute: %v", err)
	}
	if sig.ClientEffort != "low" {
		t.Fatalf("pinned-alias path ClientEffort = %q, want low", sig.ClientEffort)
	}
}

// No effort requested is not the same as "effort low": the signals carry the
// empty string, which a `when: {effort: ...}` rule treats as a wildcard.
func TestClientEffortEmptyWhenClientSendsNone(t *testing.T) {
	rr := &effortRecordingRouter{}
	p := newEffortPipeline(rr, nil)

	if _, err := p.Execute(context.Background(), captureMsg("m-primary", ""), "anthropic", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rr.signals.ClientEffort != "" {
		t.Fatalf("router saw ClientEffort = %q, want empty when the client asked for none", rr.signals.ClientEffort)
	}
}
