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
	AxisDomain       = "domain"
	AxisEffort       = "effort"
	AxisCapabilities = "capabilities"
	AxisCostClass    = "cost_class"
)

// HeuristicClassifier uses keyword matching against the last user message
// to guess one axis. It's deliberately dumb — a starting point, not a final
// answer. The multi-axis split in arbiter.yaml is handled by running several
// instances of this same type with different keyword maps and axis settings,
// merged via MergedClassifier, rather than baking each axis into the type.
type HeuristicClassifier struct {
	name     string
	axis     string              // which Signals field this instance fills
	keywords map[string][]string // axis value -> keywords
}

// NewHeuristicClassifier creates a heuristic classifier filling axis. An
// empty axis means AxisDomain, which is what every pre-existing config
// (written before axes were declared) means.
func NewHeuristicClassifier(name, axis string, keywords map[string][]string) *HeuristicClassifier {
	if axis == "" {
		axis = AxisDomain
	}
	return &HeuristicClassifier{
		name:     name,
		axis:     axis,
		keywords: keywords,
	}
}

// Classify performs keyword-based classification against the last user
// message. The intent with the most keyword hits wins; ties go to whichever
// intent was declared first in config. Confidence is hits / (hits + 1), a
// cheap curve that approaches 1.0 as evidence piles up but never reaches it
// (heuristics are never fully certain) and is exactly 0 for zero hits.
func (hc *HeuristicClassifier) Classify(ctx context.Context, req *types.NormalizedRequest) (types.Signals, error) {
	text := strings.ToLower(types.LastUserText(req))

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
	switch hc.axis {
	case AxisEffort:
		sig.Effort = bestValue
	case AxisCostClass:
		sig.CostClass = bestValue
	case AxisCapabilities:
		// Every matched group is a capability, not just the winner — a
		// request can need vision and tool_use at once.
		sig.RequiredCapabilities = matched
	default:
		sig.Domain = bestValue
	}
	return sig, nil
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

// Classify merges signals from all classifiers. Each scalar axis (Domain,
// Effort, CostClass) is filled by whichever sub-classifier reported the
// highest confidence *for that axis* — keyed per-axis, not globally, so a
// high-confidence domain classifier can't starve a lower-confidence effort
// classifier out of populating Effort. RequiredCapabilities and
// EstimatedTokens are unioned/maxed since those are additive rather than
// exclusive facts about the request.
func (mc *MergedClassifier) Classify(ctx context.Context, req *types.NormalizedRequest) (types.Signals, error) {
	var merged types.Signals
	axisConfidence := make(map[string]float64)
	capSeen := make(map[string]bool)

	for _, c := range mc.classifiers {
		sig, err := c.Classify(ctx, req)
		if err != nil {
			return types.Signals{}, err
		}

		if sig.Domain != "" && sig.Confidence > axisConfidence[AxisDomain] {
			axisConfidence[AxisDomain] = sig.Confidence
			merged.Domain = sig.Domain
		}
		if sig.Effort != "" && sig.Confidence > axisConfidence[AxisEffort] {
			axisConfidence[AxisEffort] = sig.Confidence
			merged.Effort = sig.Effort
		}
		if sig.CostClass != "" && sig.Confidence > axisConfidence[AxisCostClass] {
			axisConfidence[AxisCostClass] = sig.Confidence
			merged.CostClass = sig.CostClass
		}
		for _, capability := range sig.RequiredCapabilities {
			if !capSeen[capability] {
				capSeen[capability] = true
				merged.RequiredCapabilities = append(merged.RequiredCapabilities, capability)
			}
		}
		if sig.EstimatedTokens > merged.EstimatedTokens {
			merged.EstimatedTokens = sig.EstimatedTokens
		}
		if sig.Confidence > merged.Confidence {
			merged.Confidence = sig.Confidence
		}
	}

	return merged, nil
}
