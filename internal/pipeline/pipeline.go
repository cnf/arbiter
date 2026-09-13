// Package pipeline wires together normalization, classification, routing,
// guardrails and the upstream call into the single request lifecycle every
// inbound request goes through.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
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

// defaultCooldown applies when a 429 arrives without a usable Retry-After
// header — enough to avoid hammering a provider that just said "slow down",
// without locking it out for long.
const defaultCooldown = 5 * time.Second

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

	fallbacks []string // ordered fallback provider names, tried on 429/5xx
	providers map[string]types.ProviderConfig

	cooldownMu sync.Mutex
	cooldowns  map[string]time.Time // provider -> earliest time it may be tried again

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
	providers map[string]types.ProviderConfig,
	fallbacks []string,
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
		providers:      providers,
		fallbacks:      fallbacks,
		cooldowns:      make(map[string]time.Time),
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

	// Streaming vs. non-streaming: different code paths
	if req.Stream {
		return p.executeStream(ctx, traceID, route, req)
	}

	resp, _, err := p.tryUpstream(ctx, route, req)
	if err != nil {
		p.logger.LogError(ctx, "error", err, map[string]interface{}{"provider": route.Provider})
		return nil, err
	}
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

// executeStream handles streaming requests. It returns a channel of normalized
// stream events that the HTTP handler will translate and send to the client.
func (p *Pipeline) executeStream(ctx context.Context, traceID string, route types.Route, req *types.NormalizedRequest) (interface{}, error) {
	// Send the request upstream (with fallback/retry handling) and get the
	// event channel. A 429/5xx fails SendStream synchronously — the HTTP
	// status is known before any SSE bytes flow — so fallback works exactly
	// as on the non-streaming path.
	_, eventChan, err := p.tryUpstream(ctx, route, req)
	if err != nil {
		p.logger.LogError(ctx, "error", err, map[string]interface{}{"provider": route.Provider})
		return nil, err
	}

	// Forward upstream events, stamping each with the trace ID — the
	// equivalent of resp.TraceID on the non-streaming path — and the model
	// actually serving the request (upstream-reported when the upstream says
	// so on message_start, the routed model otherwise). Also accumulates
	// usage from stream events and logs the upstream call once (with latency)
	// when the stream ends, mirroring the non-streaming path's LogUpstream.
	out := make(chan *types.NormalizedStreamEvent)
	go func() {
		defer close(out)
		start := time.Now()
		var usage types.Usage
		for evt := range eventChan {
			evt.TraceID = traceID
			if evt.MessageModel == "" {
				evt.MessageModel = route.Model
			}
			if evt.InputTokens > 0 {
				usage.InputTokens = evt.InputTokens
			}
			if evt.OutputTokens > 0 {
				usage.OutputTokens = evt.OutputTokens
			}
			out <- evt
		}
		p.logger.LogUpstream(ctx, route.Provider, 200, time.Since(start), usage)
	}()

	// The HTTP handler consumes this and flushes events as SSE.
	return &upstream.StreamResponse{EventChan: out}, nil
}

// upstreamAction is what to do after a failed upstream attempt.
type upstreamAction int

const (
	actionRetrySame      upstreamAction = iota // transient (5xx): try this provider again
	actionNextCandidate                        // move on to the next fallback provider
	actionFailFast                             // give up; a retry can't help
)

