package translator

import (
	"encoding/json"

	"github.com/cnf/arbiter/pkg/types"
)

// --- Anthropic SSE event types ---

// AnthropicStreamEvent is an event in an Anthropic SSE stream.
type AnthropicStreamEvent struct {
	Type         string                  `json:"type"`
	Message      *AnthropicStreamMessage `json:"message,omitempty"`
	ContentBlock *types.AnthropicContent `json:"content_block,omitempty"`
	Delta        *AnthropicStreamDelta   `json:"delta,omitempty"`
	Index        int                     `json:"index,omitempty"`
	Usage        *types.AnthropicUsage   `json:"usage,omitempty"`
}

type AnthropicStreamMessage struct {
	ID         string                   `json:"id"`
	Type       string                   `json:"type"`
	Role       string                   `json:"role"`
	Content    []types.AnthropicContent `json:"content"`
	Model      string                   `json:"model"`
	StopReason string                   `json:"stop_reason"`
	Usage      types.AnthropicUsage     `json:"usage"`
}

type AnthropicStreamDelta struct {
	Type  string `json:"type"` // "text_delta", "tool_use_delta"
	Text  string `json:"text,omitempty"`
	Input string `json:"input,omitempty"` // for tool_use_delta
}

// --- OpenAI SSE event types ---

// OpenAIStreamEvent is an event in an OpenAI SSE stream.
type OpenAIStreamEvent struct {
	ID      string               `json:"id"`
	Object  string               `json:"object"`
	Created int64                `json:"created"`
	Model   string               `json:"model"`
	Choices []OpenAIStreamChoice `json:"choices,omitempty"`
	Usage   *types.OpenAIUsage   `json:"usage,omitempty"`
}

type OpenAIStreamChoice struct {
	Index        int               `json:"index"`
	Delta        OpenAIStreamDelta `json:"delta"`
	FinishReason *string           `json:"finish_reason"`
}

type OpenAIStreamDelta struct {
	Role      string                 `json:"role,omitempty"`
	Content   string                 `json:"content,omitempty"`
	ToolCalls []types.OpenAIToolCall `json:"tool_calls,omitempty"`
}

// --- Translation from Anthropic SSE events to Normalized ---

// AnthropicStreamEventToNormalized translates a single Anthropic SSE event
// into a normalized stream event for relay to the client.
func AnthropicStreamEventToNormalized(evt *AnthropicStreamEvent) *types.NormalizedStreamEvent {
	if evt == nil {
		return nil
	}

	normalized := &types.NormalizedStreamEvent{Type: evt.Type}

	switch evt.Type {
	case "message_start":
		if evt.Message != nil {
			normalized.MessageID = evt.Message.ID
			normalized.MessageModel = evt.Message.Model
			normalized.InputTokens = evt.Message.Usage.InputTokens
		}

	case "content_block_start":
		if evt.ContentBlock != nil {
			normalized.BlockType = evt.ContentBlock.Type
			normalized.BlockIndex = evt.Index
		}

	case "content_block_delta":
		if evt.Delta != nil {
			normalized.BlockIndex = evt.Index
			normalized.DeltaType = evt.Delta.Type
			normalized.TextDelta = evt.Delta.Text
		}

	case "message_delta":
		if evt.Delta != nil {
			normalized.DeltaType = evt.Delta.Type
		}
		if evt.Usage != nil {
			normalized.OutputTokens = evt.Usage.OutputTokens
		}

	case "message_stop":
		if evt.Message != nil {
			normalized.MessageID = evt.Message.ID
			normalized.MessageStopReason = evt.Message.StopReason
		}
	}

	return normalized
}

// --- Translation from OpenAI SSE events to Normalized ---

