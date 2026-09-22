package types

// ModelCost is one entry in the cost/latency catalog, keyed by
// provider+model. Costs are US dollars per million tokens. LatencyMsP50 is
// a static estimate supplied by config; an empirical source (observed
// latencies) can populate the same struct later without changing callers.
//
// It also carries what the model can do, because two consumers need that and
// only one is about routing: the cost-aware group selector ignores it, while
// /models advertises it so a client can tell whether a model accepts an image
// before it sends one. Capabilities live here rather than in a parallel table
// so a single catalog row answers both questions.
type ModelCost struct {
	Provider          string
	Model             string
	InputCostPerMTok  float64
	OutputCostPerMTok float64
	LatencyMsP50      int

	// InputModalities is what the model accepts ("text", "image", "file").
	// nil means UNKNOWN — see config.ModelCatalogEntry.InputModalities; a
	// nil must never be rendered as an empty or text-only capability set.
	InputModalities []string

	// MaxInputTokens / MaxOutputTokens are nil when unstated.
	MaxInputTokens  *int
	MaxOutputTokens *int

	// Metadata is free-form extra data, carried through to /models and read
	// by nothing. See config.ModelCatalogEntry.Metadata.
	Metadata map[string]interface{}
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

	// RequestKind says WHAT the request is, as opposed to what it is about:
	// "title" for a client's title-generation call, and later "subagent" for
	// a delegated worker's own traffic.
	//
	// It is deliberately NOT an axis. Domain/Effort/CostClass are *routing*
	// inputs — they are contested in the merge by confidence and a force-alias
	// may override them — whereas a request's kind is a fact about the request
	// that routing does not consume. So it carries no confidence, is not in
	// KnownAxes, and no force-alias can target it. What it buys is
	// identification: a title request has no meaningful content domain, and
	// before this existed the only way to spot one on a row was to read its
	// system prompt.
	//
	// Distinct from store.Event.Kind, which is *who sent* the request
	// ("client" traffic vs Arbiter's own "classifier" calls). A title request
	// is a client request with kind "client" and RequestKind "title".
	RequestKind string

	// AxisConfidence carries a confidence PER AXIS, for a classifier that fills
	// more than one axis from a single call.
	//
	// Confidence above stays the "how sure was this classifier overall" number,
	// which is exact for a one-axis classifier and a lie for a multi-axis one:
	// a decision-model call answering domain at 0.98 and cost_class at 0.61 has
	// no single honest value, and reporting 0.98 for both would let the
	// cost_class verdict beat a legitimate 0.70 classifier on that axis.
	//
	// MergedClassifier keys its per-axis pick on this when present, falling back
	// to Confidence when absent — so every existing classifier (which leaves
	// this nil) keeps its exact current behaviour. Consumers that want "how sure
	// was this request's classification" should keep reading Confidence, which
	// is the highest per-axis value in that case.
	AxisConfidence map[string]float64

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

	// Verdict is a classifier-supplied one-line description of this call's
	// outcome, used verbatim as the stored routing rationale (the pipeline
	// appends the input preview). It exists so each classifier type owns its
	// own wording: a decisions call's outcome is a set of axis values with
	// probabilities, which the LLM classifier's "replied %q" phrasing cannot
	// express. Empty means the pipeline falls back to that phrasing, which is
	// what every pre-existing call site still gets.
	Verdict string

	// Axes are the axis values THIS call filled, keyed by axis name.
	//
	// Per-call, not the merged Signals: a classifier row describes one upstream
	// call, and the merged value mixes in whatever other classifiers concluded.
	// Without this a multi-axis decisions call recorded only its domain, so
	// cost_class and effort were visible in the rationale text but empty as
	// fields — stored, and unqueryable.
	Axes map[string]string

	// AxisConfidence is the confidence per axis THIS call reported, for the
	// axes in Axes. A multi-axis call has no single honest confidence, so the
	// row stores the highest of these (the same rule Signals.Confidence
	// follows) rather than a value borrowed from another classifier.
	//
	// Without it a classifier row's confidence column was written as nothing at
	// all and read back as 0.0% on the request detail page — the verdict and its
	// certainty were both known and only the verdict was stored.
	AxisConfidence map[string]float64
}

// Capability names. These are the values a classifier fills
// Signals.RequiredCapabilities with and a policy rule's `capabilities:`
// matches against.
//
// CapToolUse, CapLongContext and CapVision are the pre-existing vocabulary the
// heuristics already emit, kept unchanged so existing configs and rules keep
// meaning what they meant. CapAttachment is new and deliberately distinct from
// CapVision: a request carrying an image/document block is a FACT about the
// bytes, whereas "vision" was always a guess from the text. A rule may match
// either, and an operator who wants the certainty can now ask for it.
const (
	CapVision      = "vision"
	CapToolUse     = "tool_use"
	CapLongContext = "long_context"
	CapAttachment  = "attachment"
)

// KnownCapabilities is the set a `detect:` block may name, in the order an
// error message should list them.
var KnownCapabilities = []string{CapToolUse, CapAttachment, CapVision, CapLongContext}

// Axis names. Canonical, and the source of truth KnownAxes is built from, so
// the config package can name an axis without importing internal/classifier
// (which it cannot reach).
const (
	AxisDomainName       = "domain"
	AxisEffortName       = "effort"
	AxisCostClassName    = "cost_class"
	AxisCapabilitiesName = "capabilities"
)

// KnownAxes lists the axis names a force-alias may target. Keys are the
// canonical (current) names; the config layer also accepts the deprecated
// spellings and maps them onto these.
var KnownAxes = []string{AxisDomainName, AxisEffortName, AxisCostClassName, AxisCapabilitiesName}

// UnmatchedValue is the reserved sentinel a scalar axis (domain, effort,
// cost_class) is filled with when a model-backed classifier reaches its
// escape verdict — the configured `escape:` label, or the auto-added
// `other` when a decisions classifier declares none. Before this existed,
// an escape verdict filled no axis at all: no `when:` rule could match it,
// every wildcard rule matched it, and a merge silently discarded it in
// favour of any other classifier's value, real or not. `unmatched` makes
// that outcome a first-class value an operator can write `when: {domain:
// unmatched}` against — see MergedClassifier.Classify for the merge rule
// (a real value always beats it; it only fills the axis when nothing else
// did) and validateLLMClassifiers/validateDecisionsClassifiers for why a
// configured label may never be named this.
//
// Deliberately not used for the capabilities axis: RequiredCapabilities is
// an additive set (a request needs vision AND tool_use), not a single
// contested value, so "nothing matched" has no analogous sentinel there —
// an unfilled capabilities axis is just an empty slice, same as before.
const UnmatchedValue = "unmatched"
