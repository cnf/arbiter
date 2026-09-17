package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/classifier"
	"github.com/cnf/arbiter/pkg/types"
)

// fakeCallingClassifier always fills Domain and reports one ClassifierCall,
// standing in for an LLMClassifier without needing a real upstream in these
// pipeline-level tests (LLMClassifier itself is tested directly in
// internal/classifier).
type fakeCallingClassifier struct{ calls int }

func (f *fakeCallingClassifier) Classify(context.Context, *types.NormalizedRequest) (types.Signals, error) {
	f.calls++
	return types.Signals{
		Domain: "code_generation",
		ClassifierCalls: []*types.ClassifierCallInfo{
			{Provider: "cls-provider", Model: "cls-model", StatusCode: 200, RawReply: "code_generation"},
		},
	}, nil
}

// TestClassifierCallRecordedOnceThenSkippedByAffinityPin proves a
// kind="classifier" event is recorded alongside the real request on a
// session's first turn (classify actually ran), and not recorded again on
// the second turn — because the session-affinity pin skips classify()
// entirely once a session is pinned, exactly the "classify once per
// session, for free" property the LLM classifier work is built on.
func TestClassifierCallRecordedOnceThenSkippedByAffinityPin(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	w := &capturingWriter{}
	fc := &fakeCallingClassifier{}
	p := NewPipeline(
		nil, fakeNormalizer{model: ""}, fakeDenormalizer{},
		[]classifier.Classifier{fc}, &fakeRouter{route: primaryRoute()}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil,
	)

	// Turn 1: no pin yet, classify runs, both the classifier event and the
	// real request event must land.
	if _, err := p.Execute(context.Background(), []byte("please fix this bug in the parser"), "openai", "t1", "chat-1"); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if fc.calls != 1 {
		t.Fatalf("classifier calls after turn 1 = %d, want 1", fc.calls)
	}

	events := w.events
	var classifierEvents, clientEvents int
	for _, ev := range events {
		switch ev.Kind {
		case "classifier":
			classifierEvents++
			if ev.SessionKey != events[len(events)-1].SessionKey {
				t.Errorf("classifier event session key = %q, want to match the real request's", ev.SessionKey)
			}
			if ev.Provider != "cls-provider" || ev.Model != "cls-model" {
				t.Errorf("classifier event provider/model = %s/%s, want cls-provider/cls-model", ev.Provider, ev.Model)
			}
		case "client", "":
			clientEvents++
		}
	}
	if classifierEvents != 1 {
		t.Fatalf("classifier-kind events after turn 1 = %d, want 1", classifierEvents)
	}
	if clientEvents != 1 {
		t.Fatalf("client-kind events after turn 1 = %d, want 1", clientEvents)
	}

	// Turn 2: same session, same (empty) requested model — the pin applies,
	// classify() never runs, so no new classifier event should appear.
	if _, err := p.Execute(context.Background(), []byte("please fix this bug in the parser"), "openai", "t2", "chat-1"); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if fc.calls != 1 {
		t.Fatalf("classifier calls after turn 2 = %d, want still 1 (pin must skip classify)", fc.calls)
	}

	classifierEvents = 0
	for _, ev := range w.events {
		if ev.Kind == "classifier" {
			classifierEvents++
		}
	}
	if classifierEvents != 1 {
		t.Fatalf("classifier-kind events after turn 2 = %d, want still 1", classifierEvents)
	}
}
