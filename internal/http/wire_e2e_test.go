package http

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cnf/arbiter/internal/translator"
	"github.com/cnf/arbiter/internal/upstream"
	"github.com/cnf/arbiter/pkg/types"
)

// anthropicUpstreamSSE is a byte-faithful transcription of what an Anthropic
// upstream sends for a short thinking-then-text reply — including the two
// events a proxy is most likely to mishandle: a thinking block carrying its
// signature, and a message_delta carrying the stop_reason.
const anthropicUpstreamSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01ABC","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":100,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"ErcBCkgI"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":" there"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":12}}

event: message_stop
data: {"type":"message_stop"}

`

// parseUpstreamAnthropic replicates the upstream client's SSE handling exactly:
// readSSEStream skips any line that is not `data: `, unmarshals the payload into
// an AnthropicStreamEvent, and hands it to the exported translator. Keeping the
// replication here lets one test cover the parse and the write in a single
// package.
func parseUpstreamAnthropic(t *testing.T, raw string) []*types.NormalizedStreamEvent {
	t.Helper()
	var out []*types.NormalizedStreamEvent
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}
		var evt translator.AnthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			t.Fatalf("upstream event failed to unmarshal: %v\n%s", err, data)
		}
		if n := translator.AnthropicStreamEventToNormalized(&evt); n != nil {
			out = append(out, n)
		}
	}
	return out
}

// TestAnthropicUpstreamToClientWireEndToEnd drives the real parser and the real
// streaming writer, and pins the exact frames an Anthropic client receives.
//
// This is the seam neither the translator tests nor the framing test covered:
// an event that parses correctly upstream but reaches the client in a shape its
// SDK cannot accumulate is invisible to both.
func TestAnthropicUpstreamToClientWireEndToEnd(t *testing.T) {
	normalized := parseUpstreamAnthropic(t, anthropicUpstreamSSE)
	if len(normalized) == 0 {
		t.Fatal("upstream parser produced no events")
	}

	ch := make(chan *types.NormalizedStreamEvent, len(normalized))
	for _, e := range normalized {
		ch <- e
	}
	close(ch)

	h := NewHandler(nil, nil)
	w := httptest.NewRecorder()
	h.handleStream(context.Background(), w, "trace-e2e", &upstream.StreamResponse{EventChan: ch}, "anthropic")
	out := w.Body.String()

	t.Logf("CLIENT WIRE:\n%s", out)

	// 1. Every frame must be named, and named what its payload says.
	var events []string
	for _, frame := range strings.Split(out, "\n\n") {
		frame = strings.TrimSpace(frame)
		if frame == "" {
			continue
		}
		lines := strings.Split(frame, "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "event: ") {
			t.Errorf("frame is not `event: X` + `data: Y`: %q", frame)
			continue
		}
		name := strings.TrimPrefix(lines[0], "event: ")
		events = append(events, name)
		if !strings.Contains(lines[1], `"type":"`+name+`"`) {
			t.Errorf("event name %q does not match payload type: %s", name, lines[1])
		}
	}

	// 2. The client must see both blocks opened, streamed, and closed.
	want := []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"message_delta", "message_stop",
	}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Errorf("event sequence:\n got %v\nwant %v", events, want)
	}

	// 3. The stop reason must reach the client. A client that never learns why
	// the turn ended leaves its UI spinning after the text has been shown.
	if !strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Errorf("stop_reason lost — the turn never terminates for the client:\n%s", out)
	}

	// 4. Both blocks must carry a real index, block 0 included.
	for _, wantIdx := range []string{`"index":0`, `"index":1`} {
		if !strings.Contains(out, wantIdx) {
			t.Errorf("missing %s on the wire:\n%s", wantIdx, out)
		}
	}

	// 5. No null frame, and no delta whose text value is literally null — a
	// client concatenating onto such a field renders `undefined`.
	if strings.Contains(out, "data: null") {
		t.Errorf("a `data: null` frame reached the client:\n%s", out)
	}
	if strings.Contains(out, `"text":null`) || strings.Contains(out, `"thinking":null`) {
		t.Errorf("a delta carries a literal null text/thinking value:\n%s", out)
	}
}

// A thinking block's delta must go out as `thinking_delta` and a text block's
// as `text_delta`. The SDK accumulates each onto the matching field of the
// block it already opened; a delta type does not itself create that field.
func TestDeltaTypesForThinkingAndText(t *testing.T) {
	thinking := translator.NormalizedToAnthropicStreamEvent(&types.NormalizedStreamEvent{
		Type: "content_block_delta", BlockIndex: 0, DeltaType: "reasoning_delta", Reasoning: "r",
	})
	if thinking.Delta == nil || thinking.Delta.Type != "thinking_delta" {
		t.Errorf("reasoning delta emitted as %+v, want thinking_delta", thinking.Delta)
	}
	text := translator.NormalizedToAnthropicStreamEvent(&types.NormalizedStreamEvent{
		Type: "content_block_delta", BlockIndex: 1, DeltaType: "text_delta", TextDelta: "t",
	})
	if text.Delta == nil || text.Delta.Type != "text_delta" || text.Delta.Text != "t" {
		t.Errorf("text delta emitted as %+v, want text_delta with text", text.Delta)
	}
}
