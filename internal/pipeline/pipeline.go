// Package pipeline wires together normalization, classification, routing,
// guardrails and the upstream call into the single request lifecycle every
// inbound request goes through.
//
// The lifecycle is split across files by stage, in the order a request moves
// through them:
//
//	pipeline.go   the Pipeline type, its construction, config, and per-request
//	              header plumbing (WithHeaders/headersFromContext)
//	execute.go    Execute — the top-level lifecycle: normalize, guardrails,
//	              classify, route, then hand off to upstream or executeStream
//	routing.go    resolveRoute and the literal/alias/pin route resolution
//	upstream.go   tryUpstream, the cooldown/fallback policy, and the error
//	              translation that turns an upstream failure into an HTTP status
//	streaming.go  executeStream and the reassembly of streamed tool calls
//	record.go     everything that writes to the event store: request events,
//	              classifier-call events, captured content, cost and tool names
//	cooldown.go   the cooldown store tryUpstream consults
//	affinity.go   the session→provider pin store resolveRoute consults
//	session.go    deriving a session key from a request when none was hinted
package pipeline

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/cnf/arbiter/internal/classifier"
	"github.com/cnf/arbiter/internal/guardrail"
	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/router"
	"github.com/cnf/arbiter/internal/store"
	"github.com/cnf/arbiter/internal/translator"
	"github.com/cnf/arbiter/internal/upstream"
	"github.com/cnf/arbiter/pkg/types"
)

// defaultCooldown applies when a 429 arrives without a usable Retry-After
// header — enough to avoid hammering a provider that just said "slow down",
// without locking it out for long.
const defaultCooldown = 5 * time.Second

// Pinner persists session-affinity pins. The pipeline depends on this narrow
// slice rather than the store's wider surface, so the store stays free to grow
// and tests can substitute a fake — the same shape router.CostLatencyLookup
// establishes.
//
// AffinityPinRecord is the persistence shape, kept separate from the pipeline's
// internal affinityPin so the stored format cannot drift silently when the
// internal struct changes.
type Pinner interface {
	SavePin(ctx context.Context, p AffinityPinRecord) error
	LoadPin(ctx context.Context, sessionKey, promptHash string) (AffinityPinRecord, bool, error)
	DeletePin(ctx context.Context, sessionKey, promptHash string) error
}

// AffinityPinRecord is one prompt family's pin, as persisted. PromptHash
// separates families sharing one SessionKey (main thread vs. a title call vs.
// a subagent run) — see affinityKey and schema.sql's affinity_pins comment.
type AffinityPinRecord struct {
	SessionKey     string
	PromptHash     string
	RequestedModel string
	Provider       string
	Model          string
	ExpiresAt      time.Time
}

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

	// cooldowns is injected rather than owned, so it survives a reload — see
	// cooldownStore. A map living here was thrown away on every config save,
	// which re-opened a provider's flood the moment an unrelated setting changed.
	cooldowns *CooldownStore

	affinity        *affinityStore
	defaultCacheTTL time.Duration // used when a served provider sets no cache_ttl override

	// pinner persists affinity pins so they outlive this Pipeline. A reload
	// rebuilds the whole pipeline, so without persistence every live
	// conversation lost its pin on any config save — silently re-routing it and
	// breaking its prompt cache. Nil means pins stay in memory only (no store
	// configured), which is the correct degradation.
	pinner Pinner

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

	// configEpoch identifies the resolved config that built this pipeline. It
	// is stamped on every recorded event so spend can later be compared across
	// config changes. Empty when unknown (tests building a bare pipeline).
	configEpoch string

	// captureContent turns on request/response body capture (storage.
	// capture_content). Off unless the operator asks for it: this is the only
	// path that writes conversation text to disk.
	captureContent bool

	// noPin is session_affinity.no_pin as a set: request_kind values that
	// must never be pinned. See SetNoPin.
	noPin map[string]bool
}