// OpenAIStreamEventToNormalized translates a single OpenAI SSE event into
// a normalized stream event for relay to the client.
func OpenAIStreamEventToNormalized(evt *OpenAIStreamEvent) *types.NormalizedStreamEvent {
	if evt == nil {
		return nil
	}

	// OpenAI's format is less structured than Anthropic's. We synthesize
	// events that match Anthropic's shape for uniformity.
	normalized := &types.NormalizedStreamEvent{
		Type: "content_block_delta", // most OpenAI chunks are content deltas
	}

	if len(evt.Choices) > 0 {
		choice := evt.Choices[0]
		normalized.BlockIndex = choice.Index

		if choice.Delta.Role != "" && choice.Delta.Role == "assistant" {
			// First chunk with role="assistant" signals message_start
			normalized.Type = "message_start"
			normalized.MessageModel = evt.Model
		}

		if choice.Delta.Content != "" {
			normalized.Type = "content_block_delta"
			normalized.DeltaType = "text_delta"
			normalized.TextDelta = choice.Delta.Content
		}

		if len(choice.Delta.ToolCalls) > 0 {
			normalized.Type = "content_block_delta"
			normalized.DeltaType = "tool_use_delta"
		}

		if choice.FinishReason != nil {
			normalized.Type = "message_stop"
			normalized.MessageStopReason = openAIFinishReasonToNormalized(*choice.FinishReason)
		}
	}

	if evt.Usage != nil {
		normalized.InputTokens = evt.Usage.PromptTokens
		normalized.OutputTokens = evt.Usage.CompletionTokens
	}

	return normalized
}

// --- Translation from Normalized events to wire formats ---

// NormalizedToAnthropicStreamEvent translates a normalized stream event into
// Anthropic wire format for sending to the client.
func NormalizedToAnthropicStreamEvent(evt *types.NormalizedStreamEvent) *AnthropicStreamEvent {
	if evt == nil {
		return nil
	}

	anthropic := &AnthropicStreamEvent{Type: evt.Type}

	switch evt.Type {
	case "message_start":
		anthropic.Message = &AnthropicStreamMessage{
			ID:    evt.MessageID,
			Type:  "message",
			Role:  "assistant",
			Model: evt.MessageModel,
			Usage: types.AnthropicUsage{InputTokens: evt.InputTokens},
		}

	case "content_block_start":
		anthropic.Index = evt.BlockIndex
		anthropic.ContentBlock = &types.AnthropicContent{Type: evt.BlockType}

	case "content_block_delta":
		anthropic.Index = evt.BlockIndex
		anthropic.Delta = &AnthropicStreamDelta{Type: evt.DeltaType}
		if evt.DeltaType == "text_delta" {
			anthropic.Delta.Text = evt.TextDelta
		}

	case "message_delta":
		anthropic.Delta = &AnthropicStreamDelta{Type: evt.DeltaType}
		anthropic.Usage = &types.AnthropicUsage{OutputTokens: evt.OutputTokens}

	case "message_stop":
		anthropic.Message = &AnthropicStreamMessage{
			ID:         evt.MessageID,
			StopReason: evt.MessageStopReason,
		}
	}

	return anthropic
}

// NormalizedToOpenAIStreamEvent translates a normalized stream event into
// OpenAI wire format for sending to the client. messageID and created come
// from the caller so every chunk of one completion shares the same ID and
// timestamp, as OpenAI clients expect. Model is taken from the event: the
// pipeline stamps MessageModel on every event (upstream-reported model when
// available, the routed model otherwise).
func NormalizedToOpenAIStreamEvent(evt *types.NormalizedStreamEvent, messageID string, created int64) *OpenAIStreamEvent {
	if evt == nil {
		return nil
	}

	openai := &OpenAIStreamEvent{
		ID:      "chatcmpl-" + messageID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   evt.MessageModel,
		Choices: []OpenAIStreamChoice{{Index: evt.BlockIndex}},
	}

	switch evt.Type {
	case "message_start":
		openai.Choices[0].Delta.Role = "assistant"

	case "content_block_delta":
		if evt.DeltaType == "text_delta" {
			openai.Choices[0].Delta.Content = evt.TextDelta
		}

	case "message_stop":
		finish := normalizedToOpenAIFinishReason(evt.MessageStopReason)
		openai.Choices[0].FinishReason = &finish
		if evt.InputTokens > 0 || evt.OutputTokens > 0 {
			openai.Usage = &types.OpenAIUsage{
				PromptTokens:     evt.InputTokens,
				CompletionTokens: evt.OutputTokens,
				TotalTokens:      evt.InputTokens + evt.OutputTokens,
			}
		}
	}

	return openai
}

// ParseSSEEvent parses a single line from an SSE stream (data: {...}) into JSON.
// SSE format is "data: <json>\n\n", so the caller strips "data: " before calling.
func ParseSSEEvent(data string, into interface{}) error {
	return json.Unmarshal([]byte(data), into)
}
