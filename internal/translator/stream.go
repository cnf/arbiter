package translator

import (
	"encoding/json"

	"github.com/cnf/arbiter/pkg/types"
)

// --- Anthropic SSE event types ---

// AnthropicStreamMessage is the message object carried on a message_start
// event, and echoed (mostly empty) on message_stop.
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
	Type  string `json:"type"` // "text_delta", "thinking_delta", "signature_delta", "input_json_delta", "message_delta"
	Text  string `json:"text,omitempty"`
	Input string `json:"input,omitempty"` // for tool_use_delta

	// Thinking carries a thinking_delta's text, and PartialJSON a
	// input_json_delta's slice of a tool call's arguments. Both are relaying
	// fields for content that originates in another wire format.
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	// Signature is Anthropic's opaque per-thinking-block token. It is NOT
	// content and has no OpenAI equivalent, but an Anthropic client must
	// replay it with the block on its next request, so it cannot be dropped
	// on the anthropic-to-anthropic path. It is carried through the
	// normalized type and re-emitted there; the OpenAI translator ignores it,
	// because a client that cannot replay a thinking block has no use for it.
	Signature string `json:"signature,omitempty"`
	// StopReason rides on a message_delta's delta, NOT on its message. It is
	// how an Anthropic stream tells the client why the turn ended, and an
	// event that only forwards the delta's type drops it — leaving a client
	// with the text but never a terminal reason.
	StopReason string `json:"stop_reason,omitempty"`
}

