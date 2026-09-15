package types

// ModelCost is one entry in the cost/latency catalog, keyed by
// provider+model. Costs are US dollars per million tokens. LatencyMsP50 is
// a static estimate supplied by config; an empirical source (observed
// latencies) can populate the same struct later without changing callers.
type ModelCost struct {
	Provider          string
	Model             string
	InputCostPerMTok  float64
	OutputCostPerMTok float64
	LatencyMsP50      int
}

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

