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

	// ClassifierCalls carries diagnostics for any sub-classifier that made its
	// own upstream request to produce a signal (e.g. an LLM-backed domain
	// classifier) — nil/empty for classifiers that don't (the heuristic ones).
	// The pipeline reads this to record one store event per call, tagged
	// kind="classifier", alongside the real request's own event. A slice, not
	// a single value, so more than one LLM-backed axis can coexist later
	// without reworking this seam again.
	ClassifierCalls []*ClassifierCallInfo
}

// ClassifierCallInfo is one upstream call a classifier made on its own behalf
// while producing a Signals value. Provider/Model/Usage/LatencyMs/StatusCode
// describe the call that was actually attempted (the one that determined the
// outcome — the last one tried, on either success or exhausted failure).
// Error is empty on success.
//
// RawReply is the model's literal text reply and Input/SystemPrompt are what it
// was asked — the whole prompt, not just the text classified. The reply alone
// shows what the classifier decided, not what it saw, which is exactly what a
// wrong verdict needs: without the input there is no way to tell a model that
// misjudged a clear message from a rubric that failed to describe the category.
// The pipeline stores the input as captured content (see recordClassifierCalls),
// gated on storage.capture_content like every other path that writes
// conversation text to disk.
type ClassifierCallInfo struct {
	Provider     string
	Model        string
	LatencyMs    int64
	Usage        Usage
	StatusCode   int
	Error        string
	RawReply     string
	Input        string
	SystemPrompt string
}

// KnownAxes lists the axis names a force-alias may target. Keys are the
// canonical (current) names; the config layer also accepts the deprecated
// spellings and maps them onto these.
var KnownAxes = []string{"domain", "effort", "cost_class", "capabilities"}
