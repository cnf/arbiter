package types

import "time"

// NormalizedResponse is the canonical internal representation of a response.
type NormalizedResponse struct {
	Content        []ContentBlock
	StopReason     string
	Usage          Usage
	TraceID        string
	RoutingDecision string
}

// Usage tracks token consumption.
type Usage struct {
	InputTokens  int
	OutputTokens int
	CacheRead    int // cached input tokens
	CacheWrite   int // cache creation tokens
}

// AnthropicResponse represents a raw Anthropic /v1/messages response.
type AnthropicResponse struct {
	ID           string                 `json:"id"`
	Type         string                 `json:"type"`
	Role         string                 `json:"role"`
	Content      []AnthropicContent     `json:"content"`
	Model        string                 `json:"model"`
	StopReason   string                 `json:"stop_reason"`
	StopSequence string                 `json:"stop_sequence,omitempty"`
	Usage        AnthropicUsage         `json:"usage"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

// AnthropicUsage is usage info in Anthropic response.
type AnthropicUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_input_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// OpenAIResponse represents a raw OpenAI /chat/completions response.
type OpenAIResponse struct {
	ID                string                 `json:"id"`
	Object            string                 `json:"object"`
	Created           int64                  `json:"created"`
	Model             string                 `json:"model"`
	Choices           []OpenAIChoice         `json:"choices"`
	Usage             OpenAIUsage            `json:"usage"`
	SystemFingerprint string                 `json:"system_fingerprint,omitempty"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
}

// OpenAIChoice is a choice in an OpenAI response.
type OpenAIChoice struct {
	Index        int          `json:"index"`
	Message      OpenAIMessage `json:"message"`
	FinishReason string       `json:"finish_reason"`
	LogProbs     interface{}  `json:"logprobs,omitempty"`
}

// OpenAIUsage is usage info in OpenAI response.
type OpenAIUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails map[string]int `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails map[string]int `json:"completion_tokens_details,omitempty"`
}

// ProviderConfig holds connection info for an upstream provider.
type ProviderConfig struct {
	Type      string                 // "anthropic", "openai", etc.
	Endpoint  string
	APIKey    string
	Models    []string
	Headers   map[string]string
	Timeout   time.Duration
	RetryMax  int
}

// Route is the routing decision: which provider handles this request.
type Route struct {
	Provider  string
	Model     string
	Config    ProviderConfig
	Rationale string // one-line explanation
}

// Metadata augments a Route with runtime info.
type Metadata struct {
	CostTier        string
	QuotaRemaining  int
	LatencyTarget   string // "fast", "normal", "quality"
	TraceID         string
	RoutedAt        time.Time
}
