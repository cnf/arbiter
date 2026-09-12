package types

// NormalizedRequest is the canonical internal representation of a request,
// independent of Anthropic or OpenAI wire format.
type NormalizedRequest struct {
	Messages     []Message
	Model        string
	MaxTokens    int
	Temperature  float64
	SystemPrompt string
	Tools        []Tool

	// Tracking
	OriginalFormat  string // "anthropic" or "openai"
	OriginalPayload []byte
	TraceID         string
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

// AnthropicRequest represents a raw Anthropic /v1/messages request.
type AnthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature,omitempty"`
	System      string             `json:"system,omitempty"`
	Messages    []AnthropicMessage `json:"messages"`
	Tools       []AnthropicTool    `json:"tools,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

// AnthropicMessage is a message in Anthropic wire format.
type AnthropicMessage struct {
	Role    string             `json:"role"`
	Content []AnthropicContent `json:"content"`
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
