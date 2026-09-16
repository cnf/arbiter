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

// capturingRecorder records both normal events and rejections, which is what a
// real SQLiteWriter does. Adding the rejection method here is deliberate: the
// pipeline type-asserts store.ContentRecorder, so a writer without it would
// silently stop capturing rejections, and this fake would hide that.
type capturingRecorder struct {
	events    []store.Event
	rejected  []store.CapturedContent
	rejectIDs []int64
}

func (w *capturingRecorder) Record(ev store.Event) {
	w.events = append(w.events, ev)
}

func (w *capturingRecorder) RecordRejected(id int64, c store.CapturedContent) {
	w.rejectIDs = append(w.rejectIDs, id)
	w.rejected = append(w.rejected, c)
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
		fakeLogger{}, time.Minute, nil, w, nil,
	)
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
		fakeLogger{}, time.Minute, nil, w, nil,
	)

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
		fakeLogger{}, time.Minute, nil, w, nil,
	)
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
		fakeLogger{}, time.Minute, nil, w, nil,
	)
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
		fakeLogger{}, time.Minute, nil, w, nil,
	)

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

// TestRejectedRequestContentIsRecorded proves the (b) decision at the pipeline
// level: a request a pre-guardrail refuses still gets its content stored, under
// a rejection id, so "why was this refused" has something to show.
func TestRejectedRequestContentIsRecorded(t *testing.T) {
	fu := &fakeUpstream{}
	w := &capturingRecorder{}
	p := NewPipeline(
		nil, eavesdropNormalizer{}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil,
		[]guardrail.Guardrail{rejectingGuardrail{}}, nil,
		fakeLogger{}, time.Minute, nil, w, nil,
	)
	p.SetCaptureContent(true)

	_, err := p.Execute(context.Background(), []byte("a request that will be refused"), "openai", "t-reject", "")
	if err == nil {
		t.Fatal("Execute returned nil error, want the guardrail rejection")
	}

	if len(w.rejected) != 1 {
		t.Fatalf("rejections recorded = %d, want 1", len(w.rejected))
	}
	if len(w.events) != 0 {
		t.Errorf("events recorded = %d, want 0 (a rejected request gets no request row)", len(w.events))
	}
	blocks := w.rejected[0].Request
	if len(blocks) == 0 {
		t.Fatal("no content captured for the rejection")
	}
	var found bool
	for _, b := range blocks {
		if string(b.Body) == "a request that will be refused" {
			found = true
		}
	}
	if !found {
		t.Errorf("rejected content = %+v, want the user's text", blocks)
	}

	// The rejection id is derived from the trace id, so it is stable for this
	// request and distinct from other rejections.
	if w.rejectIDs[0] == 0 {
		t.Error("rejection id is 0; a stable non-zero owner id is required")
	}
	other := rejectionID("t-reject")
	if w.rejectIDs[0] != other {
		t.Errorf("rejection id = %d, want the trace-derived %d", w.rejectIDs[0], other)
	}
	if rejectionID("t-other") == other {
		t.Error("different trace ids produced the same rejection id")
	}
}

// TestRejectedContentIsNotCapturedWhenCaptureOff keeps the rejection path
// consistent with the normal one: no capture means no writes anywhere.
func TestRejectedContentIsNotCapturedWhenCaptureOff(t *testing.T) {
	w := &capturingRecorder{}
	p := NewPipeline(
		nil, eavesdropNormalizer{}, fakeDenormalizer{},
		nil, &fakeRouter{}, &fakeUpstream{}, testProviders(), nil,
		[]guardrail.Guardrail{rejectingGuardrail{}}, nil,
		fakeLogger{}, time.Minute, nil, w, nil,
	)

	if _, err := p.Execute(context.Background(), []byte("refused"), "openai", "t1", ""); err == nil {
		t.Fatal("Execute returned nil error, want a rejection")
	}
	if len(w.rejected) != 0 {
		t.Errorf("rejections recorded = %d, want 0 with capture off", len(w.rejected))
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
