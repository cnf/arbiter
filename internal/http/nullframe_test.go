package http

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cnf/arbiter/internal/upstream"
	"github.com/cnf/arbiter/pkg/types"
)

// streamEvents returns the normalized sequence a Claude reply produces,
// including the two event types the translator now drops by returning nil
// (signature_delta from the upstream, content_block_stop on the outbound side).
func streamEvents() []*types.NormalizedStreamEvent {
	return []*types.NormalizedStreamEvent{
		{Type: "message_start", MessageModel: "claude-sonnet-5"},
		{Type: "content_block_start", BlockIndex: 0, BlockType: "thinking"},
		{Type: "content_block_delta", BlockIndex: 0, DeltaType: "reasoning_delta", Reasoning: "reasoning-text"},
		{Type: "content_block_delta", BlockIndex: 0, DeltaType: "signature_delta", Signature: "sig-token"},
		{Type: "content_block_stop", BlockIndex: 0},
		{Type: "content_block_start", BlockIndex: 1, BlockType: "text"},
		{Type: "content_block_delta", BlockIndex: 1, DeltaType: "text_delta", TextDelta: "text-payload"},
		{Type: "content_block_stop", BlockIndex: 1},
		{Type: "message_stop", MessageStopReason: "end_turn"},
	}
}

// A deliberately-dropped event must not reach the client as the JSON literal
// `null`.
//
// NormalizedTo*StreamEvent returns nil for an event that has no representation
// in the target wire format — correct at that layer, since there is nothing to
// send. The streaming writer then hands the nil to json.Marshal, which
// produces the four characters `null`, and writes `data: null` into the middle
// of the client's event stream. That frame is not a valid event for an
// Anthropic or an OpenAI client to parse: it has no `type` on one wire and no
// `choices` on the other, and a strict client aborts the reply on it.
//
// Every other test in this repo calls the translators directly and so never
// sees the nil reach the writer — which is exactly the seam this pins.
func TestStreamNeverWritesNullFrame(t *testing.T) {
	for _, format := range []string{"openai", "anthropic"} {
		t.Run(format, func(t *testing.T) {
			ch := make(chan *types.NormalizedStreamEvent, len(streamEvents()))
			for _, e := range streamEvents() {
				ch <- e
			}
			close(ch)

			h := NewHandler(nil, nil)
			w := httptest.NewRecorder()
			h.handleStream(context.Background(), w, "trace-1",
				&upstream.StreamResponse{EventChan: ch}, format)

			body := w.Body.String()

			if strings.Contains(body, "data: null") {
				t.Errorf("stream contains a `data: null` frame — the client cannot parse it:\n%s", body)
			}
			// Every frame must be either a JSON object or the terminator.
			for _, frame := range strings.Split(body, "\n\n") {
				frame = strings.TrimSpace(frame)
				if frame == "" {
					continue
				}
				if frame == "data: [DONE]" {
					continue
				}
				payload := strings.TrimPrefix(frame, "data: ")
				if !strings.HasPrefix(payload, "{") {
					t.Errorf("frame is not a JSON object: %q", frame)
				}
			}
			// The real payloads must still have made it through.
			if !strings.Contains(body, "reasoning-text") {
				t.Errorf("reasoning payload lost:\n%s", body)
			}
			if !strings.Contains(body, "text-payload") {
				t.Errorf("text payload lost:\n%s", body)
			}
			if !strings.Contains(body, "data: [DONE]") {
				t.Errorf("no terminator:\n%s", body)
			}
		})
	}
}
