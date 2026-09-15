package types

import "time"

// NormalizedResponse is the canonical internal representation of a response.
type NormalizedResponse struct {
	Content         []ContentBlock
	StopReason      string
	Model           string // model that actually served the request, for echoing back to the client
	Usage           Usage
	TraceID         string
	RoutingDecision string
	RateLimit       *RateLimitInfo // populated by upstream client from response headers, if any
}

// NormalizedStreamEvent is an event emitted during streaming response. Used
// internally to represent a unified stream format before translation to the
// client's requested wire format (Anthropic SSE or OpenAI SSE).
type NormalizedStreamEvent struct {
	TraceID        string         // Arbiter's request trace id, for correlating events with logs
	Type           string         // "message_start", "content_block_start", "content_block_delta", "message_delta", "message_stop"
	MessageID      string         // for message_start, message_delta, message_stop
	MessageModel   string         // for message_start
	MessageStopReason string       // for message_stop
	BlockIndex     int            // for content_block_* (which block in content)
	BlockType      string         // for content_block_start (e.g. "text", "tool_use")
	DeltaType      string         // for content_block_delta (e.g. "text_delta", "tool_use_delta")
	TextDelta      string         // for text_delta
	InputTokens    int            // for message_start (accumulated through message)
	OutputTokens   int            // for message_delta (cumulative at this point)
}

// Usage tracks token consumption and (if the upstream reports it) cost.
type Usage struct {
	InputTokens  int
	OutputTokens int
	CacheRead    int     // cached input tokens
	CacheWrite   int     // cache creation tokens
	CostUSD      float64 // 0 if upstream doesn't report cost (e.g. plain Anthropic API)
}

// RateLimitInfo is quota/rate-limit state extracted from upstream response
// headers (post_call guardrail territory) — used both for logging and as a
// routing input ("degrade to OpenRouter when Anthropic's 5h window is low").
type RateLimitInfo struct {
	RequestsRemaining  int
	RequestsLimit      int
	TokensRemaining    int
	TokensLimit        int
	ResetAt            time.Time
	Unified5hRemaining float64 // 0.0-1.0, Anthropic unified quota (Pro/Max), -1 if not applicable
	Unified5hReset     time.Time
}

// AnthropicResponse represents a raw Anthropic /v1/messages response.
type AnthropicResponse struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Role         string             `json:"role"`
	Content      []AnthropicContent `json:"content"`
	Model        string             `json:"model"`
	StopReason   string             `json:"stop_reason"`
	StopSequence string             `json:"stop_sequence,omitempty"`
	Usage        AnthropicUsage     `json:"usage"`
}

// AnthropicUsage is usage info in an Anthropic response.
type AnthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// OpenAIResponse represents a raw OpenAI /chat/completions response.
type OpenAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   OpenAIUsage    `json:"usage"`
}

// OpenAIChoice is a choice in an OpenAI response.
type OpenAIChoice struct {
	Index        int           `json:"index"`
	Message      OpenAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

// OpenAIUsage is usage info in an OpenAI response (OpenRouter extends this
// with cost fields; plain OpenAI omits them, which is fine — zero value).
type OpenAIUsage struct {
	PromptTokens     int                    `json:"prompt_tokens"`
	CompletionTokens int                    `json:"completion_tokens"`
	TotalTokens      int                    `json:"total_tokens"`
	Cost             float64                `json:"cost,omitempty"` // OpenRouter
	CostDetails      map[string]float64     `json:"cost_details,omitempty"`
	PromptDetails    map[string]interface{} `json:"prompt_tokens_details,omitempty"`
}

// ProviderConfig holds connection info for an upstream provider.
type ProviderConfig struct {
	Name     string // config key, e.g. "claude"
	Type     string // "anthropic" or "openai" (wire format the upstream speaks)
	Endpoint string
	APIKey   string
	Models   []string
	Headers  map[string]string
	Timeout  time.Duration
	RetryMax int
	CacheTTL time.Duration // 0 means "use the pipeline's default session affinity TTL"
}

// Route is the routing decision: which provider/model handles this request.
type Route struct {
	Provider  string
	Model     string
	Config    ProviderConfig
	Rationale string // one-line explanation, always populated

	// Fallbacks are extra candidate routes to try, in order, if this one
	// fails retriably — populated when the route came from a group alias, so
	// the group's unselected members form its fallback chain. The pipeline
	// tries these before the global routing.fallback_providers list.
	Fallbacks []Route
}

// Metadata augments a Route with runtime info captured at decision time.
type Metadata struct {
	CostTier       string
	QuotaRemaining int
	LatencyTarget  string // "fast", "normal", "quality"
	TraceID        string
	RoutedAt       time.Time
}
