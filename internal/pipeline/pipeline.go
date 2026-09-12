package pipeline

import (
	"context"

	"github.com/cnf/arbiter/internal/classifier"
	"github.com/cnf/arbiter/internal/guardrail"
	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/router"
	"github.com/cnf/arbiter/internal/translator"
	"github.com/cnf/arbiter/pkg/types"
)

// Pipeline orchestrates the full request lifecycle.
type Pipeline struct {
	translator   translator.Translator
	normalizer   translator.Normalizer
	denormalizer translator.Denormalizer

	classifiers []classifier.Classifier
	router      router.Router

	preGuardrails  []guardrail.Guardrail
	postGuardrails []guardrail.Guardrail

	logger logging.Logger
}

// NewPipeline creates a new pipeline.
func NewPipeline(
	t translator.Translator,
	n translator.Normalizer,
	d translator.Denormalizer,
	classifiers []classifier.Classifier,
	r router.Router,
	preG, postG []guardrail.Guardrail,
	l logging.Logger,
) *Pipeline {
	return &Pipeline{
		translator:     t,
		normalizer:     n,
		denormalizer:   d,
		classifiers:    classifiers,
		router:         r,
		preGuardrails:  preG,
		postGuardrails: postG,
		logger:         l,
	}
}

// Execute runs the full pipeline: normalize → classify → route → guard → upstream → guard → denormalize.
func (p *Pipeline) Execute(ctx context.Context, payload []byte) (interface{}, error) {
	// TODO: implement full pipeline orchestration
	return nil, nil
}
