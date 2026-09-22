package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/guardrail"
	"github.com/cnf/arbiter/internal/store"
	"github.com/cnf/arbiter/internal/upstream"
	"github.com/cnf/arbiter/pkg/types"
)

// capturingRecorder records events, standing in for a real SQLiteWriter.
type capturingRecorder struct {
	events []store.Event
}

func (w *capturingRecorder) Record(ev store.Event) {
	w.events = append(w.events, ev)
}

// eavesdropNormalizer returns a request with a client-supplied system prompt and
// a user message, so a test can tell whether capture saw the client's text or a
// guardrail's rewrite.
type eavesdropNormalizer struct{}

func (eavesdropNormalizer) Detect([]byte) (string, error) { return "openai", nil }
func (eavesdropNormalizer) ToNormalized(payload []byte, _ string) (*types.NormalizedRequest, error) {
	return &types.NormalizedRequest{
		Model:        "m-primary",
		SystemPrompt: "CLIENT SYSTEM PROMPT",
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{types.TextBlock(string(payload))}},
		},
	}, nil
}

// rewritingGuardrail stands in for the system_prompt guardrail: it replaces the
// client's system prompt with Arbiter's own, which is exactly the rewrite that
// must not be what ends up in the content store.
type rewritingGuardrail struct{}

func (rewritingGuardrail) Name() string { return "system_prompt" }
func (rewritingGuardrail) ShouldRun(*types.NormalizedRequest) bool {
	return true
}
func (rewritingGuardrail) ApplyPre(_ context.Context, req *types.NormalizedRequest) (*types.NormalizedRequest, error) {
	req.SystemPrompt = "ARBITER INJECTED PROMPT"
	return req, nil
}
func (rewritingGuardrail) ApplyPost(_ context.Context, resp *types.NormalizedResponse, _ types.Route) (*types.NormalizedResponse, error) {
	return resp, nil
}

// TestCaptureHappensBeforePreGuardrails is the correctness claim that matters
// most in this feature: if capture ran after a pre-guardrail, the store would
// hold Arbiter's injected prompt as though the client had sent it, and the whole
// "find the text the client prepends" payoff would collapse into showing our own
// system prompt back at us.
func TestCaptureHappensBeforePreGuardrails(t *testing.T) {
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

	var systemText string
	for _, b := range ev.Content.Request {
		if b.Role == "system" {
			systemText = string(b.Body)
		}
	}
	if systemText != "CLIENT SYSTEM PROMPT" {
		t.Errorf("captured system text = %q, want the CLIENT's prompt, not the guardrail's rewrite", systemText)
	}
	if systemText == "ARBITER INJECTED PROMPT" {
		t.Error("captured Arbiter's injected prompt — capture ran after pre-guardrails and the corpus is now polluted with our own text")
	}

	// The user text came through too.
	var userText string
	for _, b := range ev.Content.Request {
		if b.Role == "user" {
			userText = string(b.Body)
		}
	}
	if userText != "the real question" {
		t.Errorf("captured user text = %q, want %q", userText, "the real question")
	}
}

