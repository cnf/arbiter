package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/guardrail"
	"github.com/cnf/arbiter/pkg/types"
)

// TestCaptureRecordsBothFormsWhenPreGuardrailRuns is #13's pipeline half: when
// a pre-guardrail mutates the request, the recorded event must carry both the
// "as sent" capture (Content.Request, unchanged behavior — see
// TestCaptureHappensBeforePreGuardrails in capture_test.go) and the new "as
// guardrailed" capture (Content.RequestGuardrailed), so the UI has both forms
// to toggle between. rewritingGuardrail and eavesdropNormalizer are shared
// fakes defined in capture_test.go, in this same package.
func TestCaptureRecordsBothFormsWhenPreGuardrailRuns(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{Usage: types.Usage{InputTokens: 5}}}
	w := &capturingRecorder{}
	p := NewPipeline(
		nil, eavesdropNormalizer{}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil,
		[]guardrail.Guardrail{rewritingGuardrail{}}, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)
	p.SetCaptureContent(true)

	if _, err := p.Execute(context.Background(), []byte("the real question"), "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	ev, ok := lastEvent(w)
	if !ok {
		t.Fatal("no event recorded")
	}
	if ev.Content == nil {
		t.Fatal("no content captured")
	}

	var asSent, asGuardrailed string
	for _, b := range ev.Content.Request {
		if b.Role == "system" {
			asSent = string(b.Body)
		}
	}
	for _, b := range ev.Content.RequestGuardrailed {
		if b.Role == "system" {
			asGuardrailed = string(b.Body)
		}
	}
	if asSent != "CLIENT SYSTEM PROMPT" {
		t.Errorf("Content.Request system text = %q, want the client's own prompt", asSent)
	}
	if asGuardrailed != "ARBITER INJECTED PROMPT" {
		t.Errorf("Content.RequestGuardrailed system text = %q, want the guardrail's rewrite", asGuardrailed)
	}
	if asSent == asGuardrailed {
		t.Error("both captures hold the same text — the second capture did not run after the guardrail")
	}
}

// TestCaptureSkipsGuardrailedFormWhenNoPreGuardrailRuns is the common-case
// guard: with no pre-guardrail configured (or none whose ShouldRun matches),
// the two captures would be byte-identical, so RequestGuardrailed must stay
// empty rather than duplicating every block's storage for nothing.
func TestCaptureSkipsGuardrailedFormWhenNoPreGuardrailRuns(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{Usage: types.Usage{InputTokens: 5}}}
	w := &capturingRecorder{}
	p := NewPipeline(
		nil, fakeNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)
	p.SetCaptureContent(true)

	if _, err := p.Execute(context.Background(), []byte("hello"), "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	ev, ok := lastEvent(w)
	if !ok || ev.Content == nil {
		t.Fatal("no content captured")
	}
	if len(ev.Content.RequestGuardrailed) != 0 {
		t.Errorf("Content.RequestGuardrailed = %+v, want empty with no pre-guardrail configured", ev.Content.RequestGuardrailed)
	}
	if len(ev.Content.Request) == 0 {
		t.Error("Content.Request is empty; the one form that should always be captured is missing")
	}
}