// defaultAffinityTTL applies when config sets no session_affinity.default_ttl.
//
// Sized to a working session, not to a cache window: the pin exists so a
// conversation keeps hitting the provider that holds its warm prompt cache, and
// a short idle timeout expires it whenever the user pauses to read, think or run
// a build — after which the next turn re-routes to a cold provider and pays full
// price. 25 hours covers a long agentic run plus an overnight gap, so a
// conversation resumed the next morning is still pinned. Affinity state is a
// small in-memory map, so a generous TTL costs nothing to hold.
const defaultAffinityTTL = 25 * time.Hour

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
	pinner Pinner,
	cooldowns *CooldownStore,
) *Pipeline {
	if cooldowns == nil {
		// Tests and any caller that does not care about reload survival get a
		// working store rather than a nil dereference.
		cooldowns = NewCooldownStore()
	}
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
		cooldowns:       cooldowns,
		affinity:        newAffinityStore(pinner),
		pinner:          pinner,
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

// SetConfigEpoch records which resolved config built this pipeline, so every
// event it records can be attributed to that config. It is called once by the
// wiring code before the pipeline is published, not on the request path.
func (p *Pipeline) SetConfigEpoch(epoch string) {
	p.configEpoch = epoch
}

// SetCaptureContent turns request/response body capture on or off. Like
// SetConfigEpoch, it is a setter rather than a constructor parameter: capture
// is wiring-time policy, and a 17th positional argument would churn every
// NewPipeline call site in the tests for something no test currently varies.
// Called once by the wiring code before the pipeline is published.
func (p *Pipeline) SetCaptureContent(on bool) {
	p.captureContent = on
}

// SetNoPin sets session_affinity.no_pin: request_kind values that must never
// be pinned. Config decides, the classifier only ever labels (see #69) — this
// is where that policy actually lives. A request whose sig.RequestKind is in
// this set still classifies and routes normally; it simply never writes or
// reads an affinity_pins row. Nil/empty (the default) pins every kind,
// unchanged from before this existed. Like SetConfigEpoch/SetCaptureContent,
// a setter rather than a constructor parameter, called once by the wiring
// code before the pipeline is published.
func (p *Pipeline) SetNoPin(kinds []string) {
	if len(kinds) == 0 {
		p.noPin = nil
		return
	}
	set := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		set[k] = true
	}
	p.noPin = set
}

// pins reports whether a request with this RequestKind is eligible for
// affinity pinning under session_affinity.no_pin. Ordinary client traffic
// (an empty RequestKind) is always eligible — no_pin excludes a *kind*, and
// "no kind at all" is not a kind an operator could have named.
func (p *Pipeline) pins(requestKind string) bool {
	return requestKind == "" || !p.noPin[requestKind]
}

// headersContextKey is the context key WithHeaders/headersFromContext share.
// Headers ride on ctx rather than as an Execute parameter because they are
// metadata rather than routing input — attached to the stored row, like trace
// ID. They never influence routing, so they don't belong in the signature
// every call site (including ~30 in tests) must pass. One of them is now also
// relayed upstream rather than only recorded (`anthropic-beta`, read below),
// which does not change that: it is carried to the same upstream the route
// already chose, not used to choose it.
type headersContextKey struct{}

// WithHeaders returns a derived context carrying the client's inbound
// headers, already redacted by the caller (Arbiter's HTTP layer masks
// credential-shaped values before this is called — the pipeline doesn't know
// which header names are sensitive). Execute reads them back via
// headersFromContext, attaches them to the stored request row, and relays the
// one header that is per-request negotiation rather than observability.
func WithHeaders(ctx context.Context, headers map[string]string) context.Context {
	return context.WithValue(ctx, headersContextKey{}, headers)
}

func headersFromContext(ctx context.Context) map[string]string {
	h, _ := ctx.Value(headersContextKey{}).(map[string]string)
	return h
}

// headerValue reads a header out of a captured map, case-insensitively. HTTP
// header names are case-insensitive, and captureHeaders preserves whatever
// casing the client used (real clients send `anthropic-beta` lowercase, but a
// client is free not to), so an exact-key lookup would drop the value for
// some callers while working for others — the kind of difference that looks
// like the client's fault rather than the proxy's.
func headerValue(headers map[string]string, name string) string {
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// Execute runs the full pipeline: normalize -> pre-guardrails -> classify ->
// route -> upstream -> post-guardrails -> denormalize. format is the
