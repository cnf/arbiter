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

// HeuristicClassifier uses keyword matching against the last user message
// to guess intent. It's deliberately dumb — a starting point, not a final
// answer. The domain/capability split in lanes.yaml is handled by running
// two instances of this same type with different keyword maps and merging
// via MergedClassifier, rather than baking "domain" vs "capability" into
// the type itself.
type HeuristicClassifier struct {
	name     string
	keywords map[string][]string // intent -> keywords
}

// NewHeuristicClassifier creates a heuristic classifier.
func NewHeuristicClassifier(name string, keywords map[string][]string) *HeuristicClassifier {
	return &HeuristicClassifier{
		name:     name,
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

	var bestIntent string
	var bestHits int
	var capabilities []string

	for intent, kws := range hc.keywords {
		hits := 0
		for _, kw := range kws {
			if strings.Contains(text, strings.ToLower(kw)) {
				hits++
			}
		}
		if hits == 0 {
			continue
		}
		// Any keyword group can also double as a capability signal (e.g. a
		// "capability" classifier instance configured with vision/tool_use
		// groups) — surface every group that matched, not just the winner.
		capabilities = append(capabilities, intent)
		if hits > bestHits {
			bestIntent = intent
			bestHits = hits
		}
	}

	confidence := 0.0
	if bestHits > 0 {
		confidence = float64(bestHits) / float64(bestHits+1)
	}

	return types.Signals{
		Intent:               bestIntent,
		RequiredCapabilities: capabilities,
		EstimatedTokens:      estimateTokens(req),
		Confidence:           confidence,
	}, nil
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

// Classify merges signals from all classifiers. Intent/CostSensitivity come
// from whichever sub-classifier reports the highest confidence (first one
// wins ties); RequiredCapabilities and EstimatedTokens are unioned/maxed
// since those are additive rather than exclusive facts about the request.
func (mc *MergedClassifier) Classify(ctx context.Context, req *types.NormalizedRequest) (types.Signals, error) {
	var merged types.Signals
	capSeen := make(map[string]bool)

	for _, c := range mc.classifiers {
		sig, err := c.Classify(ctx, req)
		if err != nil {
			return types.Signals{}, err
		}

		if sig.Confidence > merged.Confidence {
			merged.Confidence = sig.Confidence
			if sig.Intent != "" {
				merged.Intent = sig.Intent
			}
			if sig.CostSensitivity != "" {
				merged.CostSensitivity = sig.CostSensitivity
			}
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
	}

	return merged, nil
}
