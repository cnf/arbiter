package classifier

import (
	"context"
	"strings"

	"github.com/cnf/arbiter/pkg/types"
)

// Classifier analyzes a request and produces routing signals.
type Classifier interface {
	Classify(ctx context.Context, req *types.NormalizedRequest) (types.Signals, error)
}

// Factory creates a Classifier from config.
type Factory func(name string, config map[string]interface{}) (Classifier, error)

// Registry holds all registered classifier factories.
var Registry = make(map[string]Factory)

// Register registers a classifier factory by type name.
func Register(typeName string, factory Factory) {
	Registry[typeName] = factory
}

// Axis names a Signals field a classifier produces. Each heuristic instance
// declares which axis it fills, so several instances can run side by side
// (domain, effort, capabilities) without overwriting each other's axis —
// the merger keys off this rather than off the classifier's name.
const (
	AxisDomain       = types.AxisDomainName
	AxisEffort       = types.AxisEffortName
	AxisCapabilities = types.AxisCapabilitiesName
	AxisCostClass    = types.AxisCostClassName
)

// HeuristicClassifier uses keyword matching against the last user message
// to guess one axis. It's deliberately dumb — a starting point, not a final
// answer. The multi-axis split in arbiter.yaml is handled by running several
// instances of this same type with different keyword maps and axis settings,
// merged via MergedClassifier, rather than baking each axis into the type.
//
// It can also carry a RequestMatcher, which is the opposite kind of guess: an
// exact structural signature (a client's known preamble, say) rather than a
// keyword hit. When one is configured and hits, it wins outright and the
// keywords are not consulted — a request that provably IS a title generation
// is not a candidate for keyword guessing.
type HeuristicClassifier struct {
	name     string
	axis     string              // which Signals field this instance fills
	keywords map[string][]string // axis value -> keywords

	// matcher, when non-nil, matches the request's own text (system prompt or
	// messages) rather than the last user message. See RequestMatcher.
	matcher *RequestMatcher

	// detect, when non-empty, names capabilities to test STRUCTURALLY against
	// the request (tools present, attachment present, over a token threshold)
	// rather than by keyword. Only meaningful on the capabilities axis, and
	// unioned with whatever keywords also matched — a request can need vision
	// and tool_use at once. See capabilityPredicate.
	detect            []string
	longContextTokens int
}

// NewHeuristicClassifier creates a heuristic classifier filling axis. An
// empty axis means AxisDomain, which is what every pre-existing config
// (written before axes were declared) means.
func NewHeuristicClassifier(name, axis string, keywords map[string][]string) *HeuristicClassifier {
	return NewHeuristicClassifierWithMatch(name, axis, keywords, nil)
}

// NewHeuristicClassifierWithMatch creates a heuristic classifier that consults
// matcher first. A nil matcher is exactly NewHeuristicClassifier's behaviour.
func NewHeuristicClassifierWithMatch(name, axis string, keywords map[string][]string, matcher *RequestMatcher) *HeuristicClassifier {
	return NewHeuristicClassifierFull(name, axis, keywords, matcher, nil, 0)
}

// NewHeuristicClassifierFull creates a heuristic classifier with every
// optional capability. detect names capabilities tested structurally;
// longContextTokens is the threshold long_context is measured against (0 means
// unconfigured, which config validation rejects rather than letting it never
// fire).
func NewHeuristicClassifierFull(name, axis string, keywords map[string][]string, matcher *RequestMatcher, detect []string, longContextTokens int) *HeuristicClassifier {
	if axis == "" {
		axis = AxisDomain
	}
	return &HeuristicClassifier{
		name:              name,
		axis:              axis,
		keywords:          keywords,
		matcher:           matcher,
		detect:            detect,
		longContextTokens: longContextTokens,
	}
}

// DecisiveMatch reports whether a decisive matcher on this classifier HIT.
// False for a classifier with no matcher, and false for a decisive matcher
// that did not match — a miss must fall through, not stop the merge.
//
// Deliberately stateless: classifiers are built once at config load and shared
// across concurrent requests, so recording "I matched" on the struct would be a
// data race. This re-evaluates the match instead, which is cheap (string
// comparison) and means the answer cannot go stale.
func (hc *HeuristicClassifier) DecisiveMatch(req *types.NormalizedRequest) bool {
	return hc.matcher.Decisive() && hc.matcher.Match(req)
}