// AnthropicStreamEvent is an event in an Anthropic SSE stream.
//
// Index is a pointer so that block 0 is transmitted as `"index":0` while the
// message-level events (message_start, message_delta, message_stop) carry no
// index field at all. Anthropic requires `index` on every content_block_*
// event, and a plain int with omitempty silently drops it for block 0 — the
// first block of every reply, which is where a thinking block or the opening
// text lives.
type AnthropicStreamEvent struct {
	Type         string                  `json:"type"`
	Message      *AnthropicStreamMessage `json:"message,omitempty"`
	ContentBlock *types.AnthropicContent `json:"content_block,omitempty"`
	Delta        *AnthropicStreamDelta   `json:"delta,omitempty"`
	Index        *int                    `json:"index,omitempty"`
	Usage        *types.AnthropicUsage   `json:"usage,omitempty"`
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

// derefIndex reads an Anthropic event's optional block index. A nil index
// (an event that carries none) reads as 0, which is the block a client that
// saw no index would have assumed anyway; the distinction only matters when
// re-emitting, where a nil must stay nil.
func derefIndex(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// intPtr returns a pointer to v, for the optional index fields on the
// Anthropic wire types. Block 0 must be transmitted as `0`, not omitted.
func intPtr(v int) *int { return &v }

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
			// Anthropic reports cache read/write only here, never on
			// message_delta (whose usage carries just cumulative
			// output_tokens) — this was the only event that could have
			// populated NormalizedStreamEvent's cache fields, and nothing
			// did, so every streamed Claude row recorded zero cache usage
			// even when the upstream cached the prompt.
			normalized.CacheReadTokens = evt.Message.Usage.CacheReadInputTokens
			normalized.CacheWriteTokens = evt.Message.Usage.CacheCreationInputTokens
		}

	case "content_block_start":
		if evt.ContentBlock != nil {
			normalized.BlockType = evt.ContentBlock.Type
			blockIndex := derefIndex(evt.Index)
			normalized.BlockIndex = blockIndex
			// A tool call's identity arrives HERE and nowhere else: Anthropic
			// opens the block with id and name, then streams the arguments as
			// input_json_delta events that carry only the index. An OpenAI
			// client needs id and name on its first tool-call fragment, and
			// this event is that fragment — the block start is where the
			// identity is announced on both wires, so it is carried here
			// rather than re-derived downstream.
			if evt.ContentBlock.Type == "tool_use" {
				normalized.ToolCallID = evt.ContentBlock.ID
				normalized.ToolCallName = evt.ContentBlock.Name
				normalized.ToolCallIndex = blockIndex
			}
			// A text block's opening text (Anthropic sends it empty in
			// practice) is content like any other; dropping it would lose the
			// first characters of a reply that used the field.
			normalized.TextDelta = evt.ContentBlock.Text
		}

	case "content_block_delta":
		if evt.Delta != nil {
			blockIndex := derefIndex(evt.Index)
			normalized.BlockIndex = blockIndex
			normalized.TextDelta = evt.Delta.Text

			// Which block index this delta belongs to is only known from the
			// content_block_start that opened it — a tool_use block is opened
			// at its own index and then streamed as input_json_delta, whose
			// event carries that same index. The index is authoritative as
			// sent; evt.Index is it.
			switch evt.Delta.Type {
			case "thinking_delta":
				normalized.DeltaType = "reasoning_delta"
				normalized.Reasoning = evt.Delta.Thinking
			case "input_json_delta":
				normalized.DeltaType = "tool_use_delta"
				normalized.ToolCallIndex = blockIndex
				normalized.ToolCallArgs = evt.Delta.PartialJSON
			case "signature_delta":
				// Anthropic's per-thinking-block signature. It is not
				// content and has no OpenAI equivalent, but an Anthropic
				// client must echo it back with the block on its next
				// request, so it is carried on the normalized type and
				// re-emitted by the Anthropic translator. The OpenAI
				// translator ignores it — a client that cannot replay a
				// thinking block has nothing to do with it. Deliberately
				// typed, not left to fall through as an empty delta.
				normalized.DeltaType = "signature_delta"
				normalized.Signature = evt.Delta.Signature
			default:
				normalized.DeltaType = evt.Delta.Type
			}
		}

	case "content_block_stop":
		// Anthropic's block terminator, carrying only its index. Without a
		// case here the index fell to BlockIndex's zero value, so EVERY
		// block closed as `"index":0` — a second block's stop was reported as
		// closing the first, leaving a client's block bookkeeping wrong at
		// the point it finalizes the message.
		normalized.BlockIndex = derefIndex(evt.Index)

	case "message_delta":
		// stop_reason rides on the DELTA, not on the message. It is how the
		// stream says why the turn ended; reading only the delta's type here
		// dropped it, and the client was left with the text but no terminal
		// reason — its turn never concluded.
		if evt.Delta != nil {
			normalized.DeltaType = evt.Delta.Type
			normalized.MessageStopReason = evt.Delta.StopReason
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
			// Anthropic sends `"content":[]`; leaving it nil marshals to
			// `"content":null`, and a client that appends its blocks onto this
			// array hits `undefined` on the first push.
			Content: []types.AnthropicContent{},
			Usage: types.AnthropicUsage{
				InputTokens:              evt.InputTokens,
				CacheReadInputTokens:     evt.CacheReadTokens,
				CacheCreationInputTokens: evt.CacheWriteTokens,
			},
		}

	case "content_block_start":
		anthropic.Index = intPtr(evt.BlockIndex)
		anthropic.ContentBlock = &types.AnthropicContent{Type: evt.BlockType}
		// A tool_use block is announced with its id and name here and nowhere
		// else — the input_json_delta events that follow carry only argument
		// fragments. Omitting them leaves an Anthropic client with a tool call
		// it cannot name or answer.
		if evt.BlockType == "tool_use" {
			anthropic.Index = intPtr(evt.ToolCallIndex)
			anthropic.ContentBlock.ID = evt.ToolCallID
			anthropic.ContentBlock.Name = evt.ToolCallName
		}
		if evt.TextDelta != "" {
			anthropic.ContentBlock.Text = evt.TextDelta
		}

	case "content_block_stop":
		// Anthropic closes each block with its index. This event carries no
		// delta and no content_block, so dropping it under the assumption
		// that "there is nothing to send" leaves the client without its block
		// terminator — the index alone is the payload.
		anthropic.Index = intPtr(evt.BlockIndex)

	case "content_block_delta":
		anthropic.Index = intPtr(evt.BlockIndex)
		switch evt.DeltaType {
		case "signature_delta":
			// Anthropic's own event, re-emitted verbatim. It has to survive
			// this path: the client echoes it back on the next request, so a
			// dropped signature leaves it unable to replay the thinking block.
			anthropic.Delta = &AnthropicStreamDelta{Type: "signature_delta", Signature: evt.Signature}
		case "reasoning_delta":
			// Anthropic's own name for vendor reasoning is a thinking delta.
			anthropic.Delta = &AnthropicStreamDelta{Type: "thinking_delta", Thinking: evt.Reasoning}
		case "tool_use_delta":
			// Partial JSON for a tool call's input, which is what Anthropic's
			// input_json_delta carries. The upstream's arguments fragment maps
			// across unchanged.
			anthropic.Index = intPtr(evt.ToolCallIndex)
			anthropic.Delta = &AnthropicStreamDelta{Type: "input_json_delta", PartialJSON: evt.ToolCallArgs}
		default:
			anthropic.Delta = &AnthropicStreamDelta{Type: evt.DeltaType}
			if evt.DeltaType == "text_delta" {
				anthropic.Delta.Text = evt.TextDelta
			}
		}

	case "message_delta":
		// The stop_reason must travel on the delta, which is where an
		// Anthropic client reads it. A message_delta carrying only a type is
		// a message_delta that never terminates the turn.
		anthropic.Delta = &AnthropicStreamDelta{Type: "message_delta", StopReason: evt.MessageStopReason}
		anthropic.Usage = &types.AnthropicUsage{OutputTokens: evt.OutputTokens}

	case "message_stop":
		// Anthropic's message_stop carries no message object at all — the
		// stop_reason was already delivered on the preceding message_delta.
		// Emitting the normalized message here wrote an empty message with a
		// literal null content, which is not a shape the upstream ever sends.

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

	case "content_block_start":
		// OpenAI has no block-start event, but this is where a tool call's
		// identity is announced, so it doubles as the first tool-call
		// fragment: id, type and name, with arguments to follow on the
		// input_json_delta events. Dropping this event left the client with
		// argument fragments for a tool it could not name — and, since an
		// OpenAI client keys its calls by index, no call to attach them to.
		if evt.BlockType == "tool_use" {
			openai.Choices[0].Index = evt.ToolCallIndex
			openai.Choices[0].Delta.ToolCalls = []OpenAIStreamToolCall{{
				Index:    evt.ToolCallIndex,
				ID:       evt.ToolCallID,
				Type:     "function",
				Function: OpenAIStreamToolCallFunc{Name: evt.ToolCallName},
			}}
		} else if evt.TextDelta != "" {
			// A text block whose opening chunk already carries text.
			openai.Choices[0].Delta.Content = evt.TextDelta
		} else {
			// A block opening with nothing on it (the usual case for text:
			// Anthropic sends the text in the deltas). OpenAI clients have no
			// block concept, so an empty chunk here is pure noise on the wire
			// and is dropped rather than emitted.
			return nil
		}

	case "content_block_stop":
		// Anthropic's block terminator. OpenAI has no equivalent — the
		// content_block_delta carves the same information into the client's
		// own shape — so it is dropped deliberately. Falling through emitted
		// a well-formed chunk carrying nothing on every block of every
		// streamed reply.
		return nil

	case "content_block_delta":
		switch evt.DeltaType {
		case "text_delta":
			openai.Choices[0].Delta.Content = evt.TextDelta
		case "reasoning_delta":
			// Relayed under the field OpenRouter-family clients expect, so an
			// agent CLI's thinking trace survives the proxy. A Claude
			// thinking_delta reaches here as a reasoning_delta (the inbound
			// parser renames it), so this arm is what stops a thinking reply
			// from arriving as a stream of empty chunks.
			openai.Choices[0].Delta.ReasoningContent = evt.Reasoning
		case "thinking_delta":
			// The Anthropic name, for a reasoning delta that reached the
			// normalized type without being renamed. Without this arm the
			// delta falls through and the client gets a chunk carrying
			// nothing — the exact shape that reads as an empty reply.
			openai.Choices[0].Delta.ReasoningContent = evt.Reasoning
		case "tool_use_delta":
			// One fragment per chunk, exactly as the upstream sent it: id and
			// name on the first, a slice of the arguments JSON on each. The
			// client concatenates arguments by index — reassembling here would
			// delay every call until the whole stream had been buffered.
			//
			// The tool-call index is Anthropic's content-block index (the
			// inbound parser carries it through). It is distinct and stable
			// per call, which is all the client's concatenation contract
			// needs; the choice index is left where it was.
			openai.Choices[0].Delta.ToolCalls = []OpenAIStreamToolCall{{
				Index: evt.ToolCallIndex,
				ID:    evt.ToolCallID,
				Type:  "function",
				Function: OpenAIStreamToolCallFunc{
					Name:      evt.ToolCallName,
					Arguments: evt.ToolCallArgs,
				},
			}}
		case "input_json_delta":
			// The Anthropic name for the same fragment, read directly off a
			// stream that was not renamed inbound.
			openai.Choices[0].Delta.ToolCalls = []OpenAIStreamToolCall{{
				Index: evt.ToolCallIndex,
				Type:  "function",
				Function: OpenAIStreamToolCallFunc{
					Name:      evt.ToolCallName,
					Arguments: evt.ToolCallArgs,
				},
			}}
		case "signature_delta":
			// Anthropic's thinking-block signature has no OpenAI equivalent
			// and is not content: an OpenAI client cannot replay a thinking
			// block, so there is nothing to send. Dropped HERE, deliberately,
			// as an explicit no-op — falling through the switch would emit a
			// well-formed chunk carrying nothing, which is the defect this
			// whole change is about.
			return nil
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
		// cost. Its shape matters: OpenAI and OpenRouter both send this as a
		// chunk whose choice carries an EMPTY delta alongside the usage block
		// ("choices":[{"index":0,"delta":{}}],"usage":{...}), and strict clients
		// validate against a union that requires `choices` to be present. A
		// choice-less chunk fails that validation and aborts the whole stream,
		// so the empty-delta form is reproduced here rather than omitted.
		openai.Choices = []OpenAIStreamChoice{{Index: evt.BlockIndex}}
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
