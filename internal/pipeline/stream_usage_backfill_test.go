package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/upstream"
	"github.com/cnf/arbiter/pkg/types"
)

// sequenceUpstream emits a fixed, caller-supplied sequence of stream events —
// unlike fakeUpstream's single hardcoded message_stop, this lets a test
// reproduce Anthropic's real event order (message_start carrying
// InputTokens/cache counters, message_delta carrying only OutputTokens) and
// assert what a CLIENT actually receives at the end of the pipe, not just
// what the stored row ends up with.
type sequenceUpstream struct {
	events []*types.NormalizedStreamEvent
}

func (u *sequenceUpstream) Send(context.Context, types.Route, *types.NormalizedRequest) (*types.NormalizedResponse, error) {
	return &types.NormalizedResponse{}, nil
}

func (u *sequenceUpstream) SendStream(context.Context, types.Route, *types.NormalizedRequest) (<-chan *types.NormalizedStreamEvent, <-chan error, error) {
	ch := make(chan *types.NormalizedStreamEvent, len(u.events))
	for _, e := range u.events {
		ch <- e
	}
	close(ch)
	errCh := make(chan error, 1)
	errCh <- nil
	return ch, errCh, nil
}

// TestStreamedAnthropicUsageBackfillsInputAndCacheOntoTerminalChunk pins the
// #27 fix's second half: Anthropic's message_start carries InputTokens and
// both cache counters, but the event an OpenAI-format client actually reads
// Usage from — message_delta — carries only OutputTokens on the real wire.
// Without a backfill, the client-visible chunk showed prompt_tokens=0 and no
// cache breakdown regardless of what message_start reported; a naive fix
// that added only the cache field would have made this WORSE (a real cache
// count divided by a fabricated zero prompt count).
//
// This drives the real pipeline goroutine, not a hand-built accumulator: the
// events pass through Execute exactly as internal/upstream's SSE reader
// would deliver them, and the assertion is on what emerges from
// EventChan — the same channel internal/http/handler.go relays to the wire.
func TestStreamedAnthropicUsageBackfillsInputAndCacheOntoTerminalChunk(t *testing.T) {
	u := &sequenceUpstream{events: []*types.NormalizedStreamEvent{
		{Type: "message_start", InputTokens: 2, CacheReadTokens: 143223, CacheWriteTokens: 73},
		{Type: "content_block_delta", TextDelta: "hi"},
		{Type: "message_delta", OutputTokens: 264, MessageStopReason: "end_turn"},
		{Type: "message_stop"},
	}}
	w := &capturingWriter{}
	p := NewPipeline(
		nil, streamingNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, u, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	out, err := p.Execute(context.Background(), []byte("stream please"), "openai", "t1", "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sr, ok := out.(*upstream.StreamResponse)
	if !ok {
		t.Fatalf("stream Execute returned %T, want *upstream.StreamResponse", out)
	}

	var deltaEvt *types.NormalizedStreamEvent
	for evt := range sr.EventChan {
		if evt.Type == "message_delta" {
			deltaEvt = evt
		}
	}
	if deltaEvt == nil {
		t.Fatal("no message_delta event reached the client-facing channel")
	}
	if deltaEvt.InputTokens != 2 {
		t.Errorf("client-visible message_delta.InputTokens = %d, want 2 (backfilled from message_start)", deltaEvt.InputTokens)
	}
	if deltaEvt.CacheReadTokens != 143223 {
		t.Errorf("client-visible message_delta.CacheReadTokens = %d, want 143223", deltaEvt.CacheReadTokens)
	}
	if deltaEvt.CacheWriteTokens != 73 {
		t.Errorf("client-visible message_delta.CacheWriteTokens = %d, want 73", deltaEvt.CacheWriteTokens)
	}
	if deltaEvt.OutputTokens != 264 {
		t.Errorf("client-visible message_delta.OutputTokens = %d, want 264 (must not be clobbered by the backfill)", deltaEvt.OutputTokens)
	}

	// The stored row must show the same totals — the backfill must not have
	// come at the expense of what gets recorded.
	ev, ok := waitForEvent(w)
	if !ok {
		t.Fatal("streamed request was not recorded")
	}
	if ev.Usage.InputTokens != 2 || ev.Usage.CacheRead != 143223 || ev.Usage.CacheWrite != 73 || ev.Usage.OutputTokens != 264 {
		t.Errorf("stored usage = %+v, want InputTokens=2 CacheRead=143223 CacheWrite=73 OutputTokens=264", ev.Usage)
	}
}