// decisiveMatcher is implemented by a classifier that can end classification
// outright. A narrow interface rather than a field on Classifier, so the two
// model-backed types and every pre-existing classifier are untouched.
type decisiveMatcher interface {
	DecisiveMatch(req *types.NormalizedRequest) bool
}

// modelBacked is the shared skeleton of the two classifiers that call an
// upstream model (LLMClassifier and DecisionsClassifier). Both try a model
// call, and on failure defer entirely to a wrapped fallback — keeping the
// failed attempt's diagnostics so the stored row still explains what happened.
//
// The two Classify methods were otherwise identical for ~20 lines, including
// the same "fallback failed too, but still keep the call diagnostics" comment;
// only the success half (one label vs several verdicts) differs. Splitting the
// prologue out means the failure policy has one home and the two types only
// supply what is genuinely theirs.
//
// tryFn reports ok=false when the model call failed outright; call is still
// non-nil in that case so the failure is recorded.
func modelBacked[V any](
	ctx context.Context,
	req *types.NormalizedRequest,
	fallback Classifier,
	tryFn func() (verdicts V, call *types.ClassifierCallInfo, ok bool),
	onSuccess func(sig *types.Signals, verdicts V),
) (types.Signals, error) {
	verdicts, call, ok := tryFn()
	if !ok {
		sig := types.Signals{}
		if fallback != nil {
			if fb, err := fallback.Classify(ctx, req); err == nil {
				sig = fb
			}
			// A fallback error is a config/programmer error, not a network
			// one — HeuristicClassifier never errors. Keep the model call's
			// own diagnostics rather than losing them to it.
		}
		if call != nil {
			sig.ClassifierCalls = append(sig.ClassifierCalls, call)
		}
		return sig, nil
	}

	sig := types.Signals{
		AxisConfidence:  map[string]float64{},
		ClassifierCalls: []*types.ClassifierCallInfo{call},
	}
	onSuccess(&sig, verdicts)
	return sig, nil
}

// onlyIfUnsetClassifier is implemented by a model-backed classifier that must
// not run — no upstream call, no cost — unless its axis is still empty after
// every classifier declared before it. A narrow interface so the merge can
// gate it without knowing the concrete type; heuristic classifiers never
// implement it, and config validation rejects only_if_unset on them anyway.
//
// An axis is "set" as soon as any earlier classifier filled it with a
// non-empty value — including an escape/"other" verdict, which fills no axis
// and therefore leaves it unset. For a decisions classifier, which fills
// several axes from one call, the classifier runs unless EVERY axis it fills
// is already set: the call is cheap per axis, so it is only worth skipping
// when every answer it could give is already known.
type onlyIfUnsetClassifier interface {
	gated() bool
	// gateAxes returns the axes this classifier fills. nil means "any axis"
	// (the llm classifier's declared axis covers it; unused today). The merge
	// skips the classifier only when every axis in the list is already set.
	gateAxes() []string
}

// Classify performs keyword-based classification against the last user
// message. The intent with the most keyword hits wins; ties go to whichever
// intent was declared first in config. Confidence is hits / (hits + 1), a
// cheap curve that approaches 1.0 as evidence piles up but never reaches it
// (heuristics are never fully certain) and is exactly 0 for zero hits.
//
// A configured matcher is consulted first and wins outright: a structural
// signature is not a guess, so there is nothing for keywords to add. Its
// confidence is 1.0 for the same reason — an exact match is the one thing this
// classifier can be certain of.
func (hc *HeuristicClassifier) Classify(ctx context.Context, req *types.NormalizedRequest) (types.Signals, error) {
	if hc.matcher != nil && hc.matcher.Match(req) {
		sig := types.Signals{
			EstimatedTokens: estimateTokens(req),
			Confidence:      1.0,
			RequestKind:     hc.matcher.Kind(),
		}
		// Per-axis confidence is reported only when the matcher fills an
		// axis. A kind-only signature fills none, and claiming certainty
		// about an axis it left empty would let it win that axis in the
		// merge on the strength of a value it never produced.
		if hc.matcher.Value() != "" {
			sig.AxisConfidence = map[string]float64{hc.axis: 1.0}
		}
		hc.fillAxis(&sig, hc.matcher.Value())
		return sig, nil
	}

	// The FIRST user turn with text, matching what the model-backed
	// classifiers classify. Reading the last user turn meant an agentic
	// request whose final turn was tool_result-only matched no keyword at all
	// — the same empty-selection bug, one layer cheaper.
	text := strings.ToLower(types.FirstUserText(req))

	var bestValue string
	var bestHits int
	var matched []string

	for value, kws := range hc.keywords {
		hits := 0
		for _, kw := range kws {
			if strings.Contains(text, strings.ToLower(kw)) {
				hits++
			}
		}
		if hits == 0 {
			continue
		}
		matched = append(matched, value)
		if hits > bestHits {
			bestValue = value
			bestHits = hits
		}
	}

	confidence := 0.0
	if bestHits > 0 {
		confidence = float64(bestHits) / float64(bestHits+1)
	}

	sig := types.Signals{
		EstimatedTokens: estimateTokens(req),
		Confidence:      confidence,
	}
	if bestValue != "" {
		sig.AxisConfidence = map[string]float64{hc.axis: confidence}
	}
	if hc.axis == AxisCapabilities {
		// Every matched group is a capability, not just the winner — a
		// request can need vision and tool_use at once.
		sig.RequiredCapabilities = matched
		hc.detectCapabilities(req, &sig)
	} else {
		hc.fillAxis(&sig, bestValue)
	}
	return sig, nil
}

