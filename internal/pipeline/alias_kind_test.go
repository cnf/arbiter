package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/classifier"
	"github.com/cnf/arbiter/internal/router"
	"github.com/cnf/arbiter/internal/translator"
	"github.com/cnf/arbiter/pkg/types"
)

// The alias-declared request kind (#44): an alias that says what a request
// naming it IS — "subagent" for an alias dedicated to subagent traffic — must
// stamp that kind onto the stored row at EVERY path the alias's declaration
// influences the route. A force alias resolves to no route, so its traffic
// falls through to classify+rules, and there are two paths that can serve it:
//
//   - the force-alias path (turn 1): applyForceAlias stamps the alias-declared
//     kind, overriding whatever a matcher classified (the operator's word
//     beats an inference),
//   - the session-affinity pin (turn 2+ of the same conversation): the pin
//     records the client's model — for alias traffic that IS the alias name —
//     and the pin path re-stamps the kind from the alias, since classification
//     never runs there.
//
// The short-circuit routes (pinned/group aliases, literal model names) are
// deliberately NOT covered: config validation rejects request_kind on those
// shapes, so the alias-declared kind can only reach the force path and the
// pin that follows it.
//
// A stamp at only one site shows `request_kind=subagent` on the first row and
// nothing on every row after — which is exactly the "worked once, then
// silently vanished" shape this file's tests pin down.
//
// These tests drive Execute end to end with a real translator and a capturing
// writer, so what is asserted is the STORED ROW, not an intermediate signal.

// kindAliasResolver configures one force alias, "subagent-worker", that
// declares request_kind=subagent. The force shape is the contract (see
// internal/config/aliases.go: a request_kind belongs on a force alias — a
// pinned/group alias is a destination, not a declaration). It short-circuits
// to no route, so traffic falls through to classify+rules and applyForceAlias
// stamps the kind — which is exactly the shape a real subagent alias uses.
func kindAliasResolver() *router.AliasResolver {
	return router.NewAliasResolver(map[string]router.Alias{
		"subagent-worker": {Name: "subagent-worker", Force: map[string][]string{}, RequestKind: "subagent"},
	}, testProviders(), nil, nil)
}

// capturingRouter reports the signals routing saw, and returns a route.
type kindRecordingRouter struct {
	signals types.Signals
	calls   int
}

func (r *kindRecordingRouter) Route(_ context.Context, _ *types.NormalizedRequest, sig types.Signals) (types.Route, error) {
	r.calls++
	r.signals = sig
	return primaryRoute(), nil
}

func newKindPipeline(rr *kindRecordingRouter, writer *capturingWriter) *Pipeline {
	return NewPipeline(
		translator.NewDefaultTranslator(), translator.NewDefaultTranslator(), translator.NewDefaultTranslator(),
		nil, rr, &fakeUpstream{resp: &types.NormalizedResponse{}},
		testProviders(), nil, nil, nil, fakeLogger{}, time.Minute,
		kindAliasResolver(), writer, nil, nil, nil)
}

