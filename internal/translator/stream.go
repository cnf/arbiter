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
	Type  string `json:"type"` // "text_delta", "thinking_delta", "input_json_delta", "message_delta"
	Text  string `json:"text,omitempty"`
	Input string `json:"input,omitempty"` // for tool_use_delta

	// Thinking carries a thinking_delta's text, and PartialJSON a
	// input_json_delta's slice of a tool call's arguments. Both are relaying
	// fields for content that originates in another wire format.
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
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
	ToolCalls []OpenAIStreamToolCall `json:"tool_calls,omitempty"`

	// Vendor reasoning. OpenRouter-family upstreams emit reasoning text under
	// several names; all are captured so the field can be relayed rather than
	// silently dropped. ReasoningDetails is kept as raw JSON because its
	// element shape varies by provider and is only ever passed through.
	Reasoning        string            `json:"reasoning,omitempty"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	ReasoningDetails []json.RawMessage `json:"reasoning_details,omitempty"`
}

// OpenAIStreamToolCall is a tool-call fragment in a streaming delta. Distinct
// from types.OpenAIToolCall (the non-streaming shape): here Function.Name is
// present only on the first fragment, and Arguments arrives as a slice per
// chunk that the client concatenates. Index identifies which call within the
// message a fragment belongs to.
type OpenAIStreamToolCall struct {
	Index    int                      `json:"index"`
	ID       string                   `json:"id,omitempty"`
	Type     string                   `json:"type,omitempty"`
	Function OpenAIStreamToolCallFunc `json:"function"`
}

type OpenAIStreamToolCallFunc struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
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
//
// Ordering rule: the start event is derived from an explicit role marker, and
// the stop event only from a finish_reason. OpenAI's terminal chunk (the one
// carrying usage, sent after the finish chunk when include_usage is set) also
// carries role="assistant" — deriving a start from it would emit a second
// message_start *after* message_stop, which is invalid on the wire and is what
// a strict client breaks on. Once finished is set, no further start is derived
// from this stream's remaining chunks.
func OpenAIStreamEventToNormalized(evt *OpenAIStreamEvent, finished bool) *types.NormalizedStreamEvent {
	if evt == nil {
		return nil
	}

	// A usage-only chunk (no choices) is how OpenAI and OpenRouter report
	// token counts at the end of a stream. It is not a delta of anything, but
	// it is the *only* carrier of usage, so it becomes a distinct event type
	// rather than being dropped.
	if len(evt.Choices) == 0 {
		if evt.Usage == nil {
			return nil
		}
		normalized := &types.NormalizedStreamEvent{Type: "usage"}
		applyOpenAIUsage(normalized, evt.Usage)
		return normalized
	}

	normalized := &types.NormalizedStreamEvent{
		Type: "content_block_delta", // most OpenAI chunks are content deltas
	}

	choice := evt.Choices[0]
	normalized.BlockIndex = choice.Index

	if choice.Delta.Role == "assistant" && evt.Usage == nil && !finished {
		// First chunk with role="assistant" signals message_start — unless it
		// is really the trailing usage/role chunk, or the stop already went out.
		normalized.Type = "message_start"
		normalized.MessageModel = evt.Model
	}

	// carriesDelta records whether this chunk put any content on the wire. The
	// terminal usage chunk arrives as a choice with an empty delta plus a usage
	// object, which would otherwise leave an empty content_block_delta that
	// silently swallows the token counts.
	carriesDelta := false

	if choice.Delta.Content != "" {
		normalized.Type = "content_block_delta"
		normalized.DeltaType = "text_delta"
		normalized.TextDelta = choice.Delta.Content
		carriesDelta = true
	}

	if reasoning := openAIReasoningText(choice.Delta); reasoning != "" {
		normalized.Type = "content_block_delta"
		normalized.DeltaType = "reasoning_delta"
		normalized.Reasoning = reasoning
		carriesDelta = true
	}

	if len(choice.Delta.ToolCalls) > 0 {
		tc := choice.Delta.ToolCalls[0]
		normalized.Type = "content_block_delta"
		normalized.DeltaType = "tool_use_delta"
		normalized.ToolCallIndex = tc.Index
		normalized.ToolCallID = tc.ID
		normalized.ToolCallName = tc.Function.Name
		normalized.ToolCallArgs = tc.Function.Arguments
		carriesDelta = true
	}

	if choice.FinishReason != nil {
		normalized.Type = "message_stop"
		normalized.MessageStopReason = openAIFinishReasonToNormalized(*choice.FinishReason)
		carriesDelta = true
	}

	if evt.Usage != nil {
		applyOpenAIUsage(normalized, evt.Usage)
		// A chunk whose only payload is usage is the accounting event, not a
		// delta. Typing it as one is what lets the outbound side emit a
		// choice-less chunk with the counts attached, matching what upstream
		// sent and what an OpenAI client parses.
		if !carriesDelta {
			normalized.Type = "usage"
		}
	}

	return normalized
}

// openAIReasoningText returns whichever reasoning field the upstream used.
// OpenRouter sends the same text under `reasoning` and `reasoning_content`,
// so the first non-empty one is taken (they are duplicates, not distinct
// content — relaying both would double the client's thinking trace).
func openAIReasoningText(d OpenAIStreamDelta) string {
	if d.ReasoningContent != "" {
		return d.ReasoningContent
	}
	return d.Reasoning
}

// applyOpenAIUsage copies the token/cost accounting off a usage object onto a
// normalized event, including the prompt-cache counters — which the streaming
// path previously never read, leaving every streamed row with zero tokens and
// no way to tell whether cache affinity was working.
func applyOpenAIUsage(normalized *types.NormalizedStreamEvent, usage *types.OpenAIUsage) {
	normalized.InputTokens = usage.PromptTokens
	normalized.OutputTokens = usage.CompletionTokens
	normalized.CostUSD = usage.Cost

	if cached, ok := usage.PromptDetails["cached_tokens"]; ok {
		if f, ok := cached.(float64); ok {
			normalized.CacheReadTokens = int(f)
		}
	}
	// OpenRouter reports cache writes under either name depending on the
	// upstream model, and reports them on the same prompt-details object.
	for _, key := range []string{"cache_write_tokens", "cache_creation_tokens"} {
		if v, ok := usage.PromptDetails[key]; ok {
			if f, ok := v.(float64); ok && int(f) > 0 {
				normalized.CacheWriteTokens = int(f)
				break
			}
		}
	}
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
		switch evt.DeltaType {
		case "reasoning_delta":
			// Anthropic's own name for vendor reasoning is a thinking delta.
			anthropic.Delta = &AnthropicStreamDelta{Type: "thinking_delta", Thinking: evt.Reasoning}
		case "tool_use_delta":
			// Partial JSON for a tool call's input, which is what Anthropic's
			// input_json_delta carries. The upstream's arguments fragment maps
			// across unchanged.
			anthropic.Index = evt.ToolCallIndex
			anthropic.Delta = &AnthropicStreamDelta{Type: "input_json_delta", PartialJSON: evt.ToolCallArgs}
		default:
			anthropic.Delta = &AnthropicStreamDelta{Type: evt.DeltaType}
			if evt.DeltaType == "text_delta" {
				anthropic.Delta.Text = evt.TextDelta
			}
		}

	case "message_delta":
		anthropic.Delta = &AnthropicStreamDelta{Type: evt.DeltaType}
		anthropic.Usage = &types.AnthropicUsage{OutputTokens: evt.OutputTokens}

	case "message_stop":
		anthropic.Message = &AnthropicStreamMessage{
			ID:         evt.MessageID,
			StopReason: evt.MessageStopReason,
		}

	case "usage":
		// Anthropic has no usage-only event; its usage rides on message_delta.
		// Emitting the normalized type verbatim would put an event name on the
		// wire that an Anthropic client does not know.
		anthropic.Type = "message_delta"
		anthropic.Delta = &AnthropicStreamDelta{Type: "message_delta"}
		anthropic.Usage = &types.AnthropicUsage{
			InputTokens:              evt.InputTokens,
			OutputTokens:             evt.OutputTokens,
			CacheReadInputTokens:     evt.CacheReadTokens,
			CacheCreationInputTokens: evt.CacheWriteTokens,
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
		switch evt.DeltaType {
		case "text_delta":
			openai.Choices[0].Delta.Content = evt.TextDelta
		case "reasoning_delta":
			// Relayed under the field OpenRouter-family clients expect, so an
			// agent CLI's thinking trace survives the proxy.
			openai.Choices[0].Delta.ReasoningContent = evt.Reasoning
		case "tool_use_delta":
			// One fragment per chunk, exactly as the upstream sent it: id and
			// name on the first, a slice of the arguments JSON on each. The
			// client concatenates arguments by index — reassembling here would
			// delay every call until the whole stream had been buffered.
			openai.Choices[0].Delta.ToolCalls = []OpenAIStreamToolCall{{
				Index: evt.ToolCallIndex,
				ID:    evt.ToolCallID,
				Type:  "function",
				Function: OpenAIStreamToolCallFunc{
					Name:      evt.ToolCallName,
					Arguments: evt.ToolCallArgs,
				},
			}}
		}

	case "message_stop":
		finish := normalizedToOpenAIFinishReason(evt.MessageStopReason)
		openai.Choices[0].FinishReason = &finish

	case "message_delta":
		if evt.InputTokens > 0 || evt.OutputTokens > 0 {
			openai.Usage = &types.OpenAIUsage{
				PromptTokens:     evt.InputTokens,
				CompletionTokens: evt.OutputTokens,
				TotalTokens:      evt.InputTokens + evt.OutputTokens,
			}
		}

	case "usage":
		// The upstream's own terminal usage chunk, relayed with its counts and
		// cost. Emitted with no choices, matching the shape OpenAI clients
		// parse for the final accounting.
		openai.Choices = nil
		openai.Usage = &types.OpenAIUsage{
			PromptTokens:     evt.InputTokens,
			CompletionTokens: evt.OutputTokens,
			TotalTokens:      evt.InputTokens + evt.OutputTokens,
			Cost:             evt.CostUSD,
		}
	}

	return openai
}

// ParseSSEEvent parses a single line from an SSE stream (data: {...}) into JSON.
// SSE format is "data: <json>\n\n", so the caller strips "data: " before calling.
func ParseSSEEvent(data string, into interface{}) error {
	return json.Unmarshal([]byte(data), into)
}