// detectCapabilities adds each structurally-proven capability to sig, keeping
// the result order-stable (config order, then anything keywords already added).
//
// A structural hit is certain, so it raises Confidence to 1.0: the request
// demonstrably carries tools or an attachment, which is not a guess and must
// not lose a merge to a keyword classifier's hit/(hits+1) curve.
func (hc *HeuristicClassifier) detectCapabilities(req *types.NormalizedRequest, sig *types.Signals) {
	if len(hc.detect) == 0 {
		return
	}
	seen := make(map[string]bool, len(sig.RequiredCapabilities))
	for _, c := range sig.RequiredCapabilities {
		seen[c] = true
	}
	structural := false
	for _, name := range hc.detect {
		hit, answerable := capabilityPredicate(name, req, hc.longContextTokens)
		if !answerable || !hit {
			continue
		}
		structural = true
		if !seen[name] {
			seen[name] = true
			sig.RequiredCapabilities = append(sig.RequiredCapabilities, name)
		}
	}
	if structural {
		sig.Confidence = 1.0
		sig.AxisConfidence = map[string]float64{AxisCapabilities: 1.0}
	}
}

// fillAxis writes one value onto whichever Signals field this instance fills.
// An empty value leaves every axis empty, which is what a zero-hit heuristic
// reports and what a matcher's escape-equivalent would mean.
func (hc *HeuristicClassifier) fillAxis(sig *types.Signals, value string) {
	switch hc.axis {
	case AxisEffort:
		sig.Effort = value
	case AxisCostClass:
		sig.CostClass = value
	case AxisCapabilities:
		if value != "" {
			sig.RequiredCapabilities = []string{value}
		}
	default:
		sig.Domain = value
	}
}

// capabilityPredicates maps a capability name to the request-shape test that
// proves it, for the `detect:` block on a capability_detector classifier.
//
// These are exact: whether a request carries tools or an attachment is a fact
// about the request, not an inference from its words. So a structural hit is
// certain (confidence 1.0) and does not depend on the operator guessing which
// keyword a client will use. That matters more than it looks: the previous
// keyword-only detection matched on the shape of the TEXT ("image",
// "screenshot"), which is a guess about bytes that are actually present and
// countable.
//
// long_context is the one that needs a threshold, because "long" is a policy
// choice rather than a fact — hence `long_context_tokens:`.
func capabilityPredicate(name string, req *types.NormalizedRequest, longContextTokens int) (bool, bool) {
	switch name {
	case types.CapToolUse:
		return len(req.Tools) > 0, true
	case types.CapAttachment:
		for _, m := range req.Messages {
			for _, b := range m.Content {
				if b.Type == "attachment" {
					return true, true
				}
			}
		}
		return false, true
	case types.CapLongContext:
		if longContextTokens <= 0 {
			// Configured without a threshold: matched on nothing, reported as
			// not-answerable so the caller can reject it at config load rather
			// than silently never firing.
			return false, false
		}
		return estimateTokens(req) >= longContextTokens, true
	default:
		return false, false
	}
}

// estimateTokens is a rough char/4 heuristic over all message text, good
// enough for routing decisions (e.g. "does this need long context") without
// pulling in a real tokenizer.
func estimateTokens(req *types.NormalizedRequest) int {
	chars := len(req.SystemPrompt)
	for _, m := range req.Messages {
		chars += len(types.ExtractText(m))
	}
	return chars / 4
}