// TestCaptureOffRecordsNoContent proves the default stays inert: with capture
// disabled the recorded event carries no content and nothing is written, which
// is what makes enabling it a deliberate act.
func TestCaptureOffRecordsNoContent(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{
		Usage:   types.Usage{InputTokens: 5},
		Content: []types.ContentBlock{types.TextBlock("a response")},
	}}
	w := &capturingRecorder{}
	p := NewPipeline(
		nil, fakeNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	if _, err := p.Execute(context.Background(), []byte("hello"), "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	ev, ok := lastEvent(w)
	if !ok {
		t.Fatal("no event recorded")
	}
	if ev.Content != nil {
		t.Errorf("Content = %+v, want nil with capture off", ev.Content)
	}
}

// TestCaptureStoresRequestAndResponse proves both directions land on the event,
// and that the response is the one the client received (captured after
// post-guardrails) rather than the raw upstream payload.
func TestCaptureStoresRequestAndResponse(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{
		Usage:   types.Usage{InputTokens: 5},
		Content: []types.ContentBlock{types.TextBlock("the model's answer")},
	}}
	w := &capturingRecorder{}
	p := NewPipeline(
		nil, fakeNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)
	p.SetCaptureContent(true)

	if _, err := p.Execute(context.Background(), []byte("a question with substance"), "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	ev, ok := lastEvent(w)
	if !ok || ev.Content == nil {
		t.Fatal("no content captured")
	}
	if len(ev.Content.Request) == 0 {
		t.Error("no request blocks captured")
	}
	if len(ev.Content.Response) != 1 || string(ev.Content.Response[0].Body) != "the model's answer" {
		t.Errorf("response blocks = %+v, want the model's answer", ev.Content.Response)
	}
	if ev.Content.Response[0].Role != "assistant" {
		t.Errorf("response role = %q, want assistant", ev.Content.Response[0].Role)
	}
}

// TestCaptureStreamStoresRequestAndResponse proves the streaming path records
// BOTH halves of the capture. It used to record only the response — the request
// half, captured before pre-guardrails in Execute, was never carried into the
// streamed event — so every streamed row held a reply with no prompt, and a
// session transcript rendered as a list of answers to questions nobody asked.
func TestCaptureStreamStoresRequestAndResponse(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	w := &capturingRecorder{}
	p := NewPipeline(
		nil, streamingNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)
	p.SetCaptureContent(true)

	out, err := p.Execute(context.Background(), []byte("a question with substance"), "openai", "t1", "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sr, ok := out.(*upstream.StreamResponse)
	if !ok {
		t.Fatalf("stream Execute returned %T, want *upstream.StreamResponse", out)
	}
	for range sr.EventChan {
	}

	ev, ok := waitForRecorderEvent(w)
	if !ok {
		t.Fatal("streamed request was not recorded")
	}
	if ev.Content == nil {
		t.Fatal("no content captured on the streaming path")
	}
	if len(ev.Content.Request) == 0 {
		t.Error("no request blocks captured on a streamed request — the prompt half is missing")
	}
	// The normalizer turns the payload into the user turn, so the captured
	// request block must carry that text; it is what the transcript page shows
	// above the reply.
	if got := string(ev.Content.Request[0].Body); got != "a question with substance" {
		t.Errorf("request block = %q, want the client's own text", got)
	}
	if ev.Content.Request[0].Role != "user" {
		t.Errorf("request role = %q, want user", ev.Content.Request[0].Role)
	}
}

// TestCaptureStreamOffRecordsNoContent mirrors the non-streaming case: with
// capture disabled the event carries no content field at all, on either path.
func TestCaptureStreamOffRecordsNoContent(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	w := &capturingRecorder{}
	p := NewPipeline(
		nil, streamingNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	out, err := p.Execute(context.Background(), []byte("anything"), "openai", "t1", "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sr, _ := out.(*upstream.StreamResponse)
	for range sr.EventChan {
	}

	ev, ok := waitForRecorderEvent(w)
	if !ok {
		t.Fatal("streamed request was not recorded")
	}
	if ev.Content != nil {
		t.Errorf("Content = %+v, want nil when capture is off", ev.Content)
	}
}

// TestRejectedRequestContentIsRecorded proves the #5 decision at the
// pipeline level: a request a pre-guardrail refuses gets a real requests
// row (status_code + error set, kind stays "client"), with its content
// attached the normal way — not a content-only stub under a separate
// "rejected" owner kind, which would leave it invisible in the requests
// table, /admin/stats, and every cost aggregate.
func TestRejectedRequestContentIsRecorded(t *testing.T) {
	fu := &fakeUpstream{}
	w := &capturingRecorder{}
	p := NewPipeline(
		nil, eavesdropNormalizer{}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil,
		[]guardrail.Guardrail{rejectingGuardrail{}}, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)
	p.SetCaptureContent(true)

	_, err := p.Execute(context.Background(), []byte("a request that will be refused"), "openai", "t-reject", "")
	if err == nil {
		t.Fatal("Execute returned nil error, want the guardrail rejection")
	}

	if len(w.events) != 1 {
		t.Fatalf("events recorded = %d, want 1 — a refused request must still get a requests row", len(w.events))
	}
	ev := w.events[0]
	if ev.StatusCode == 0 {
		t.Error("status_code = 0, want the guardrail's real status")
	}
	if ev.Error == "" {
		t.Error("error = \"\", want the guardrail's rejection message")
	}
	if ev.Kind != "client" && ev.Kind != "" {
		t.Errorf("kind = %q, want \"client\" (empty defaults to client) — a refused request is still client traffic", ev.Kind)
	}
	if ev.Content == nil || len(ev.Content.Request) == 0 {
		t.Fatal("no content captured for the refused request")
	}
	var found bool
	for _, b := range ev.Content.Request {
		if string(b.Body) == "a request that will be refused" {
			found = true
		}
	}
	if !found {
		t.Errorf("captured content = %+v, want the user's text", ev.Content.Request)
	}
}

// TestRejectedContentIsNotCapturedWhenCaptureOff proves #5's row is
// unconditional: capture_content only ever gates the request/response
// bodies, never the row itself — "nothing invisible" holds even with
// capture off, so a refused request still gets a requests row with its
// status and error, just no Content attached.
func TestRejectedContentIsNotCapturedWhenCaptureOff(t *testing.T) {
	w := &capturingRecorder{}
	p := NewPipeline(
		nil, eavesdropNormalizer{}, fakeDenormalizer{},
		nil, &fakeRouter{}, &fakeUpstream{}, testProviders(), nil,
		[]guardrail.Guardrail{rejectingGuardrail{}}, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	if _, err := p.Execute(context.Background(), []byte("refused"), "openai", "t1", ""); err == nil {
		t.Fatal("Execute returned nil error, want a rejection")
	}
	if len(w.events) != 1 {
		t.Fatalf("events recorded = %d, want 1 — the row is unconditional, capture only gates Content", len(w.events))
	}
	if w.events[0].Content != nil {
		t.Errorf("Content = %+v, want nil with capture off", w.events[0].Content)
	}
}

// rejectingGuardrail fails ApplyPre, standing in for a rate limit or any other
// pre-request refusal.
type rejectingGuardrail struct{}

func (rejectingGuardrail) Name() string                            { return "rejector" }
func (rejectingGuardrail) ShouldRun(*types.NormalizedRequest) bool { return true }
func (rejectingGuardrail) ApplyPre(context.Context, *types.NormalizedRequest) (*types.NormalizedRequest, error) {
	return nil, errors.New("refused by guardrail")
}
func (rejectingGuardrail) ApplyPost(_ context.Context, resp *types.NormalizedResponse, _ types.Route) (*types.NormalizedResponse, error) {
	return resp, nil
}

func lastEvent(w *capturingRecorder) (store.Event, bool) {
	if len(w.events) == 0 {
		return store.Event{}, false
	}
	return w.events[len(w.events)-1], true
}

// waitForRecorderEvent polls for up to a second, since the stream write path
// records in a goroutine that finishes just after its channel closes. The
// capturingRecorder fake has no lock of its own (its tests are sequential), so
// this mirrors waitForEvent without asserting on the other writer type.
func waitForRecorderEvent(w *capturingRecorder) (store.Event, bool) {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if ev, ok := lastEvent(w); ok {
			return ev, true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return store.Event{}, false
}
