package types

import (
	"bytes"
	"encoding/json"
	"strings"
)

// NormalizedRequest is the canonical internal representation of a request,
// independent of Anthropic or OpenAI wire format.
type NormalizedRequest struct {
	Messages     []Message
	Model        string
	MaxTokens    int
	Temperature  float64
	SystemPrompt string
	Tools        []Tool
	Stream       bool // if true, caller expects SSE response

	// Tracking
	OriginalFormat  string // "anthropic" or "openai"
	OriginalPayload []byte
	TraceID         string
	SessionKey      string // set by the pipeline after guardrails; "" if no usable session key
}

// Message represents a single conversation turn.
type Message struct {
	Role    string // "user", "assistant", "tool"
	Content []ContentBlock
}

// ContentBlock is a single piece of content in a message. Flattened (rather
// than an interface{} union) so translators don't need type assertions.
type ContentBlock struct {
	Type string // "text", "tool_use", "tool_result", "thinking"

	// type == "text" or "thinking"
	Text string

	// type == "tool_use"
	ToolUseID string
	ToolName  string
	ToolInput map[string]interface{}

	// type == "tool_result"
	ToolResultForID string // references a prior tool_use ID
	ToolResult      string
	ToolIsError     bool
}

// Tool is a function/tool definition the model can call.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]interface{}
}

// TextBlock is a convenience constructor for a plain text content block.
func TextBlock(text string) ContentBlock {
	return ContentBlock{Type: "text", Text: text}
}

// ExtractText concatenates all text (and thinking) blocks in a message.
// Tool use/result blocks are ignored — this is meant for classifiers that
// need "what did the user actually say" in plain text.
func ExtractText(msg Message) string {
	var out string
	for _, block := range msg.Content {
		if block.Type == "text" {
			if out != "" {
				out += " "
			}
			out += block.Text
		}
	}
	return out
}

// LastUserText returns the plain text of the last user message, or "" if
// there isn't one. Used by classifiers as the primary signal source.
func LastUserText(req *NormalizedRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return ExtractText(req.Messages[i])
		}
	}
	return ""
}

// FirstUserText returns the plain text of the first user message that
// actually has text content, skipping user turns whose only content is a
// tool_result block (agentic clients send those; ExtractText yields "" for
// them, and a turn with no text carries no useful entropy for the caller).
func FirstUserText(req *NormalizedRequest) string {
	for _, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		if text := ExtractText(m); text != "" {
			return text
		}
	}
	return ""
}

// AnthropicRequest represents a raw Anthropic /v1/messages request.
type AnthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature,omitempty"`
	System      AnthropicSystem    `json:"system,omitempty"`
	Messages    []AnthropicMessage `json:"messages"`
	Tools       []AnthropicTool    `json:"tools,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

// AnthropicSystem is the request's top-level system prompt. Anthropic's wire
// format allows either a bare string or a list of content blocks
// ("system": [{"type":"text","text":"..."}]), and real clients send both —
// Claude Desktop/Code send the block form, plain curl and simple clients send
// the string. Declaring it as `string` rejected the block form outright, with
// a 400 before any routing happened, which made Arbiter unreachable for the
// clients that speak its own native format. The block form's text blocks are
// joined; non-text blocks carry no system prompt (Anthropic documents text
// only here) so they contribute nothing rather than failing.
type AnthropicSystem string

// UnmarshalJSON accepts a string or an array of content blocks.
func (s *AnthropicSystem) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*s = ""
		return nil
	}

	if trimmed[0] == '"' {
		var plain string
		if err := json.Unmarshal(trimmed, &plain); err != nil {
			return err
		}
		*s = AnthropicSystem(plain)
		return nil
	}

	var blocks []AnthropicContent
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return err
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	*s = AnthropicSystem(strings.Join(parts, "\n\n"))
	return nil
}

// MarshalJSON always emits the string form. Anthropic accepts both, and the
// string form is what a normalizing proxy should send: it is the shape a
// single-prompt request has anyway, and it keeps the outbound body identical
// regardless of which shape the client used.
func (s AnthropicSystem) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(s))
}

// AnthropicMessage is a message in Anthropic wire format. Its Content accepts
// either a bare string or a list of content blocks — Anthropic allows both, and
// simple clients (and plain curl) send the string form, which a plain
// `[]AnthropicContent` field rejected at parse time.
type AnthropicMessage struct {
	Role    string             `json:"role"`
	Content []AnthropicContent `json:"content"`
}

// UnmarshalJSON normalizes a bare-string `content` into a single text block so
// the rest of the pipeline only ever sees the block form.
func (m *AnthropicMessage) UnmarshalJSON(data []byte) error {
	// An alias avoids recursing into this method for the block-list case.
	type messageAlias struct {
		Role    string             `json:"role"`
		Content []AnthropicContent `json:"content"`
	}

	var probe struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}

	m.Role = probe.Role

	trimmed := bytes.TrimSpace(probe.Content)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		m.Content = nil
		return nil
	}
	if trimmed[0] == '"' {
		var plain string
		if err := json.Unmarshal(trimmed, &plain); err != nil {
			return err
		}
		if plain == "" {
			m.Content = nil
			return nil
		}
		m.Content = []AnthropicContent{{Type: "text", Text: plain}}
		return nil
	}

	var alias messageAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	m.Content = alias.Content
	return nil
}

// AnthropicContent is a content block in Anthropic wire format.
type AnthropicContent struct {
	Type string `json:"type"`

	Text string `json:"text,omitempty"` // text, thinking

	ID    string                 `json:"id,omitempty"`    // tool_use
	Name  string                 `json:"name,omitempty"`  // tool_use
	Input map[string]interface{} `json:"input,omitempty"` // tool_use

	ToolUseID string      `json:"tool_use_id,omitempty"` // tool_result
	Content   interface{} `json:"content,omitempty"`     // tool_result (string or blocks)
	IsError   bool        `json:"is_error,omitempty"`    // tool_result
}

// AnthropicTool is a tool definition in Anthropic wire format.
type AnthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// OpenAIRequest represents a raw OpenAI /chat/completions request.
type OpenAIRequest struct {
	Model       string          `json:"model"`
	Messages    []OpenAIMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
	Tools       []OpenAITool    `json:"tools,omitempty"`
	Stream      bool            `json:"stream,omitempty"`

	// StreamOptions is sent on streaming requests to ask the upstream for a
	// terminal usage chunk. Without it the provider reports no token counts on
	// a stream at all, so every streamed request would be recorded with zero
	// tokens, zero cost, and no cache-read figure — which is also the only
	// number that shows whether prompt-cache affinity is working.
	StreamOptions *OpenAIStreamOptions `json:"stream_options,omitempty"`
}

// OpenAIStreamOptions carries the streaming request options OpenRouter and
// OpenAI both accept.
type OpenAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// OpenAIMessage is a message in OpenAI wire format. Content may be a plain
// string (simple case) or a list of parts (multimodal); tool calls live in
// ToolCalls on assistant messages, tool results are role="tool" messages.
type OpenAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"` // role == "tool"
}

// OpenAIToolCall is a tool call requested by the assistant.
type OpenAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"` // "function"
	Function OpenAIFunctionCall `json:"function"`
}

// OpenAIFunctionCall is the function name/arguments of a tool call.
type OpenAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded
}

// OpenAITool is a tool in OpenAI wire format (function definition).
type OpenAITool struct {
	Type     string         `json:"type"` // "function"
	Function OpenAIFunction `json:"function"`
}

// OpenAIFunction is the function definition inside an OpenAITool.
type OpenAIFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters"`
}
