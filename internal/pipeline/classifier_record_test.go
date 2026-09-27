package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cnf/arbiter/internal/classifier"
	"github.com/cnf/arbiter/internal/store"
	"github.com/cnf/arbiter/pkg/types"
)

// fakeCallingClassifier always fills Domain and reports one ClassifierCall,
// standing in for an LLMClassifier without needing a real upstream in these
// pipeline-level tests (LLMClassifier itself is tested directly in
// internal/classifier). input/prompt are what the fake claims it classified and
// was asked, so a test can assert the pipeline carried them into the store.
type fakeCallingClassifier struct {
	calls  int
	input  string
	prompt string
	// axes, when set, is what the call claims it filled (a multi-axis decisions
	// call). Left nil, the pipeline must fall back to the merged sig.
	axes map[string]string
	// axisConfidence is what the call claims it was sure of, per axis.
	axisConfidence map[string]float64
	// verdict, when set, stands in for a classifier that supplies its own
	// rationale wording.
	verdict string
}

func (f *fakeCallingClassifier) Classify(context.Context, *types.NormalizedRequest) (types.Signals, error) {
	f.calls++
	// Confidence 1.0 like a real verdict: the merge only fills an axis when the
	// score is strictly above zero, so a fake reporting 0 would fill nothing —
	// see TestMergedClassifierZeroConfidenceFillsNoAxis.
	return types.Signals{
		Domain:     "code_generation",
		Confidence: 1.0,
		ClassifierCalls: []*types.ClassifierCallInfo{
			{
				Provider: "cls-provider", Model: "cls-model", StatusCode: 200,
				RawReply: "code_generation", Input: f.input, SystemPrompt: f.prompt,
				Axes: f.axes, AxisConfidence: f.axisConfidence, Verdict: f.verdict,
			},
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
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

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
			if !ev.ArrivalTs.IsZero() {
				t.Errorf("classifier event ArrivalTs = %v, want zero (only the parent client event carries arrival)", ev.ArrivalTs)
			}
		case "client", "":
			clientEvents++
			if ev.ArrivalTs.IsZero() {
				t.Error("client event ArrivalTs is zero, want Execute's start time")
			}
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

// TestClassifierCallContentIsCaptured proves the classifier row carries what it
// saw: the text it classified as a user block and the prompt it was given as a
// system block. Without the input, a wrong verdict cannot be read against what
// produced it — the reply alone shows the decision, not the evidence.
func TestClassifierCallContentIsCaptured(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	w := &capturingWriter{}
	fc := &fakeCallingClassifier{input: "please fix this bug", prompt: "Classify the user's message."}
	p := NewPipeline(
		nil, fakeNormalizer{model: ""}, fakeDenormalizer{},
		[]classifier.Classifier{fc}, &fakeRouter{route: primaryRoute()}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)
	p.SetCaptureContent(true)

	if _, err := p.Execute(context.Background(), []byte("please fix this bug in the parser"), "openai", "t1", "chat-1"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var cls *store.Event
	for i := range w.events {
		if w.events[i].Kind == "classifier" {
			cls = &w.events[i]
		}
	}
	if cls == nil {
		t.Fatal("no classifier event recorded")
	}
	if cls.Content == nil {
		t.Fatal("classifier event carries no content; the input it classified is unrecorded")
	}

	var prompt, input string
	for _, b := range cls.Content.Request {
		switch b.Role {
		case "system":
			prompt = string(b.Body)
		case "user":
			input = string(b.Body)
		}
	}
	if input != "please fix this bug" {
		t.Errorf("captured input = %q, want the text the classifier saw", input)
	}
	if prompt != "Classify the user's message." {
		t.Errorf("captured prompt = %q, want the prompt the classifier was given", prompt)
	}
}

// TestClassifierRationaleNamesTheInput proves the recorded rationale carries
// what was classified, not just the verdict: the request list shows the
// rationale before anyone loads captured content, so "replied code_generation"
// with no input is unreadable as evidence.
func TestClassifierRationaleNamesTheInput(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	w := &capturingWriter{}
	fc := &fakeCallingClassifier{input: "please fix this bug", prompt: "Classify."}
	p := NewPipeline(
		nil, fakeNormalizer{model: ""}, fakeDenormalizer{},
		[]classifier.Classifier{fc}, &fakeRouter{route: primaryRoute()}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	if _, err := p.Execute(context.Background(), []byte("please fix this bug in the parser"), "openai", "t1", "chat-1"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var rationale string
	for _, ev := range w.events {
		if ev.Kind == "classifier" {
			rationale = ev.RoutingRationale
		}
	}
	if !strings.Contains(rationale, `replied "code_generation"`) {
		t.Errorf("rationale = %q, want the verdict", rationale)
	}
	if !strings.Contains(rationale, "please fix this bug") {
		t.Errorf("rationale = %q, want the classified input named alongside the verdict", rationale)
	}
}

// TestClassifierRowCarriesEveryAxisTheCallFilled is the regression test for a
// real report: a two-question decisions classifier answered cost_class AND
// domain, the rationale named both, but the classifier's own row left
// cost_class empty — the row read only the MERGED sig.Domain, so every other
// axis a multi-axis call filled was stored as nothing.
func TestClassifierRowCarriesEveryAxisTheCallFilled(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	w := &capturingWriter{}
	fc := &fakeCallingClassifier{
		input: "please fix this bug", prompt: "Pick the category.",
		axes:           map[string]string{"domain": "code_generation", "cost_class": "budget"},
		axisConfidence: map[string]float64{"domain": 0.98, "cost_class": 0.91},
		verdict: `decisions classifier answered domain="code_generation" (0.98), ` +
			`cost_class="budget" (0.91)`,
	}
	p := NewPipeline(
		nil, fakeNormalizer{model: ""}, fakeDenormalizer{},
		[]classifier.Classifier{fc}, &fakeRouter{route: primaryRoute()}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	if _, err := p.Execute(context.Background(), []byte("please fix this bug in the parser"), "openai", "t1", "chat-1"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var cls *store.Event
	for i := range w.events {
		if w.events[i].Kind == "classifier" {
			cls = &w.events[i]
		}
	}
	if cls == nil {
		t.Fatal("no classifier event recorded")
	}
	if cls.Domain != "code_generation" {
		t.Errorf("classifier row domain = %q, want code_generation", cls.Domain)
	}
	// The bug: this was empty, so cost_class was visible in the rationale text
	// and unqueryable as a field.
	if cls.CostClass != "budget" {
		t.Errorf("classifier row cost_class = %q, want budget — a call's own axes must be recorded, not just the merged domain", cls.CostClass)
	}
	// And the other half of the same report: the row's confidence was written as
	// nothing and read back as 0.0%, even though the call knew it was 0.98/0.91.
	if cls.Confidence != 0.98 {
		t.Errorf("classifier row confidence = %v, want the call's highest per-axis value 0.98", cls.Confidence)
	}
}

// A classifier that reports no per-call axes (the LLM classifier's one-axis
// case) still gets the merged sig written, so nothing that existed before
// per-call axes changes what it records.
func TestClassifierRowFallsBackToMergedAxes(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	w := &capturingWriter{}
	fc := &fakeCallingClassifier{input: "hi", prompt: "Classify."} // no Axes set
	p := NewPipeline(
		nil, fakeNormalizer{model: ""}, fakeDenormalizer{},
		[]classifier.Classifier{fc}, &fakeRouter{route: primaryRoute()}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	if _, err := p.Execute(context.Background(), []byte("please fix this bug in the parser"), "openai", "t1", "chat-1"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for _, ev := range w.events {
		if ev.Kind == "classifier" {
			if ev.Domain != "code_generation" {
				t.Errorf("classifier row domain = %q, want the merged sig's code_generation", ev.Domain)
			}
			return
		}
	}
	t.Fatal("no classifier event recorded")
}

// TestEllipsizeCutsOnRuneBoundary is the reason the helper exists rather than a
// plain slice: cutting at a byte offset would split a multi-byte rune and put
// invalid UTF-8 into the store and onto the page.
func TestEllipsizeCutsOnRuneBoundary(t *testing.T) {
	// Each "é" is 2 bytes, so a 5-byte budget lands mid-rune without the fix.
	s := strings.Repeat("é", 10)
	got := ellipsize(s, 5)
	if !utf8.ValidString(got) {
		t.Errorf("ellipsize produced invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("ellipsize = %q, want a cut marker so a preview isn't mistaken for the whole message", got)
	}
	if len(got) > 5+len("…") {
		t.Errorf("ellipsize = %q (%d bytes), want at most the budget plus the marker", got, len(got))
	}

	// A string under the budget is returned untouched — no marker, no change.
	if got := ellipsize("short", 120); got != "short" {
		t.Errorf("ellipsize = %q, want the input unchanged", got)
	}
}

// TestClassifierCallContentIsGatedOnCapture proves the input rides the same
// storage.capture_content switch as every other path that writes conversation
// text to disk — a second switch for derived calls is a switch nobody keeps in
// sync, and "no content" and "capture off" must not become two different empty
// states on one page.
func TestClassifierCallContentIsGatedOnCapture(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	w := &capturingWriter{}
	fc := &fakeCallingClassifier{input: "please fix this bug", prompt: "Classify."}
	p := NewPipeline(
		nil, fakeNormalizer{model: ""}, fakeDenormalizer{},
		[]classifier.Classifier{fc}, &fakeRouter{route: primaryRoute()}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	if _, err := p.Execute(context.Background(), []byte("please fix this bug in the parser"), "openai", "t1", "chat-1"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for _, ev := range w.events {
		if ev.Kind == "classifier" && ev.Content != nil {
			t.Errorf("classifier content = %+v, want nil with capture off", ev.Content)
		}
	}
}
