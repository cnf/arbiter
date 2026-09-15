// Package pipeline wires together normalization, classification, routing,
// guardrails and the upstream call into the single request lifecycle every
// inbound request goes through.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/cnf/arbiter/internal/classifier"
	"github.com/cnf/arbiter/internal/guardrail"
	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/router"
	"github.com/cnf/arbiter/internal/store"
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

	affinity        *affinityStore
	defaultCacheTTL time.Duration // used when a served provider sets no cache_ttl override

	// aliasResolver resolves force-alias overrides named by req.Model. Nil
	// when no aliases are configured — resolveRoute skips the force step
	// entirely rather than calling into a nil resolver.
	aliasResolver *router.AliasResolver

	// literalModels maps a declared model name to the provider that declares
	// it, so an explicit req.Model can skip classify+rules. Built once here
	// (deterministically, providers in sorted order) rather than scanned per
	// request.
	literalModels map[string]string

	logger logging.Logger

	// store records every completed request. NoopWriter when no store is
	// configured, so tests and local dev without a DB file work unchanged.
	store store.Writer

	// costCatalog answers cost lookups for computing cost_usd when the
	// upstream didn't report one. nil means no catalog configured — cost
	// then stays 0 for those requests.
	costCatalog router.CostLatencyLookup
}

// defaultAffinityTTL applies when config sets no session_affinity.default_ttl.
const defaultAffinityTTL = 5 * time.Minute

// NewPipeline creates a new pipeline. cacheTTL is the default idle TTL for
// session affinity pins (config's session_affinity.default_ttl, or
// defaultAffinityTTL if unset/zero); individual providers may override it
// via ProviderConfig.CacheTTL.
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
	cacheTTL time.Duration,
	aliasResolver *router.AliasResolver,
	writer store.Writer,
	costCatalog router.CostLatencyLookup,
) *Pipeline {
	if writer == nil {
		writer = store.NoopWriter{}
	}
	if cacheTTL <= 0 {
		cacheTTL = defaultAffinityTTL
	}

	// A model declared by two providers resolves to the alphabetically first
	// one, so the choice is stable across restarts rather than dependent on
	// map iteration order.
	literalModels := make(map[string]string)
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		for _, m := range providers[name].Models {
			if _, seen := literalModels[m]; !seen {
				literalModels[m] = name
			}
		}
	}

	return &Pipeline{
		translator:      t,
		normalizer:      n,
		denormalizer:    d,
		classifiers:     classifiers,
		router:          r,
		upstream:        u,
		providers:       providers,
		fallbacks:       fallbacks,
		cooldowns:       make(map[string]time.Time),
		affinity:        newAffinityStore(),
		defaultCacheTTL: cacheTTL,
		preGuardrails:   preG,
		postGuardrails:  postG,
		aliasResolver:   aliasResolver,
		literalModels:   literalModels,
		logger:          l,
		store:           writer,
		costCatalog:     costCatalog,
	}
}

