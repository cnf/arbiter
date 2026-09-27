package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/router"
	"github.com/cnf/arbiter/internal/store"
	"github.com/cnf/arbiter/internal/upstream"
	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// capturingWriter records every Event in memory so a test can assert what the
// pipeline sent to the store, without a database.
type capturingWriter struct {
	mu     sync.Mutex
	events []store.Event
}

func (w *capturingWriter) Record(ev store.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, ev)
}

func (w *capturingWriter) last() (store.Event, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.events) == 0 {
		return store.Event{}, false
	}
	return w.events[len(w.events)-1], true
}

// streamingNormalizer is fakeNormalizer with Stream=true, so Execute takes the
// streaming path. The shared fake leaves Stream false.
type streamingNormalizer struct{ model string }

func (streamingNormalizer) Detect([]byte) (string, error) { return "openai", nil }
func (n streamingNormalizer) ToNormalized(payload []byte, _ string) (*types.NormalizedRequest, error) {
	return &types.NormalizedRequest{
		Model:    n.model,
		Stream:   true,
		Messages: []types.Message{{Role: "user", Content: []types.ContentBlock{types.TextBlock(string(payload))}}},
	}, nil
}

// catalogLookup prices mock models so cost computation can be exercised.
func catalogLookup() router.CostLatencyLookup {
	return router.NewStaticCatalog([]types.ModelCost{
		{Provider: "primary", Model: "m-primary", InputCostPerMTok: 3, OutputCostPerMTok: 15},
	})
}

// TestExecuteRecordsCompletedRequest is the load-bearing write-path test: a
// successful non-streaming request must produce exactly one store event with
// the served route, usage, status and a cost computed from the catalog when
// the upstream reported none.
func TestExecuteRecordsCompletedRequest(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{
		Usage:   types.Usage{InputTokens: 1000, OutputTokens: 200},
		Content: []types.ContentBlock{{Type: "tool_use", ToolName: "read_file"}},
	}}
	w := &capturingWriter{}
	p := NewPipeline(
		nil, fakeNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, catalogLookup(), nil, nil)

	before := time.Now()
	if _, err := p.Execute(context.Background(), []byte("hello"), "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	after := time.Now()

	ev, ok := w.last()
	if !ok {
		t.Fatal("no event recorded")
	}
	// ArrivalTs must be Execute's own start — bounded by this call. (Ts
	// itself is stamped by the real SQLiteWriter.Record, not by this test's
	// capturingWriter, so it stays zero here and isn't compared against.)
	if ev.ArrivalTs.Before(before) || ev.ArrivalTs.After(after) {
		t.Errorf("ArrivalTs = %v, want between %v and %v", ev.ArrivalTs, before, after)
	}
	if ev.TraceID != "t1" || ev.Provider != "primary" || ev.Model != "m-primary" {
		t.Errorf("identity fields wrong: %+v", ev)
	}
	if ev.StatusCode != 200 || ev.Error != "" || ev.Stream {
		t.Errorf("status/error/stream wrong: %+v", ev)
	}
	if ev.Usage.InputTokens != 1000 || ev.Usage.OutputTokens != 200 {
		t.Errorf("usage not carried through: %+v", ev.Usage)
	}
	// 1000 in * $3/MTok + 200 out * $15/MTok = 0.003 + 0.003 = 0.006
	if ev.Usage.CostUSD < 0.00599 || ev.Usage.CostUSD > 0.00601 {
		t.Errorf("CostUSD = %v, want ~0.006 computed from the catalog", ev.Usage.CostUSD)
	}
	if len(ev.ToolCalls) != 1 || ev.ToolCalls[0] != "read_file" {
		t.Errorf("ToolCalls = %v, want [read_file]", ev.ToolCalls)
	}
}