// MergedClassifier runs multiple classifiers and merges their signals.
// Used to combine independently-configured classifier instances (e.g.
// "domain" and "capability", both HeuristicClassifiers with different
// keyword maps) into the single Signals value the router expects.
type MergedClassifier struct {
	name        string
	classifiers []Classifier
}

// NewMergedClassifier creates a classifier that merges multiple classifiers.
func NewMergedClassifier(name string, classifiers []Classifier) *MergedClassifier {
	return &MergedClassifier{
		name:        name,
		classifiers: classifiers,
	}
}

// axisScore is the confidence to compare one classifier's value for one axis
// against. A classifier that fills several axes from one call reports a
// confidence PER AXIS (AxisConfidence) — a single value would be a lie, and
// would let a high-confidence verdict on one axis win a different axis it
// barely considered. A classifier that fills one axis (every pre-existing type)
// reports no per-axis map at all and is scored on its overall Confidence, which
// is exactly the behaviour that shipped before AxisConfidence existed.
func axisScore(sig types.Signals, axis string) float64 {
	if sig.AxisConfidence != nil {
		if v, ok := sig.AxisConfidence[axis]; ok {
			return v
		}
	}
	return sig.Confidence
}

// mergeAxisValue applies one classifier's verdict for one scalar axis
// (Domain, Effort, CostClass) to the merge in progress. target points at the
// merged Signals field for this axis; axisConfidence records the score that
// won it, keyed by axis name.
//
// Three rules, checked in order:
//
//  1. A zero-or-absent value, or a zero confidence, fills nothing — "no
//     confidence" and "no signal" must look identical, or a classifier that
//     is completely unsure would overwrite a better answer (see
//     TestMergedClassifierZeroConfidenceFillsNoAxis).
//  2. A real value (anything but the types.UnmatchedValue escape sentinel)
//     always beats an axis currently holding UnmatchedValue, REGARDLESS of
//     confidence — an escape verdict is "nothing fits", which must never
//     outrank an actual answer just because the escape call happened to be
//     more confident about having nothing to say. Among two real values the
//     usual per-axis highest-confidence-wins rule applies.
//  3. UnmatchedValue only fills the axis when it is currently completely
//     unset — a real value already recorded, however low its confidence, is
//     never displaced by "nothing fits".
func mergeAxisValue(target *string, axisConfidence map[string]float64, axis, value string, score float64) {
	if value == "" || score <= 0 {
		return
	}
	switch current := *target; current {
	case "":
		*target = value
		axisConfidence[axis] = score
	case types.UnmatchedValue:
		if value != types.UnmatchedValue {
			*target = value
			axisConfidence[axis] = score
		} else if score > axisConfidence[axis] {
			axisConfidence[axis] = score
		}
	default: // current already holds a real value
		if value != types.UnmatchedValue && score > axisConfidence[axis] {
			*target = value
			axisConfidence[axis] = score
		}
	}
}

// axisValue reads back the value the merge has accumulated so far for axis,
// from the same scalar fields mergeAxisValue writes. Capabilities has no
// scalar value to read (it's additive, see types.UnmatchedValue's doc) and is
// not reachable here in practice — gateAxes never names it — so it reports
// empty rather than assuming an unknown axis is unset.
func axisValue(sig *types.Signals, axis string) string {
	switch axis {
	case AxisEffort:
		return sig.Effort
	case AxisCostClass:
		return sig.CostClass
	case AxisDomain:
		return sig.Domain
	default:
		return ""
	}
}

// axesAllSet reports whether every named axis has been filled with a REAL
// value by an earlier classifier in this merge. An axis holding only the
// escape sentinel types.UnmatchedValue counts as still unset: "nothing fits"
// is not an answer that should make a later, more specific classifier skip
// its own attempt. merged holds the accumulated values; axisConfidence
// tracks which axes carry a recorded score (an unmatched verdict does record
// one, so this alone cannot distinguish the two cases — axisValue does). An
// empty axes list means "no axes gate this classifier" — treated as
// not-all-set so it still runs.
func axesAllSet(axes []string, merged *types.Signals, axisConfidence map[string]float64) bool {
	if len(axes) == 0 {
		return false
	}
	for _, axis := range axes {
		if _, ok := axisConfidence[axis]; !ok {
			return false
		}
		if axisValue(merged, axis) == types.UnmatchedValue {
			return false
		}
	}
	return true
}

