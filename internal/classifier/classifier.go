package classifier

import (
	"context"

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

// HeuristicClassifier uses keyword matching to classify requests.
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

// Classify performs keyword-based classification.
func (hc *HeuristicClassifier) Classify(ctx context.Context, req *types.NormalizedRequest) (types.Signals, error) {
	// TODO: implement keyword matching
	return types.Signals{Confidence: 0.5}, nil
}

// MergedClassifier runs multiple classifiers and merges their signals.
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

// Classify merges signals from all classifiers.
func (mc *MergedClassifier) Classify(ctx context.Context, req *types.NormalizedRequest) (types.Signals, error) {
	// TODO: implement signal merging
	return types.Signals{Confidence: 0.5}, nil
}
