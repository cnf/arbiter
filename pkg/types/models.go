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

	// CacheReadCostPerMTok / CacheWriteCostPerMTok price a cache-hit and a
	// cache-creation token respectively, when the source states them (e.g. an
	// Anthropic model served through a provider that reports prompt-cache
	// pricing). Zero means "unstated", not "free" — computeCost only adds a
	// cache term when the catalog carries a rate, so a model with no cache
	// pricing data simply prices cache tokens at 0 today, same as before this
	// field existed.
	CacheReadCostPerMTok  float64
	CacheWriteCostPerMTok float64

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
	Difficulty           string   // "easy", "medium", "hard"
	RequiredCapabilities []string // "vision", "tool_use", "long_context"
	EstimatedTokens      int
	CostClass            string  // "free_only", "budget", "quality_first"
	Confidence           float64 // 0.0-1.0

	// Tags is a freeform, operator-owned label set: arbitrary strings
	// ("python", "french", "user_is_angry") Arbiter assigns NO meaning to.
	// The router matches them by set membership (`when: {tags: [python]}`),
	// and the operator decides what they mean; combined with a fixed axis
	// (e.g. domain=coding + tags=[python]) this is how per-user splits are
	// expressed without growing the fixed axis set.
	//
	// DISTINCT from RequiredCapabilities, and the distinction is subtractive
	// rather than additive: capabilities mean "the model must be able to do
	// X" — strict, and Arbiter knows it maps to a catalog property that
	// `requires_input_modalities` consumes. Tags mean the opposite: "we
	// assert nothing about X, match the string." Same additive mechanics
	// (see below), different contract; the two are never merged into one
	// field.
	//
	// Additive like RequiredCapabilities, not contested like Domain: tags
	// union across classifiers with no confidence contest and no
	// UnmatchedValue sentinel (a tag is "add this"; there is no winner).
	// Deliberately NOT a target guard — a tag filters which rules/targets a
	// request matches, never which models are viable.
	Tags []string

	// RequestKind says WHAT the request is, as opposed to what it is about:
	// "title" for a client's title-generation call, and later "subagent" for
	// a delegated worker's own traffic.
	//
	// It is deliberately NOT an axis. Domain/Difficulty/CostClass are *routing*
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

	// ClientEffort is the reasoning-effort knob the CLIENT actually sent,
	// carried verbatim from the request. Either wire spelling feeds it:
	// `output_config.effort` (Anthropic) or `reasoning_effort` (OpenAI).
	//
	// Like RequestKind it is deliberately NOT an axis: it is a fact about
	// what the client asked for, not a contested classification, so it
	// carries no confidence, is not in KnownAxes, and no classifier fills it.
	// It is stamped from req.OutputEffort before routing so a `when: {effort:
	// ...}` rule can match the client's own value.
	//
	// Distinct from OutputEffort on the request itself: that is the value
	// that goes upstream, and a force-alias/policy rule may later LOCK it to
	// something else. ClientEffort always records what the client sent,
	// regardless of any lock, so the UI can show the client's intent next to
	// what was actually served.
	ClientEffort string

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
	// cost_class and difficulty were visible in the rationale text but empty as
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
	AxisDifficultyName   = "difficulty"
	AxisCostClassName    = "cost_class"
	AxisCapabilitiesName = "capabilities"
	AxisTagsName         = "tags"
)

// KnownAxes lists the axis names a force-alias may target. Keys are the
// canonical (current) names; the config layer also accepts the deprecated
// spellings and maps them onto these.
//
// Adding a name here is what makes it a usable axis everywhere at once: the
// config layer builds canonicalAxisSet/knownAxisSet FROM this list, so a new
// entry is accepted as a classifier `axis:`, as a force-alias `force:` key,
// and (via policybuild's whenKeys) as a `when:` key. AxisTagsName is additive
// like capabilities rather than contested like domain — see Signals.Tags.
var KnownAxes = []string{AxisDomainName, AxisDifficultyName, AxisCostClassName, AxisCapabilitiesName, AxisTagsName}

// UnmatchedValue is the reserved sentinel a scalar axis (domain, difficulty,
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