// Classify merges signals from all classifiers. Each scalar axis (Domain,
// Effort, CostClass) is filled by whichever sub-classifier reported the
// highest confidence *for that axis* — keyed per-axis, not globally, so a
// high-confidence domain classifier can't starve a lower-confidence effort
// classifier out of populating Effort. RequiredCapabilities and
// EstimatedTokens are unioned/maxed since those are additive rather than
// exclusive facts about the request.
//
// A classifier whose matcher is `decisive` ends the merge when it hits: the
// classifiers after it do not run at all. That is the difference between an
// exact structural match and a guess — "this request IS a title generation" is
// not a candidate for keyword voting or a model call, and paying for a
// classification whose answer is already known is pure loss. Declared order is
// the priority order, which buildClassifiers already preserves.
func (mc *MergedClassifier) Classify(ctx context.Context, req *types.NormalizedRequest) (types.Signals, error) {
	var merged types.Signals
	axisConfidence := make(map[string]float64)
	capSeen := make(map[string]bool)
	sawAxisConfidence := false

	for _, c := range mc.classifiers {
		// A gated classifier (only_if_unset) is skipped entirely — no call,
		// no cost — when every axis it fills is already set by an earlier
		// classifier. Checked before Classify so a skip leaves no trace: no
		// signal, no ClassifierCalls entry, no record that it almost ran.
		if g, ok := c.(onlyIfUnsetClassifier); ok && g.gated() {
			if axesAllSet(g.gateAxes(), &merged, axisConfidence) {
				continue
			}
		}

		sig, err := c.Classify(ctx, req)
		if err != nil {
			return types.Signals{}, err
		}
		if sig.AxisConfidence != nil {
			sawAxisConfidence = true
		}

		mergeAxisValue(&merged.Domain, axisConfidence, AxisDomain, sig.Domain, axisScore(sig, AxisDomain))
		mergeAxisValue(&merged.Effort, axisConfidence, AxisEffort, sig.Effort, axisScore(sig, AxisEffort))
		mergeAxisValue(&merged.CostClass, axisConfidence, AxisCostClass, sig.CostClass, axisScore(sig, AxisCostClass))
		for _, capability := range sig.RequiredCapabilities {
			if !capSeen[capability] {
				capSeen[capability] = true
				merged.RequiredCapabilities = append(merged.RequiredCapabilities, capability)
			}
		}
		// RequestKind is first-non-empty-wins rather than a confidence
		// contest: a signature that identified the request outright is a
		// fact, not a guess to be outbid. Declared order is priority order
		// (buildClassifiers preserves it), so the first classifier that
		// recognizes the request names it. Recorded before the decisive
		// break below, like every other signal from this classifier.
		if merged.RequestKind == "" && sig.RequestKind != "" {
			merged.RequestKind = sig.RequestKind
		}
		if sig.EstimatedTokens > merged.EstimatedTokens {
			merged.EstimatedTokens = sig.EstimatedTokens
		}
		if sig.Confidence > merged.Confidence {
			merged.Confidence = sig.Confidence
		}
		// Additive, like RequiredCapabilities: every sub-classifier that made
		// its own upstream call (an LLMClassifier) is worth recording, not
		// just the one whose axis ends up winning.
		merged.ClassifierCalls = append(merged.ClassifierCalls, sig.ClassifierCalls...)

		// A decisive matcher HIT ends the merge: whatever this classifier
		// concluded IS the answer, so the classifiers declared after it are
		// not consulted. Checked last within the iteration so this
		// classifier's own signal is folded in before the loop stops.
		//
		// The check is on the hit, not on the configuration. A decisive
		// matcher that did not match must fall through to the classifiers
		// behind it exactly like any other miss, or one title-gen rule would
		// switch off classification for every request that is not a title
		// generation.
		if d, ok := c.(decisiveMatcher); ok && d.DecisiveMatch(req) {
			break
		}
	}

	// The per-axis scores that decided each axis are carried forward, so a
	// downstream reader (the store's confidence column, the routing rationale)
	// can see how sure each axis's winner was rather than only the single
	// highest value.
	//
	// Only when some classifier actually reported per-axis values. A merge of
	// one-axis classifiers is scored on their overall Confidence and leaves
	// this nil, so nothing that existed before AxisConfidence changes what it
	// reports — the field is additive in behaviour, not just in shape.
	if sawAxisConfidence && len(axisConfidence) > 0 {
		merged.AxisConfidence = axisConfidence
	}
	return merged, nil
}