// tryUpstream sends the request to the routed provider — retrying it on 5xx
// up to that provider's retry_max — then tries the configured fallback
// providers in order. A 429 records a cooldown (from Retry-After, else
// defaultCooldown) during which the provider is skipped without being
// contacted; non-429 4xx errors fail fast since another provider won't fix
// a bad request. Exactly one of the response / event-channel results is
// non-nil on success, per req.Stream. Each attempt — success or failure —
// is logged via LogUpstream (for streams, success is logged when the stream
// ends, where usage is known).
func (p *Pipeline) tryUpstream(ctx context.Context, route types.Route, req *types.NormalizedRequest) (*types.NormalizedResponse, <-chan *types.NormalizedStreamEvent, error) {
	candidates := append([]types.Route{route}, p.fallbackRoutes(route, req)...)

	var lastErr error
candidates:
	for _, cand := range candidates {
		if until, cooling := p.onCooldown(cand.Provider); cooling {
			p.logger.LogUpstreamCooldown(ctx, cand.Provider, until, 0, "skipped")
			continue
		}

		maxAttempts := 1
		if cand.Config.RetryMax > 0 {
			maxAttempts = cand.Config.RetryMax + 1
		}

		for attempt := 1; attempt <= maxAttempts; attempt++ {
			start := time.Now()
			if req.Stream {
				eventChan, err := p.upstream.SendStream(ctx, cand, req)
				if err == nil {
					return nil, eventChan, nil
				}
				lastErr = err
				switch p.classifyUpstreamError(ctx, cand, err, time.Since(start)) {
				case actionRetrySame:
					continue
				case actionNextCandidate:
					continue candidates
				case actionFailFast:
					return nil, nil, lastErr
				}
			}

			resp, err := p.upstream.Send(ctx, cand, req)
			if err == nil {
				p.logger.LogUpstream(ctx, cand.Provider, http.StatusOK, time.Since(start), resp.Usage)
				return resp, nil, nil
			}
			lastErr = err
			switch p.classifyUpstreamError(ctx, cand, err, time.Since(start)) {
			case actionRetrySame:
				continue
			case actionNextCandidate:
				continue candidates
			case actionFailFast:
				return nil, nil, lastErr
			}
		}
	}

	if lastErr == nil {
		// Every candidate was skipped as cooling down; surface that as a 429.
		lastErr = arbitererrors.NewUpstreamError(route.Provider, http.StatusTooManyRequests, "all candidate providers are in cooldown", nil)
	}
	return nil, nil, lastErr
}

// classifyUpstreamError logs a failed upstream attempt and decides what to
// do next: retry the same provider (5xx — transient), try the next fallback
// (429 — the cooldown is recorded here), or fail fast (other 4xx — a
// different provider won't fix a bad request; non-HTTP failures are
// Arbiter's own and aren't retryable either).
func (p *Pipeline) classifyUpstreamError(ctx context.Context, cand types.Route, err error, latency time.Duration) upstreamAction {
	var ue *arbitererrors.UpstreamError
	if !errors.As(err, &ue) {
		p.logger.LogUpstream(ctx, cand.Provider, 0, latency, types.Usage{})
		return actionFailFast
	}
	p.logger.LogUpstream(ctx, ue.Provider, ue.StatusCode, latency, types.Usage{})

	switch {
	case ue.StatusCode == http.StatusTooManyRequests:
		cooldown := ue.RetryAfter
		if cooldown <= 0 {
			cooldown = defaultCooldown
		}
		until := time.Now().Add(cooldown)
		p.markCooldown(ue.Provider, until)
		p.logger.LogUpstreamCooldown(ctx, ue.Provider, until, ue.RetryAfter, "recorded")
		return actionNextCandidate
	case ue.StatusCode >= 500:
		return actionRetrySame
	default:
		return actionFailFast
	}
}

// fallbackRoutes builds candidate routes for the configured fallback
// providers, in order. A fallback gets the provider's own first model — a
// model name from the failed provider is meaningless elsewhere — falling
// back to req.Model only when the provider lists no models. The routed
// provider itself is skipped if it appears in the fallback list (it already
// had its attempts).
func (p *Pipeline) fallbackRoutes(failed types.Route, req *types.NormalizedRequest) []types.Route {
	routes := make([]types.Route, 0, len(p.fallbacks))
	for _, name := range p.fallbacks {
		if name == failed.Provider {
			continue
		}
		cfg, ok := p.providers[name]
		if !ok {
			continue // rejected at config load; defensive
		}
		model := ""
		if len(cfg.Models) > 0 {
			model = cfg.Models[0]
		}
		if model == "" {
			model = req.Model
		}
		routes = append(routes, types.Route{
			Provider:  name,
			Model:     model,
			Config:    cfg,
			Rationale: fmt.Sprintf("fallback after provider %q failed (%s)", failed.Provider, failed.Rationale),
		})
	}
	return routes
}

// onCooldown reports whether provider is in its 429 cooldown window.
func (p *Pipeline) onCooldown(provider string) (time.Time, bool) {
	p.cooldownMu.Lock()
	defer p.cooldownMu.Unlock()
	until, ok := p.cooldowns[provider]
	if !ok || time.Now().After(until) {
		return time.Time{}, false
	}
	return until, true
}

// markCooldown extends a provider's cooldown to `until` (never shortens an
// existing one — a second 429 with a longer Retry-After extends, a shorter
// one doesn't cut the current cooldown).
func (p *Pipeline) markCooldown(provider string, until time.Time) {
	p.cooldownMu.Lock()
	defer p.cooldownMu.Unlock()
	if until.After(p.cooldowns[provider]) {
		p.cooldowns[provider] = until
	}
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
