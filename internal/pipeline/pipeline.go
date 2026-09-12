// Package pipeline wires together normalization, classification, routing,
// guardrails and the upstream call into the single request lifecycle every
// inbound request goes through.
package pipeline

import (
	"context"
	"time"

	"github.com/cnf/arbiter/internal/classifier"
	"github.com/cnf/arbiter/internal/guardrail"
	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/router"
	"github.com/cnf/arbiter/internal/translator"
	"github.com/cnf/arbiter/internal/upstream"
	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// Pipeline orchestrates the full request lifecycle.
type Pipeline struct {
	translator   translator.Translator
	normalizer   translator.Normalizer
	denormalizer translator.Denormalizer

	classifiers []classifier.Classifier
	router      router.Router
	upstream    upstream.Client

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
	u upstream.Client,
	preG, postG []guardrail.Guardrail,
	l logging.Logger,
) *Pipeline {
	return &Pipeline{
		translator:     t,
		normalizer:     n,
		denormalizer:   d,
		classifiers:    classifiers,
		router:         r,
		upstream:       u,
		preGuardrails:  preG,
		postGuardrails: postG,
		logger:         l,
	}
}

// Execute runs the full pipeline: normalize -> pre-guardrails -> classify ->
// route -> upstream -> post-guardrails -> denormalize. format is the
// caller's already-known wire format ("anthropic" or "openai") — the HTTP
// layer knows this from which endpoint was hit, so Execute never needs to
// sniff it.
func (p *Pipeline) Execute(ctx context.Context, payload []byte, format string, traceID string) (interface{}, error) {
	ctx = p.logger.WithTraceID(ctx, traceID)

	req, err := p.normalizer.ToNormalized(payload, format)
	if err != nil {
		return nil, arbitererrors.NewTranslationError("pre_routing", "normalize request", err)
	}
	req.TraceID = traceID

	for _, g := range p.preGuardrails {
		if !g.ShouldRun(req) {
			continue
		}
		mutated, err := g.ApplyPre(ctx, req)
		if err != nil {
			p.logger.LogGuardrail(ctx, g.Name(), "rejected", false)
			return nil, err
		}
		p.logger.LogGuardrail(ctx, g.Name(), "applied", true)
		req = mutated
	}

	sig, err := p.classify(ctx, req)
	if err != nil {
		return nil, arbitererrors.NewClassificationError("classify request", err)
	}

	routeStart := time.Now()
	route, _, err := p.router.Route(ctx, req, sig)
	if err != nil {
		return nil, arbitererrors.NewRoutingError("route request", err)
	}
	p.logger.LogRouting(ctx, route, sig, time.Since(routeStart))

	upstreamStart := time.Now()
	resp, err := p.upstream.Send(ctx, route, req)
	if err != nil {
		p.logger.LogError(ctx, "error", err, map[string]interface{}{"provider": route.Provider})
		return nil, err
	}
	statusCode := 200
	p.logger.LogUpstream(ctx, route.Provider, statusCode, time.Since(upstreamStart), resp.Usage)
	resp.TraceID = traceID
	resp.RoutingDecision = route.Rationale

	for _, g := range p.postGuardrails {
		mutated, err := g.ApplyPost(ctx, resp, route)
		if err != nil {
			p.logger.LogGuardrail(ctx, g.Name(), "rejected", false)
			return nil, err
		}
		p.logger.LogGuardrail(ctx, g.Name(), "applied", true)
		resp = mutated
	}

	out, err := p.denormalizer.FromNormalized(resp, format)
	if err != nil {
		return nil, arbitererrors.NewTranslationError("post_routing", "denormalize response", err)
	}
	return out, nil
}

// classify runs all configured classifiers and merges their signals. With
// zero classifiers configured, it returns a zero-value Signals rather than
// erroring — a router that doesn't need signals (like SimpleRouter) works
// fine without any classification stage at all.
func (p *Pipeline) classify(ctx context.Context, req *types.NormalizedRequest) (types.Signals, error) {
	if len(p.classifiers) == 0 {
		return types.Signals{}, nil
	}
	merged := classifier.NewMergedClassifier("pipeline", p.classifiers)
	return merged.Classify(ctx, req)
}
