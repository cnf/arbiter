package translator

import (
	"encoding/json"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

func TestAnthropicStreamEventTranslation(t *testing.T) {
	evt := &AnthropicStreamEvent{
		Type:  "content_block_delta",
		Index: 0,
		Delta: &AnthropicStreamDelta{
			Type: "text_delta",
			Text: "Hello, world!",
		},
	}

	// Translate to normalized
	normalized := AnthropicStreamEventToNormalized(evt)
	if normalized.Type != "content_block_delta" {
		t.Fatalf("expected type content_block_delta, got %s", normalized.Type)
	}
	if normalized.TextDelta != "Hello, world!" {
		t.Fatalf("expected text delta, got %q", normalized.TextDelta)
	}

	// Translate back to Anthropic
	roundtrip := NormalizedToAnthropicStreamEvent(normalized)
	if roundtrip.Type != evt.Type {
		t.Fatalf("roundtrip type mismatch: %s != %s", roundtrip.Type, evt.Type)
	}
	if roundtrip.Delta.Text != evt.Delta.Text {
		t.Fatalf("roundtrip text mismatch: %q != %q", roundtrip.Delta.Text, evt.Delta.Text)
	}

	// Should be JSON-serializable
	b, err := json.Marshal(roundtrip)
	if err != nil {
		t.Fatalf("marshal roundtrip: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("marshaled to empty bytes")
	}
}

func TestOpenAIStreamEventTranslation(t *testing.T) {
	evt := &OpenAIStreamEvent{
		ID:      "chatcmpl-test",
		Model:   "gpt-4",
		Object:  "text_completion.chunk",
		Created: 1234567890,
		Choices: []OpenAIStreamChoice{{
			Index: 0,
			Delta: OpenAIStreamDelta{
				Content: "Hello there!",
			},
		}},
	}

	// Translate to normalized
	normalized := OpenAIStreamEventToNormalized(evt)
	if normalized.Type != "content_block_delta" {
		t.Fatalf("expected type content_block_delta, got %s", normalized.Type)
	}
	if normalized.TextDelta != "Hello there!" {
		t.Fatalf("expected text delta, got %q", normalized.TextDelta)
	}

	// Translate to Anthropic format
	messageID := "test-msg-id"
	anthEvt := NormalizedToAnthropicStreamEvent(normalized)
	if anthEvt.Type != "content_block_delta" {
		t.Fatalf("expected translated type content_block_delta, got %s", anthEvt.Type)
	}
	if anthEvt.Delta.Text != "Hello there!" {
		t.Fatalf("expected translated text, got %q", anthEvt.Delta.Text)
	}

	// Translate to OpenAI format
	oaiEvt := NormalizedToOpenAIStreamEvent(normalized, messageID, 1234567890)
	b, err := json.Marshal(oaiEvt)
	if err != nil {
		t.Fatalf("marshal openai event: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("marshaled to empty bytes")
	}
}

func TestNormalizedToOpenAIStreamEventFidelity(t *testing.T) {
	created := int64(1700000000)

	// message_start carries model + role, with the shared ID/created/model
	start := &types.NormalizedStreamEvent{
		Type:         "message_start",
		MessageID:    "msg_123",
		MessageModel: "gpt-4o",
	}
	oaiStart := NormalizedToOpenAIStreamEvent(start, "trace-1", created)
	if oaiStart.ID != "chatcmpl-trace-1" {
		t.Errorf("ID = %q, want chatcmpl-trace-1", oaiStart.ID)
	}
	if oaiStart.Created != created {
		t.Errorf("Created = %d, want %d", oaiStart.Created, created)
	}
	if oaiStart.Model != "gpt-4o" {
		t.Errorf("Model = %q, want gpt-4o", oaiStart.Model)
	}
	if oaiStart.Object != "chat.completion.chunk" {
		t.Errorf("Object = %q, want chat.completion.chunk", oaiStart.Object)
	}
	if oaiStart.Choices[0].Delta.Role != "assistant" {
		t.Errorf("Delta.Role = %q, want assistant", oaiStart.Choices[0].Delta.Role)
	}

	// a later content chunk also carries model/created (OpenAI sends model
	// and created on every chunk of one completion)
	delta := &types.NormalizedStreamEvent{
		Type:      "content_block_delta",
		DeltaType: "text_delta",
		TextDelta: "hello",
	}
	oaiDelta := NormalizedToOpenAIStreamEvent(delta, "trace-1", created)
	if oaiStart.ID != "chatcmpl-trace-1" {
		t.Errorf("ID = %q, want chatcmpl-trace-1", oaiStart.ID)
	}
	if oaiDelta.Created != created {
		t.Errorf("Created = %d, want %d", oaiDelta.Created, created)
	}

	// message_stop maps the normalized stop reason back to OpenAI's
	// finish_reason vocabulary ("end_turn" -> "stop")
	stop := &types.NormalizedStreamEvent{
		Type:              "message_stop",
		MessageStopReason: "end_turn",
	}
	oaiStop := NormalizedToOpenAIStreamEvent(stop, "trace-1", created)
	if oaiStop.Choices[0].FinishReason == nil || *oaiStop.Choices[0].FinishReason != "stop" {
		got := "<nil>"
		if oaiStop.Choices[0].FinishReason != nil {
			got = *oaiStop.Choices[0].FinishReason
		}
		t.Errorf("FinishReason = %q, want stop", got)
	}
}

func TestStreamMessageStartAndStop(t *testing.T) {
	// Anthropic message_start event
	startEvt := &AnthropicStreamEvent{
		Type: "message_start",
		Message: &AnthropicStreamMessage{
			ID:    "msg_123",
			Model: "claude-3-haiku",
			Usage: types.AnthropicUsage{InputTokens: 100},
		},
	}

	normalized := AnthropicStreamEventToNormalized(startEvt)
	if normalized.Type != "message_start" {
		t.Fatalf("expected message_start, got %s", normalized.Type)
	}
	if normalized.MessageID != "msg_123" {
		t.Fatalf("expected message ID msg_123, got %s", normalized.MessageID)
	}
	if normalized.InputTokens != 100 {
		t.Fatalf("expected 100 input tokens, got %d", normalized.InputTokens)
	}

	// Anthropic message_stop event
	stopEvt := &AnthropicStreamEvent{
		Type: "message_stop",
		Message: &AnthropicStreamMessage{
			ID:         "msg_123",
			StopReason: "end_turn",
		},
	}

	normalized = AnthropicStreamEventToNormalized(stopEvt)
	if normalized.Type != "message_stop" {
		t.Fatalf("expected message_stop, got %s", normalized.Type)
	}
	if normalized.MessageStopReason != "end_turn" {
		t.Fatalf("expected stop_reason end_turn, got %s", normalized.MessageStopReason)
	}
}
