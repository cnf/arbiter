package types

// NormalizedRequest is the canonical internal representation of a request,
// independent of Anthropic or OpenAI format.
type NormalizedRequest struct {
	// Core request data
	Messages     []Message
	Model        string
	MaxTokens    int
	Temperature  float64
	SystemPrompt string
	Tools        []Tool

	// Tracking
	OriginalFormat string // "anthropic" or "openai"
	OriginalPayload []byte
	TraceID        string
}

// Message represents a conversation turn.
type Message struct {
	Role    string        // "user", "assistant"
	Content interface{}   // string or []ContentBlock
}

// ContentBlock is a single piece of content in a message.
type ContentBlock struct {
	Type string      // "text", "tool_use", "image", etc.
	Data interface{} // specific to Type
}

// Tool is a function/tool definition the model can call.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]interface{}
}

// AnthropicRequest represents a raw Anthropic /v1/messages request.
type AnthropicRequest struct {
	Model            string                 `json:"model"`
	MaxTokens        int                    `json:"max_tokens"`
	Temperature      float64                `json:"temperature,omitempty"`
	System           string                 `json:"system,omitempty"`
	Messages         []AnthropicMessage     `json:"messages"`
	Tools            []AnthropicTool        `json:"tools,omitempty"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"`
}

// AnthropicMessage is a message in Anthropic format.
type AnthropicMessage struct {
	Role    string                `json:"role"`
	Content []AnthropicContent    `json:"content"`
}

// AnthropicContent is a content block in Anthropic format.
type AnthropicContent struct {
	Type string      `json:"type"`
	Text string      `json:"text,omitempty"`
	// TODO: extend for tool_use, image, etc.
}

// AnthropicTool is a tool in Anthropic format.
type AnthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// OpenAIRequest represents a raw OpenAI /chat/completions request.
type OpenAIRequest struct {
	Model       string                 `json:"model"`
	Messages    []OpenAIMessage        `json:"messages"`
	MaxTokens   int                    `json:"max_tokens,omitempty"`
	Temperature float64                `json:"temperature,omitempty"`
	Tools       []OpenAITool           `json:"tools,omitempty"`
	ToolChoice  interface{}            `json:"tool_choice,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// OpenAIMessage is a message in OpenAI format.
type OpenAIMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []OpenAIContent
}

// OpenAIContent is a content block in OpenAI format.
type OpenAIContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// TODO: extend for tool_use, image, etc.
}

// OpenAITool is a tool in OpenAI format (function definition).
type OpenAITool struct {
	Type     string                 `json:"type"` // "function"
	Function OpenAIFunction         `json:"function"`
}

// OpenAIFunction is the function definition inside an OpenAITool.
type OpenAIFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters"`
}