// Execute runs the full pipeline: normalize -> pre-guardrails -> classify ->
// route -> upstream -> post-guardrails -> denormalize. format is the
// caller's already-known wire format ("anthropic" or "openai") — the HTTP
// layer knows this from which endpoint was hit, so Execute never needs to
// sniff it. sessionHint is an inbound session identifier (e.g. an
// X-Session-Id header), used to pin this conversation to whichever
// provider/model actually serves it — see resolveRoute.
func (p *Pipeline) Execute(ctx context.Context, payload []byte, format string, traceID string, sessionHint string) (interface{}, error) {
	ctx = p.logger.WithTraceID(ctx, traceID)
	start := time.Now()

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

	// Session key is derived after pre-guardrails, not before: a guardrail
	// like system_prompt can rewrite req.SystemPrompt, and the key must hash
	// the final content that actually goes out — otherwise the same
	// conversation could hash differently across requests.
	sessionKey, hasKey := SessionKey(sessionHint, req)
	req.SessionKey = sessionKey

	route, sig, err := p.resolveRoute(ctx, req, hasKey)
	if err != nil {
		return nil, err
	}

	// Streaming vs. non-streaming: different code paths
	if req.Stream {
		return p.executeStream(ctx, traceID, route, req, sessionKey, hasKey, sig, start)
	}

	resp, _, _, served, err := p.tryUpstream(ctx, route, req)
	if err != nil {
		p.logger.LogError(ctx, "error", err, map[string]interface{}{"provider": route.Provider})
		status, provider := upstreamFailure(err)
		if provider == "" {
			provider = route.Provider
		}
		p.record(store.Event{
			TraceID:          traceID,
			SessionKey:       sessionKey,
			Format:           format,
			Provider:         provider,
			Model:            req.Model,
			AliasUsed:        p.aliasName(req.Model),
			RoutingRationale: route.Rationale,
			Domain:           sig.Domain,
			Effort:           sig.Effort,
			CostClass:        sig.CostClass,
			Confidence:       sig.Confidence,
			LatencyMs:        time.Since(start).Milliseconds(),
			StatusCode:       status,
			Error:            err.Error(),
		})
		return nil, err
	}
	if hasKey {
		p.affinity.pin(sessionKey, req.Model, served.Provider, served.Model, p.cacheTTLFor(served.Provider))
	}
	resp.TraceID = traceID
	resp.RoutingDecision = served.Rationale

	for _, g := range p.postGuardrails {
		mutated, err := g.ApplyPost(ctx, resp, served)
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

	usage := resp.Usage
	usage.CostUSD = p.computeCost(served.Provider, served.Model, usage)
	p.record(store.Event{
		TraceID:          traceID,
		SessionKey:       sessionKey,
		Format:           format,
		Provider:         served.Provider,
		Model:            served.Model,
		AliasUsed:        p.aliasName(req.Model),
		RoutingRationale: served.Rationale,
		Domain:           sig.Domain,
		Effort:           sig.Effort,
		CostClass:        sig.CostClass,
		Confidence:       sig.Confidence,
		Usage:            usage,
		LatencyMs:        time.Since(start).Milliseconds(),
		StatusCode:       http.StatusOK,
		ToolCalls:        toolCallNames(resp.Content),
	})
	return out, nil
}

// record enqueues a completed request for the event store. It never blocks
// the request path: a configured store owns the enqueue policy (drop with a
// warning when saturated), and the default NoopWriter discards outright.
func (p *Pipeline) record(ev store.Event) {
	p.store.Record(ev)
}

// aliasName reports the alias the client named, if req.Model resolves to one.
// Empty when the client named a literal model or nothing at all.
func (p *Pipeline) aliasName(model string) string {
	if p.aliasResolver != nil && p.aliasResolver.Has(model) {
		return model
	}
	return ""
}

// toolCallNames lists the tool names a response invoked, preserving order.
func toolCallNames(blocks []types.ContentBlock) []string {
	var names []string
	for _, b := range blocks {
		if b.Type == "tool_use" && b.ToolName != "" {
			names = append(names, b.ToolName)
		}
	}
	return names
}

// computeCost fills in cost from the static catalog when the upstream reported
// none (plain Anthropic/OpenAI report nothing; OpenRouter reports a real
// figure). A provider-reported cost is authoritative and left untouched. Cache
// tokens are not priced — the catalog carries no cache rates — so the result
// is a lower bound for providers that bill cache reads separately.
func (p *Pipeline) computeCost(provider, model string, usage types.Usage) float64 {
	if usage.CostUSD > 0 || p.costCatalog == nil {
		return usage.CostUSD
	}
	mc, ok := p.costCatalog.Lookup(provider, model)
	if !ok {
		return 0
	}
	const perMTok = 1_000_000.0
	return (float64(usage.InputTokens)*mc.InputCostPerMTok +
		float64(usage.OutputTokens)*mc.OutputCostPerMTok) / perMTok
}

// upstreamFailure extracts the status code and serving provider from a failed
// attempt. A non-UpstreamError (Arbiter's own translation/routing failure)
// has no upstream status, so it records 0.
func upstreamFailure(err error) (status int, provider string) {
	var ue *arbitererrors.UpstreamError
	if errors.As(err, &ue) {
		return ue.StatusCode, ue.Provider
	}
	return 0, ""
}

// resolveRoute decides which route to attempt, in precedence order:
//
//  1. An explicit concrete model — req.Model exactly matching a configured
//     provider's declared model — routes straight there. The client named a
//     real model, so this wins even over an existing affinity pin.
//  2. An affinity pin, for as long as the client keeps requesting the same
//     model. It applies when the request has a usable session key, req.Model
//     equals the model the pin was recorded under, and the pinned provider
//     isn't cooling down.
//  3. A client-named pinned/group alias, resolved directly (group members
//     become the route's fallback chain).
//  4. Otherwise a fresh classify+route: signals are classified, then a
//     force-alias named by req.Model overrides the axes it declares, then
//     the router matches.
//
// A client that explicitly requests a different model discards the pin and
// routes fresh — it means what it says. A pinned provider in cooldown is
// likewise treated as a miss rather than an error, since fresh routing is
// exactly what should happen when the pin's cache is already unusable.
//
// req.Model == "" is not special-cased: it can never match a recorded
// requested-model (pins are always recorded under a non-empty model), so an
// empty model simply never hits a pin.
func (p *Pipeline) resolveRoute(ctx context.Context, req *types.NormalizedRequest, hasKey bool) (types.Route, types.Signals, error) {
	if route, ok := p.literalModelRoute(req.Model); ok {
		p.logger.LogRouting(ctx, route, types.Signals{}, 0)
		return route, types.Signals{}, nil
	}

	if hasKey {
		if provider, model, ok := p.affinity.get(req.SessionKey, req.Model); ok {
			if _, cooling := p.onCooldown(provider); !cooling {
				if cfg, ok := p.providers[provider]; ok {
					route := types.Route{
						Provider:  provider,
						Model:     model,
						Config:    cfg,
						Rationale: "session affinity pin",
					}
					p.logger.LogRouting(ctx, route, types.Signals{}, 0)
					return route, types.Signals{}, nil
				}
			}
		}
	}

	// A client-named pinned/group alias selects a target directly, the same
	// way a rule target would. Force aliases resolve to nothing by design —
	// they only shape axes — so they fall through to classify+rules below.
	if route, ok := p.aliasRoute(req.Model); ok {
		p.logger.LogRouting(ctx, route, types.Signals{}, 0)
		return route, types.Signals{}, nil
	}

	sig, err := p.classify(ctx, req)
	if err != nil {
		return types.Route{}, types.Signals{}, arbitererrors.NewClassificationError("classify request", err)
	}
	sig = p.applyForceAlias(req, sig)

	routeStart := time.Now()
	route, _, err := p.router.Route(ctx, req, sig)
	if err != nil {
		return types.Route{}, types.Signals{}, arbitererrors.NewRoutingError("route request", err)
	}
	p.logger.LogRouting(ctx, route, sig, time.Since(routeStart))
	return route, sig, nil
}

// literalModelRoute returns a direct route when req.Model is a model actually
// declared by a configured provider.
func (p *Pipeline) literalModelRoute(model string) (types.Route, bool) {
	if model == "" {
		return types.Route{}, false
	}
	name, ok := p.literalModels[model]
	if !ok {
		return types.Route{}, false
	}
	return types.Route{
		Provider:  name,
		Model:     model,
		Config:    p.providers[name],
		Rationale: fmt.Sprintf("explicit model %q -> provider %q", model, name),
	}, true
}

// aliasRoute resolves a client-named pinned/group alias into a concrete
// route. Force aliases and non-alias names yield ok=false so the caller
// proceeds to classification and rule matching.
func (p *Pipeline) aliasRoute(name string) (types.Route, bool) {
	if p.aliasResolver == nil || name == "" {
		return types.Route{}, false
	}
	if _, isForce := p.aliasResolver.Force(name); isForce {
		return types.Route{}, false
	}
	provider, model, ok, err := p.aliasResolver.Resolve(name)
	if err != nil || !ok {
		// A malformed alias is a config error surfaced at load time; treat a
		// runtime resolution failure as "not an alias" rather than failing the
		// request here.
		return types.Route{}, false
	}
	cfg, ok := p.providers[provider]
	if !ok {
		return types.Route{}, false
	}
	if model == "" && len(cfg.Models) > 0 {
		model = cfg.Models[0]
	}
	route := types.Route{
		Provider:  provider,
		Model:     model,
		Config:    cfg,
		Rationale: fmt.Sprintf("client requested alias %q -> %s/%s", name, provider, model),
	}
	route.Fallbacks = p.aliasResolver.GroupFallbacks(name, router.AliasMember{Provider: provider, Model: model})
	return route, true
}

// applyForceAlias overrides the axes named by a force-alias, when req.Model
// names one. Only the axes the alias declares are overridden — an unforced
// axis keeps whatever the classifiers produced (e.g. "coding" forces domain
// but leaves effort to be classified normally). ok=false (req.Model isn't a
// force-alias, or no aliases are configured at all) returns sig unchanged.
func (p *Pipeline) applyForceAlias(req *types.NormalizedRequest, sig types.Signals) types.Signals {
	if p.aliasResolver == nil || req.Model == "" {
		return sig
	}
	force, ok := p.aliasResolver.Force(req.Model)
	if !ok {
		return sig
	}
	for axis, values := range force {
		if len(values) == 0 {
			continue
		}
		switch axis {
		case classifier.AxisDomain, "intent":
			sig.Domain = values[0]
		case classifier.AxisEffort:
			sig.Effort = values[0]
		case classifier.AxisCostClass, "cost_sensitivity":
			sig.CostClass = values[0]
		case classifier.AxisCapabilities:
			sig.RequiredCapabilities = values
		}
	}
	return sig
}

// cacheTTLFor returns the session affinity idle TTL to use for a pin served
// by provider: the provider's own CacheTTL override if set, else the
// pipeline's default.
func (p *Pipeline) cacheTTLFor(provider string) time.Duration {
	if cfg, ok := p.providers[provider]; ok && cfg.CacheTTL > 0 {
		return cfg.CacheTTL
	}
	return p.defaultCacheTTL
}

// executeStream handles streaming requests. It returns a channel of normalized
// stream events that the HTTP handler will translate and send to the client.
func (p *Pipeline) executeStream(ctx context.Context, traceID string, route types.Route, req *types.NormalizedRequest, sessionKey string, hasKey bool, sig types.Signals, start time.Time) (interface{}, error) {
	// Send the request upstream (with fallback/retry handling) and get the
	// event channel. A 429/5xx fails SendStream synchronously — the HTTP
	// status is known before any SSE bytes flow — so fallback works exactly
	// as on the non-streaming path.
	_, eventChan, errChan, served, err := p.tryUpstream(ctx, route, req)
	if err != nil {
		p.logger.LogError(ctx, "error", err, map[string]interface{}{"provider": route.Provider})
		status, provider := upstreamFailure(err)
		if provider == "" {
			provider = route.Provider
		}
		p.record(store.Event{
			TraceID:          traceID,
			SessionKey:       sessionKey,
			Format:           req.OriginalFormat,
			Provider:         provider,
			Model:            req.Model,
			AliasUsed:        p.aliasName(req.Model),
			RoutingRationale: route.Rationale,
			Domain:           sig.Domain,
			Effort:           sig.Effort,
			CostClass:        sig.CostClass,
			Confidence:       sig.Confidence,
			LatencyMs:        time.Since(start).Milliseconds(),
			StatusCode:       status,
			Error:            err.Error(),
			Stream:           true,
		})
		return nil, err
	}
	if hasKey {
		p.affinity.pin(sessionKey, req.Model, served.Provider, served.Model, p.cacheTTLFor(served.Provider))
	}

	// Forward upstream events, stamping each with the trace ID — the
	// equivalent of resp.TraceID on the non-streaming path — and the model
	// actually serving the request (upstream-reported when the upstream says
	// so on message_start, the routed model otherwise). Also accumulates
	// usage from stream events and logs the upstream call once (with latency)
	// when the stream ends, mirroring the non-streaming path's LogUpstream.
	//
	// The stream's terminal status is not assumed to be 200: errChan carries
	// the read goroutine's outcome, so a stream that dies mid-flight records
	// the failure rather than an optimistic success. Recording happens after
	// the event channel drains, where both usage and the terminal error are
	// known.
	out := make(chan *types.NormalizedStreamEvent)
	go func() {
		defer close(out)
		streamStart := time.Now()
		var usage types.Usage
		for evt := range eventChan {
			evt.TraceID = traceID
			if evt.MessageModel == "" {
				evt.MessageModel = served.Model
			}
			if evt.InputTokens > 0 {
				usage.InputTokens = evt.InputTokens
			}
			if evt.OutputTokens > 0 {
				usage.OutputTokens = evt.OutputTokens
			}
			out <- evt
		}

		// Drain-then-read: the contract SendStream documents. A nil error is
		// a clean finish; non-nil means the SSE read failed after the event
		// channel closed.
		streamErr := <-errChan
		status := http.StatusOK
		errMsg := ""
		if streamErr != nil {
			status = http.StatusBadGateway
			errMsg = streamErr.Error()
		}
		p.logger.LogUpstream(ctx, served.Provider, status, time.Since(streamStart), usage)

		usage.CostUSD = p.computeCost(served.Provider, served.Model, usage)
		p.record(store.Event{
			TraceID:          traceID,
			SessionKey:       sessionKey,
			Format:           req.OriginalFormat,
			Provider:         served.Provider,
			Model:            served.Model,
			AliasUsed:        p.aliasName(req.Model),
			RoutingRationale: served.Rationale,
			Domain:           sig.Domain,
			Effort:           sig.Effort,
			CostClass:        sig.CostClass,
			Confidence:       sig.Confidence,
			Usage:            usage,
			LatencyMs:        time.Since(start).Milliseconds(),
			StatusCode:       status,
			Error:            errMsg,
			Stream:           true,
		})
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
// ends, where usage is known). The returned Route is whichever candidate
// actually served the request (== route on the common path, a fallback
// candidate otherwise) — callers use it to record the session affinity pin
// against the outcome that actually happened, not the one that was attempted.
//
// For a stream, the second return is the event channel and the third is the
// terminal-outcome channel: nil once the event channel has been drained and
// the read finished cleanly, non-nil if the SSE read failed mid-stream. The
// non-streaming path returns nils for both channels.
func (p *Pipeline) tryUpstream(ctx context.Context, route types.Route, req *types.NormalizedRequest) (*types.NormalizedResponse, <-chan *types.NormalizedStreamEvent, <-chan error, types.Route, error) {
	// route.Fallbacks (a group alias's unselected members, if this route came
	// from one) are tried before the global routing.fallback_providers list —
	// they're a more specific, author-declared chain for this exact route.
	candidates := append([]types.Route{route}, route.Fallbacks...)
	candidates = append(candidates, p.fallbackRoutes(route, req)...)

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
				eventChan, errChan, err := p.upstream.SendStream(ctx, cand, req)
				if err == nil {
					return nil, eventChan, errChan, cand, nil
				}
				lastErr = err
				switch p.classifyUpstreamError(ctx, cand, err, time.Since(start)) {
				case actionRetrySame:
					continue
				case actionNextCandidate:
					continue candidates
				case actionFailFast:
					return nil, nil, nil, types.Route{}, lastErr
				}
			}

			resp, err := p.upstream.Send(ctx, cand, req)
			if err == nil {
				p.logger.LogUpstream(ctx, cand.Provider, http.StatusOK, time.Since(start), resp.Usage)
				return resp, nil, nil, cand, nil
			}
			lastErr = err
			switch p.classifyUpstreamError(ctx, cand, err, time.Since(start)) {
			case actionRetrySame:
				continue
			case actionNextCandidate:
				continue candidates
			case actionFailFast:
				return nil, nil, nil, types.Route{}, lastErr
			}
		}
	}

	if lastErr == nil {
		// Every candidate was skipped as cooling down; surface that as a 429.
		lastErr = arbitererrors.NewUpstreamError(route.Provider, http.StatusTooManyRequests, "all candidate providers are in cooldown", nil)
	}
	return nil, nil, nil, types.Route{}, lastErr
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
