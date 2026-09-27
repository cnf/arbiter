package types

import "time"

// NormalizedResponse is the canonical internal representation of a response.
type NormalizedResponse struct {
	Content    []ContentBlock
	StopReason string
	Model      string // model that actually served the request, for echoing back to the client
	Usage      Usage
	TraceID    string
	RateLimit  *RateLimitInfo // populated by upstream client from response headers, if any
}

// NormalizedStreamEvent is an event emitted during streaming response. Used
// internally to represent a unified stream format before translation to the
// client's requested wire format (Anthropic SSE or OpenAI SSE).
type NormalizedStreamEvent struct {
	TraceID           string // Arbiter's request trace id, for correlating events with logs
	Type              string // "message_start", "content_block_start", "content_block_delta", "message_delta", "message_stop"
	MessageID         string // for message_start, message_delta, message_stop
	MessageModel      string // for message_start
	MessageStopReason string // for message_stop
	BlockIndex        int    // for content_block_* (which block in content)
	BlockType         string // for content_block_start (e.g. "text", "tool_use")
	DeltaType         string // for content_block_delta (e.g. "text_delta", "tool_use_delta")
	TextDelta         string // for text_delta
	InputTokens       int    // for message_start (accumulated through message)
	OutputTokens      int    // for message_delta (cumulative at this point)

	// Tool-call fragments, for DeltaType == "tool_use_delta". Upstreams stream
	// a tool call as one fragment per chunk: the first carries the id and
	// function name, every later one carries a slice of the JSON arguments
	// string. They are relayed fragment-for-fragment rather than reassembled —
	// the client concatenates arguments by ToolCallIndex, which is the contract
	// OpenAI clients already implement. Reassembling here would mean buffering
	// the whole call before emitting anything, changing latency for no gain.
	ToolCallIndex int    // position of this call within the message's tool_calls
	ToolCallID    string // set on the first fragment only
	ToolCallName  string // set on the first fragment only
	ToolCallArgs  string // a slice of the arguments JSON, per fragment

	// Reasoning is vendor reasoning text (OpenRouter et al stream it as
	// `reasoning` / `reasoning_content`). It is not part of the OpenAI spec, so
	// it is relayed verbatim under the field the upstream used; a client that
	// does not know the field ignores it, and one that does (agent CLIs showing
	// a thinking trace) keeps working.
	Reasoning string
	// Signature carries Anthropic's opaque per-thinking-block token through
	// the relay. It is not content and has no OpenAI equivalent; it exists so
	// an Anthropic client can replay the block on its next request, and the
	// Anthropic translator re-emits it. The OpenAI translator drops it.
	Signature string
	// CacheReadTokens/CacheWriteTokens are carried on whichever event the
	// upstream reported them on, so the streaming path can account for prompt
	// cache usage the same way the non-streaming path does.
	CacheReadTokens  int
	CacheWriteTokens int
	// CostUSD is a provider-reported cost when the upstream sends one
	// (OpenRouter does, on the usage chunk).
	CostUSD float64
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

	// ExplicitModel marks a route the client named directly (a concrete model
	// declared by a provider), rather than one Arbiter chose — via an alias, a
	// pin, a policy rule, or classification.
	//
	// It changes failure behaviour: an explicit model is never silently
	// substituted. The client asked for that model specifically, so serving a
	// different one is not a valid answer — a rate-limited explicit model returns
	// the 429 to the client instead of quietly falling through to a fallback
	// provider. For every other route the fallback chain is exactly right, since
	// Arbiter made the choice and any equivalent model satisfies the request.
	ExplicitModel bool
}
