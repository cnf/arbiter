package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/classifier"
	"github.com/cnf/arbiter/pkg/types"
)

func TestExecuteRecordsClassifiedSignals(t *testing.T) {
	fc := &classifiedSignalsClassifier{signals: types.Signals{
		Domain: "discovery", Difficulty: "hard", CostClass: "free", Confidence: 0.91,
		RequiredCapabilities: []string{"vision", "tool_use"},
	}}
	w := &capturingWriter{}
	p := NewPipeline(nil, fakeNormalizer{}, fakeDenormalizer{}, []classifier.Classifier{fc},
		&fakeRouter{route: primaryRoute()}, &fakeUpstream{resp: &types.NormalizedResponse{}},
		testProviders(), nil, nil, nil, fakeLogger{}, time.Minute, nil, w, nil, nil, nil)
	if _, err := p.Execute(context.Background(), []byte("hello"), "openai", "signals", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	ev, ok := w.last()
	if !ok {
		t.Fatal("no event recorded")
	}
	if ev.Domain != "discovery" || ev.Difficulty != "hard" || ev.CostClass != "free" || ev.Confidence != 0.91 {
		t.Errorf("recorded axes/confidence = %q/%q/%q/%v", ev.Domain, ev.Difficulty, ev.CostClass, ev.Confidence)
	}
	if len(ev.RequiredCapabilities) != 2 || ev.RequiredCapabilities[0] != "vision" || ev.RequiredCapabilities[1] != "tool_use" {
		t.Errorf("recorded capabilities = %v", ev.RequiredCapabilities)
	}
}

// classifiedSignalsClassifier reports a fixed Signals value, so a pipeline test
// can assert what the write path persists rather than what a heuristic matched.
type classifiedSignalsClassifier struct{ signals types.Signals }

func (c *classifiedSignalsClassifier) Classify(context.Context, *types.NormalizedRequest) (types.Signals, error) {
	return c.signals, nil
}

// A request refused by a `stop` rule is recorded on the failure path, not the
// success path — so the classified signals that justified the refusal have to
// be threaded through recordFailed too. Before that, a stopped request's row
// carried a status and an error message and nothing else, which is exactly the
// case worth inspecting: "why was this refused?" is answered by the signals the
// rule matched on, and the rationale naming the rule's own `when` clause.
func TestStoppedRequestKeepsClassifiedSignals(t *testing.T) {
	fc := &classifiedSignalsClassifier{signals: types.Signals{
		Domain: "discovery", Difficulty: "hard", CostClass: "free", Confidence: 0.91,
		RequiredCapabilities: []string{"vision", "tool_use"}, RequestKind: "title",
	}}
	w := &capturingRecorder{}
	p := NewPipeline(nil, fakeNormalizer{}, fakeDenormalizer{}, []classifier.Classifier{fc},
		&stoppingRouter{statusCode: 406, message: "not like that"}, &fakeUpstream{},
		testProviders(), nil, nil, nil, fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	_, err := p.Execute(context.Background(), []byte("hello"), "openai", "stopped", "")
	if err == nil {
		t.Fatal("Execute: expected the stop to surface as an error, got nil")
	}
	if len(w.events) != 1 {
		t.Fatalf("events = %d, want exactly 1 refused request row", len(w.events))
	}
	ev := w.events[0]

	// The refusal itself still has to look like a refusal.
	if ev.StatusCode != 406 {
		t.Errorf("StatusCode = %d, want 406 (the stop's configured status)", ev.StatusCode)
	}
	if ev.Error == "" {
		t.Error("Error is empty, want the stop's message")
	}

	// …and the classification that led to it must not have been dropped.
	if ev.Domain != "discovery" || ev.Difficulty != "hard" || ev.CostClass != "free" {
		t.Errorf("axes = %q/%q/%q, want discovery/hard/free", ev.Domain, ev.Difficulty, ev.CostClass)
	}
	if ev.Confidence != 0.91 {
		t.Errorf("Confidence = %v, want 0.91", ev.Confidence)
	}
	if len(ev.RequiredCapabilities) != 2 || ev.RequiredCapabilities[0] != "vision" || ev.RequiredCapabilities[1] != "tool_use" {
		t.Errorf("RequiredCapabilities = %v, want [vision tool_use]", ev.RequiredCapabilities)
	}
	if ev.RequestKind != "title" {
		t.Errorf("RequestKind = %q, want title", ev.RequestKind)
	}
}

// The counterpart: when the router refuses for a reason that is NOT a
// classification verdict (the model named by the client is unknown), nothing
// was classified and the row must say so rather than inventing an empty
// capability set.
func TestUnclassifiedRoutingFailureLeavesCapabilitiesNil(t *testing.T) {
	w := &capturingRecorder{}
	p := NewPipeline(nil, fakeNormalizer{model: "not-a-real-model"}, fakeDenormalizer{}, nil,
		&fakeRouter{route: primaryRoute()}, &fakeUpstream{},
		testProviders(), nil, nil, nil, fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	_, err := p.Execute(context.Background(), []byte("hello"), "openai", "unknown", "")
	if err == nil {
		t.Fatal("Execute: expected an unknown model to be rejected, got nil")
	}
	if len(w.events) != 1 {
		t.Fatalf("events = %d, want exactly 1 refused request row", len(w.events))
	}
	if got := w.events[0].RequiredCapabilities; got != nil {
		t.Errorf("RequiredCapabilities = %v, want nil: no classifier ran, so this row must not claim one did", got)
	}
}
