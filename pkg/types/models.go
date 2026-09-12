package types

// ModelInfo holds metadata about a specific model.
type ModelInfo struct {
	Name            string
	Provider        string
	ContextWindow   int
	MaxOutputTokens int
	CostPerMTok     float64 // per million tokens (input)
	HasVision       bool
	HasTools        bool
	HasSearching    bool
}

// ModelRegistry is a lookup table for model metadata.
type ModelRegistry map[string]ModelInfo

// Signals is the output of classification: intent, capabilities, cost sensitivity.
type Signals struct {
	Intent               string   // "code_generation", "reasoning", "debugging", "chat", "discovery"
	RequiredCapabilities []string // "vision", "tool_use", "long_context"
	EstimatedTokens      int
	CostSensitivity      string  // "free_only", "budget", "quality_first"
	Confidence           float64 // 0.0-1.0
}

// CostTier represents a cost/quota tier.
type CostTier struct {
	Name              string
	MaxPerMinute      float64 // USD
	MaxPerDay         float64 // USD
	FallbackProviders []string
	Priority          int
}