// TestExecuteRecordsActualModelWhenItDiverges proves a meta-router alias
// (OpenRouter's "openrouter/auto" being the motivating case) that reports a
// different model than Arbiter routed to gets that divergence captured —
// served.Model is what Arbiter asked for, ActualModel is what the upstream's
// own response body said it used.
func TestExecuteRecordsActualModelWhenItDiverges(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{Model: "anthropic/claude-3.5-sonnet"}}
	w := &capturingWriter{}
	p := NewPipeline(
		nil, fakeNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	if _, err := p.Execute(context.Background(), []byte("hello"), "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	ev, ok := w.last()
	if !ok {
		t.Fatal("no event recorded")
	}
	if ev.Model != "m-primary" {
		t.Errorf("Model = %q, want the routed model m-primary", ev.Model)
	}
	if ev.ActualModel != "anthropic/claude-3.5-sonnet" {
		t.Errorf("ActualModel = %q, want the upstream-reported model", ev.ActualModel)
	}
}

// TestExecuteLeavesActualModelEmptyWhenItMatches proves the common case (a
// plain provider that just echoes back the requested model) doesn't produce
// redundant noise — ActualModel stays empty rather than duplicating Model.
func TestExecuteLeavesActualModelEmptyWhenItMatches(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{Model: "m-primary"}}
	w := &capturingWriter{}
	p := NewPipeline(
		nil, fakeNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	if _, err := p.Execute(context.Background(), []byte("hello"), "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	ev, ok := w.last()
	if !ok {
		t.Fatal("no event recorded")
	}
	if ev.ActualModel != "" {
		t.Errorf("ActualModel = %q, want empty when it matches the routed model", ev.ActualModel)
	}
}

// TestExecuteRecordsHeadersFromContext proves headers attached via
// WithHeaders reach the recorded event — the HTTP layer sets these on ctx
// rather than as an Execute parameter (see WithHeaders' doc comment), so this
// is the only thing proving the plumbing actually connects end to end.
func TestExecuteRecordsHeadersFromContext(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	w := &capturingWriter{}
	p := NewPipeline(
		nil, fakeNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	ctx := WithHeaders(context.Background(), map[string]string{"User-Agent": "opencode/1.0"})
	if _, err := p.Execute(ctx, []byte("hello"), "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	ev, ok := w.last()
	if !ok {
		t.Fatal("no event recorded")
	}
	if ev.Headers["User-Agent"] != "opencode/1.0" {
		t.Errorf("Headers[User-Agent] = %q, want opencode/1.0", ev.Headers["User-Agent"])
	}
}

// TestRecordStampsConfigEpoch proves every recorded event carries the config
// epoch the pipeline was told about — this is what makes per-epoch cost
// comparison possible at all. The epoch must reach the event without any
// record call site knowing about it.
func TestRecordStampsConfigEpoch(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{Usage: types.Usage{InputTokens: 10}}}
	w := &capturingWriter{}
	p := NewPipeline(
		nil, fakeNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)
	p.SetConfigEpoch("epoch-abc123")

	if _, err := p.Execute(context.Background(), []byte("hello"), "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	ev, ok := w.last()
	if !ok {
		t.Fatal("no event recorded")
	}
	if ev.ConfigEpoch != "epoch-abc123" {
		t.Fatalf("ConfigEpoch = %q, want epoch-abc123", ev.ConfigEpoch)
	}
}

// An unset epoch must stay empty rather than being invented — a bare test
// pipeline carries no config identity, and NULL is the honest representation.
func TestRecordLeavesEpochEmptyWhenUnset(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{Usage: types.Usage{InputTokens: 10}}}
	w := &capturingWriter{}
	p := NewPipeline(
		nil, fakeNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	if _, err := p.Execute(context.Background(), []byte("hello"), "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if ev, ok := w.last(); !ok || ev.ConfigEpoch != "" {
		t.Fatalf("ConfigEpoch = %q, want empty", ev.ConfigEpoch)
	}
}

// TestUpstreamReportedCostIsNotOverwritten proves a provider-reported cost
// (OpenRouter) is authoritative: the catalog is not consulted over it.
func TestUpstreamReportedCostIsNotOverwritten(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{
		Usage: types.Usage{InputTokens: 1000, OutputTokens: 200, CostUSD: 0.42},
	}}
	w := &capturingWriter{}
	p := NewPipeline(
		nil, fakeNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, catalogLookup(), nil, nil)

	if _, err := p.Execute(context.Background(), []byte("hello"), "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	ev, _ := w.last()
	if ev.Usage.CostUSD != 0.42 {
		t.Errorf("CostUSD = %v, want the upstream-reported 0.42", ev.Usage.CostUSD)
	}
}

// TestUnknownCostStaysZero proves a model absent from the catalog records no
// cost rather than a fabricated one.
func TestUnknownCostStaysZero(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{
		Usage: types.Usage{InputTokens: 1000, OutputTokens: 200},
	}}
	w := &capturingWriter{}
	p := NewPipeline(
		nil, fakeNormalizer{model: "m-fallback1"}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, catalogLookup(), nil, nil)

	if _, err := p.Execute(context.Background(), []byte("hello"), "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	ev, _ := w.last()
	if ev.Usage.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0 for a model with no catalog row", ev.Usage.CostUSD)
	}
}

// TestRecordedOnUpstreamFailure proves a failed request is still recorded,
// carrying the upstream status and the error text — the failures are exactly
// what the store exists to make queryable.
func TestRecordedOnUpstreamFailure(t *testing.T) {
	fu := &fakeUpstream{sendErr: map[string]error{
		"primary": arbitererrors.NewUpstreamError("primary", 400, "bad request", nil),
	}}
	w := &capturingWriter{}
	p := NewPipeline(
		nil, fakeNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	if _, err := p.Execute(context.Background(), []byte("hello"), "openai", "t1", ""); err == nil {
		t.Fatal("expected Execute to fail")
	}

	ev, ok := w.last()
	if !ok {
		t.Fatal("failed request was not recorded")
	}
	if ev.ArrivalTs.IsZero() {
		t.Error("ArrivalTs is zero, want Execute's start time even on failure")
	}
	if ev.StatusCode != 400 {
		t.Errorf("StatusCode = %d, want the upstream 400", ev.StatusCode)
	}
	if ev.Error == "" {
		t.Error("Error empty, want the failure text recorded")
	}
}

// TestExecuteStreamRecordsCompletedRequest proves the stream write path
// records after the event channel drains, carrying the accumulated usage and
// stream=true.
func TestExecuteStreamRecordsCompletedRequest(t *testing.T) {
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	w := &capturingWriter{}
	p := NewPipeline(
		nil, streamingNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	out, err := p.Execute(context.Background(), []byte("stream please"), "openai", "t1", "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sr, ok := out.(*upstream.StreamResponse)
	if !ok {
		t.Fatalf("stream Execute returned %T, want *upstream.StreamResponse", out)
	}
	for range sr.EventChan {
	}

	// Recording happens in the forwarding goroutine after the drain; poll
	// briefly rather than racing the goroutine's final steps.
	ev, ok := waitForEvent(w)
	if !ok {
		t.Fatal("streamed request was not recorded")
	}
	if ev.ArrivalTs.IsZero() {
		t.Error("ArrivalTs is zero, want Execute's start time on a streamed request")
	}
	if !ev.Stream {
		t.Error("Stream = false, want true for a streamed request")
	}
	if ev.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200 on a clean stream", ev.StatusCode)
	}
}

// TestExecuteStreamRecordsMidStreamFailure proves the stream path does not
// assume 200: when the read fails after events have flowed, the recorded row
// carries the failure status and error text.
func TestExecuteStreamRecordsMidStreamFailure(t *testing.T) {
	fu := &fakeUpstream{
		resp:          &types.NormalizedResponse{},
		streamTermErr: arbitererrors.NewUpstreamError("primary", 502, "upstream connection reset", nil),
	}
	w := &capturingWriter{}
	p := NewPipeline(
		nil, streamingNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, fu, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	out, err := p.Execute(context.Background(), []byte("stream please"), "openai", "t1", "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sr, ok := out.(*upstream.StreamResponse)
	if !ok {
		t.Fatalf("stream Execute returned %T, want *upstream.StreamResponse", out)
	}
	for range sr.EventChan {
	}

	ev, ok := waitForEvent(w)
	if !ok {
		t.Fatal("failed stream was not recorded")
	}
	if ev.ArrivalTs.IsZero() {
		t.Error("ArrivalTs is zero, want Execute's start time even on a mid-stream failure")
	}
	if ev.StatusCode == 200 {
		t.Errorf("StatusCode = 200 for a stream that failed mid-flight, want the failure status")
	}
	if ev.Error == "" {
		t.Error("Error empty, want the mid-stream failure text recorded")
	}
}

// waitForEvent polls for up to a second, since the stream write path records
// in a goroutine that finishes just after its channel closes.
func waitForEvent(w *capturingWriter) (store.Event, bool) {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if ev, ok := w.last(); ok {
			return ev, true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return store.Event{}, false
}