// First row of an alias-routed conversation: a force alias falls through to
// classify+rules, so applyForceAlias stamps the kind. The row must carry it.
func TestAliasRequestKindStampedOnAliasRoute(t *testing.T) {
	rr := &kindRecordingRouter{}
	w := &capturingWriter{}
	p := newKindPipeline(rr, w)

	payload, err := aliasPayload("subagent-worker")
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if _, err := p.Execute(context.Background(), payload, "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	ev, ok := w.last()
	if !ok {
		t.Fatal("no event was recorded")
	}
	if ev.RequestKind != "subagent" {
		t.Fatalf("recorded request_kind = %q, want subagent — the force-alias path must stamp the alias-declared kind", ev.RequestKind)
	}
	if ev.AliasUsed != "subagent-worker" {
		t.Fatalf("alias_used = %q, want subagent-worker", ev.AliasUsed)
	}
}

// Turn 2+ of the same conversation: the affinity-pin path, also a bare
// Signals{} exit today. The stamp must survive the pin — the pin's requested
// model is the alias name, so the alias is still lookup-able there.
func TestAliasRequestKindSurvivesAffinityPin(t *testing.T) {
	rr := &kindRecordingRouter{}
	w := &capturingWriter{}
	p := newKindPipeline(rr, w)

	payload, err := aliasPayload("subagent-worker")
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if _, err := p.Execute(context.Background(), payload, "openai", "t1", ""); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	// Same conversation text → same derived session key → turn 2 hits the pin.
	if _, err := p.Execute(context.Background(), payload, "openai", "t2", ""); err != nil {
		t.Fatalf("second Execute: %v", err)
	}

	events := w.snapshot()
	if len(events) < 2 {
		t.Fatalf("recorded %d events, want 2", len(events))
	}
	for i, ev := range events {
		if ev.RequestKind != "subagent" {
			t.Fatalf("event %d: request_kind = %q, want subagent — the affinity-pin path (turn 2+) must keep the alias-declared kind, or it silently vanishes from every row after the first", i, ev.RequestKind)
		}
	}
}

// The force path: classification runs, and the alias-declared kind must
// OVERRIDE a matcher-produced kind rather than fill-if-empty — the operator
// named this alias for this traffic, and the two sources disagreeing on the
// row is the failure shape.
func TestAliasRequestKindOverridesClassifiedKindOnForcePath(t *testing.T) {
	// A matcher that claims every request is a title request — the wrong
	// answer for traffic the operator has pointed at the subagent-worker
	// alias.
	matcher, err := classifier.NewRequestMatcher(
		[]types.MatchPattern{{Pattern: ".", Mode: "regex"}}, "", "title", nil, false)
	if err != nil {
		t.Fatalf("NewRequestMatcher: %v", err)
	}
	kindClassifier := classifier.NewHeuristicClassifierWithMatch("kind", classifier.AxisDomain, nil, matcher)

	rr := &kindRecordingRouter{}
	w := &capturingWriter{}
	p := NewPipeline(
		translator.NewDefaultTranslator(), translator.NewDefaultTranslator(), translator.NewDefaultTranslator(),
		[]classifier.Classifier{kindClassifier}, rr, &fakeUpstream{resp: &types.NormalizedResponse{}},
		testProviders(), nil, nil, nil, fakeLogger{}, time.Minute,
		kindAliasResolver(), w, nil, nil, nil)

	payload, err := aliasPayload("subagent-worker")
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if _, err := p.Execute(context.Background(), payload, "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	ev, ok := w.last()
	if !ok {
		t.Fatal("no event was recorded")
	}
	if ev.RequestKind != "subagent" {
		t.Fatalf("recorded request_kind = %q, want subagent — the alias's declaration must override the classifier's kind on the force path", ev.RequestKind)
	}
	if rr.signals.RequestKind != "subagent" {
		t.Fatalf("router saw request_kind = %q, want subagent", rr.signals.RequestKind)
	}
}

// An alias that declares no kind changes nothing: the row keeps whatever
// classification produced (nothing, on the short-circuit paths).
func TestAliasWithoutRequestKindStampsNothing(t *testing.T) {
	resolver := router.NewAliasResolver(map[string]router.Alias{
		"plain": {Name: "plain", Type: "group", Members: []router.AliasMember{
			{Provider: "primary", Model: "m-primary"},
		}},
	}, testProviders(), nil, nil)
	rr := &kindRecordingRouter{}
	w := &capturingWriter{}
	p := NewPipeline(
		translator.NewDefaultTranslator(), translator.NewDefaultTranslator(), translator.NewDefaultTranslator(),
		nil, rr, &fakeUpstream{resp: &types.NormalizedResponse{}},
		testProviders(), nil, nil, nil, fakeLogger{}, time.Minute,
		resolver, w, nil, nil, nil)

	payload, err := aliasPayload("plain")
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if _, err := p.Execute(context.Background(), payload, "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	ev, ok := w.last()
	if !ok {
		t.Fatal("no event was recorded")
	}
	if ev.RequestKind != "" {
		t.Fatalf("recorded request_kind = %q, want empty — an alias without a declaration must not invent one", ev.RequestKind)
	}
}

// aliasPayload builds a minimal OpenAI chat payload naming the given alias.
func aliasPayload(model string) ([]byte, error) {
	return []byte(`{"model":"` + model + `","max_tokens":10,"messages":[{"role":"user","content":"summarize this diff"}]}`), nil
}
