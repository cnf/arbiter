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

// Signals is the output of classification: the axes a router matches on.
// Each axis is independently classified and independently overridable by a
// force-alias, so no axis is derived from another.
type Signals struct {
	Domain               string   // "code_generation", "reasoning", "debugging", "chat", "discovery"
	Effort               string   // "easy", "medium", "hard"
	RequiredCapabilities []string // "vision", "tool_use", "long_context"
	EstimatedTokens      int
	CostClass            string  // "free_only", "budget", "quality_first"
	Confidence           float64 // 0.0-1.0
}

// KnownAxes lists the axis names a force-alias may target. Keys are the
// canonical (current) names; the config layer also accepts the deprecated
// spellings and maps them onto these.
var KnownAxes = []string{"domain", "effort", "cost_class", "capabilities"}

// CostTier represents a cost/quota tier.
type CostTier struct {
	Name              string
	MaxPerMinute      float64 // USD
	MaxPerDay         float64 // USD
	FallbackProviders []string
	Priority          int
}
