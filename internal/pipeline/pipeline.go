// Package pipeline wires together normalization, classification, routing,
// guardrails and the upstream call into the single request lifecycle every
// inbound request goes through.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
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
	arbitererrors "github.com/cnf/arbiter/pkg/errors"
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
	LoadPin(ctx context.Context, sessionKey string) (AffinityPinRecord, bool, error)
	DeletePin(ctx context.Context, sessionKey string) error
}

// AffinityPinRecord is one session's pin as persisted.
type AffinityPinRecord struct {
	SessionKey     string
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
// caller's already-known wire format ("anthropic" or "openai") — the HTTP
// layer knows this from which endpoint was hit, so Execute never needs to
// sniff it. sessionHint is an inbound session identifier (e.g. an
// X-Session-Id header), used to pin this conversation to whichever
// provider/model actually serves it — see resolveRoute.
func (p *Pipeline) Execute(ctx context.Context, payload []byte, format string, traceID string, sessionHint string) (interface{}, error) {
	ctx = p.logger.WithTraceID(ctx, traceID)
	start := time.Now()
	headers := headersFromContext(ctx)

	req, err := p.normalizer.ToNormalized(payload, format)
	if err != nil {
		// The payload never even normalized, so there is no conversation to
		// capture — only the raw bytes, which is what OriginalPayload holds for
		// the formats that get that far. Nothing to store here.
		return nil, arbitererrors.NewTranslationError("pre_routing", "normalize request", err)
	}
	req.TraceID = traceID

	// The client's beta opt-ins ride along with the request, not with the
	// provider config: they are per-request negotiation, and an Anthropic
	// upstream must be asked for the same betas the client asked Arbiter for.
	// Interleaved thinking is gated behind one of these, so dropping the
	// header silently disables the feature in the middle of the chain.
	if headers != nil {
		req.ClientBeta = headerValue(headers, "anthropic-beta")
	}

	// Session key is derived BEFORE pre-guardrails, from the client's own text.
	// Deriving it after meant the key hashed whatever the system_prompt
	// guardrail had injected, so editing that guardrail's prompt changed every
	// key at once and invalidated every live pin — the same blast radius a
	// denoise change has. The client's own opening turn is stable across the
	// whole conversation either way, so there is nothing to gain by waiting.
	sessionKey, hasKey := SessionKey(sessionHint, req)
	req.SessionKey = sessionKey

	// The client's own system prompt, kept before any pre-guardrail can mutate
	// it. A `system_prompt` guardrail with override:false PREPENDS its text, and
	// a structural classifier matching req.SystemPrompt with mode prefix would
	// then be looking for a signature behind Arbiter's own preamble — a miss
	// that looks exactly like a wrong pattern. Capture is taken here for the
	// same reason (see just below), and the two must be taken together or they
	// can disagree about what "as sent" means.
	req.ClientSystemPrompt = req.SystemPrompt

	// Capture content BEFORE any pre-guardrail runs. A guardrail such as
	// system_prompt rewrites req.SystemPrompt, and capturing after it would
	// store Arbiter's own injected prompt as though the client had sent it —
	// destroying exactly the distinction the dedup queries exist to expose.
	var content store.CapturedContent
	if p.captureContent {
		content.Request = store.CaptureRequest(req)
	}

	for _, g := range p.preGuardrails {
		if !g.ShouldRun(req) {
			continue
		}
		mutated, err := g.ApplyPre(ctx, req)
		if err != nil {
			p.logger.LogGuardrail(ctx, g.Name(), "rejected", false)
			// A guardrail rejection is a real request the operator will want to
			// see ("why was this refused?"), and it never reaches the requests
			// table. Its content goes in under owner_kind="rejected".
			p.recordRejected(traceID, content)
			return nil, err
		}
		p.logger.LogGuardrail(ctx, g.Name(), "applied", true)
		req = mutated
	}

	route, sig, err := p.resolveRoute(ctx, req, hasKey)
	if err != nil {
		// Routing failed (no rule matched and no fallback router, or a config
		// the request can't be routed under). Same reasoning as a guardrail
		// rejection: the content is worth keeping even though no row will exist.
		p.recordRejected(traceID, content)
		return nil, err
	}

	// Streaming vs. non-streaming: different code paths
	if req.Stream {
		return p.executeStream(ctx, traceID, route, req, sessionKey, hasKey, sig, start, content)
	}

	resp, _, _, served, err := p.tryUpstream(ctx, route, req)
	if err != nil {
		p.logger.LogError(ctx, "error", err, map[string]interface{}{"provider": route.Provider})
		status, provider := upstreamFailureStatus(err), upstreamFailureProvider(err)
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
			RequestKind:      sig.RequestKind,
			Effort:           sig.Effort,
			CostClass:        sig.CostClass,
			Confidence:       sig.Confidence,
			LatencyMs:        time.Since(start).Milliseconds(),
			StatusCode:       status,
			Error:            err.Error(),
			Content:          contentOrNil(content),
			Headers:          headers,
		})
		return nil, err
	}
	if hasKey {
		p.affinity.pin(ctx, sessionKey, req.Model, served.Provider, served.Model, p.cacheTTLFor(served.Provider))
	}
	// Captured before any post-guardrail can touch resp: this is what the
	// upstream itself reported, which is what a meta-router alias (e.g.
	// OpenRouter's "openrouter/auto") exists to obscure from Arbiter's own
	// routing decision — served.Model is what Arbiter asked for, this is what
	// the upstream says it actually used.
	actualModel := ""
	if resp.Model != "" && resp.Model != served.Model {
		actualModel = resp.Model
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
	// The response is captured here, after post-guardrails and denormalization,
	// because this is the content that actually goes back to the client — a
	// post-guardrail that rewrites the response must be reflected in what is
	// stored, or the UI would show something the client never received.
	if p.captureContent {
		content.Response = store.CaptureResponse(resp, "assistant")
	}
	p.record(store.Event{
		TraceID:          traceID,
		SessionKey:       sessionKey,
		Format:           format,
		Provider:         served.Provider,
		Model:            served.Model,
		ActualModel:      actualModel,
		AliasUsed:        p.aliasName(req.Model),
		RoutingRationale: served.Rationale,
		Domain:           sig.Domain,
		RequestKind:      sig.RequestKind,
		Effort:           sig.Effort,
		CostClass:        sig.CostClass,
		Confidence:       sig.Confidence,
		Usage:            usage,
		Content:          contentOrNil(content),
		LatencyMs:        time.Since(start).Milliseconds(),
		StatusCode:       http.StatusOK,
		ToolCalls:        toolCallNames(resp.Content),
		Headers:          headers,
	})
	return out, nil
}

// record enqueues a completed request for the event store. It never blocks
// the request path: a configured store owns the enqueue policy (drop with a
// warning when saturated), and the default NoopWriter discards outright.
func (p *Pipeline) record(ev store.Event) {
	// Stamped centrally rather than at each call site: the epoch is a property
	// of the pipeline, not of any one request, and every recorded event must
	// carry it for the per-epoch cost comparison to be complete.
	ev.ConfigEpoch = p.configEpoch
	// "client" is the default kind — real traffic — so every existing call
	// site (all of them client requests) needs no change. Non-client kinds
	// (e.g. "classifier") set Kind explicitly before calling record.
	if ev.Kind == "" {
		ev.Kind = "client"
	}
	p.store.Record(ev)
}

// recordClassifierCalls stores one event per upstream call a classifier made
// while producing sig (see types.ClassifierCallInfo — an LLM-backed
// classifier's own request, not the client's). Each shares the triggering
// request's trace and session key, so it shows up in context on that
// session's trajectory, and is tagged kind="classifier" so it's excluded
// from the default request list and every cost/latency aggregate (see
// Reader's kind='client' queries) — a classifier call's own spend must not
// be mistaken for what the client asked for.
//
// This runs before the real request's own event (recorded on success or
// failure, later in Execute), so a classifier call is visible even if
// routing or the upstream call that follows never completes.
//
// The row's axes come from the CALL when it reported its own (a decisions call
// fills several axes and knows which), falling back to the merged sig for a
// classifier that reports none — the LLM classifier's one-axis case, whose
// single verdict is the merged value anyway. Reading only the merged sig meant
// a multi-axis decisions call recorded its domain and nothing else, so
// cost_class was in the rationale text but empty as a field.
func (p *Pipeline) recordClassifierCalls(req *types.NormalizedRequest, sig types.Signals) {
	for _, call := range sig.ClassifierCalls {
		rationale := classifierRationale(call)
		// The verdict alone doesn't say what was judged, and the rationale is
		// what the request list shows before anyone opens the captured content
		// — so the classified text rides here too. Without it, "replied
		// code_generation" is unreadable as evidence: you cannot tell a clear
		// message the model misjudged from a rubric that failed to describe
		// the category.
		if input := ellipsize(call.Input, rationalePreview); input != "" {
			rationale += fmt.Sprintf(" — input %q", input)
		}
		axes := call.Axes
		confidence := callConfidence(call)
		if axes == nil {
			axes = map[string]string{}
			if sig.Domain != "" {
				axes[classifier.AxisDomain] = sig.Domain
			}
			if sig.Effort != "" {
				axes[classifier.AxisEffort] = sig.Effort
			}
			if sig.CostClass != "" {
				axes[classifier.AxisCostClass] = sig.CostClass
			}
		}
		p.record(store.Event{
			TraceID:          req.TraceID,
			SessionKey:       req.SessionKey,
			Kind:             "classifier",
			Format:           req.OriginalFormat,
			Provider:         call.Provider,
			Model:            call.Model,
			RoutingRationale: rationale,
			Domain:           axes[classifier.AxisDomain],
			Effort:           axes[classifier.AxisEffort],
			CostClass:        axes[classifier.AxisCostClass],
			Confidence:       confidence,
			Usage:            call.Usage,
			LatencyMs:        call.LatencyMs,
			StatusCode:       call.StatusCode,
			Error:            call.Error,
			Content:          p.classifierContent(call),
		})
	}
}

// callConfidence is the certainty to record for one classifier call. A
// multi-axis call reports a confidence per axis and has no single honest value,
// so the highest wins — the same rule Signals.Confidence follows, and the only
// claim available without picking an arbitrary axis.
//
// A call reporting neither falls back to 0, which the page renders as "0.0%"
// rather than blank. That is a real gap for a classifier that made no claim
// about its certainty (the LLM classifier sets 1.0 on a match and reports
// nothing on a failure), not a rendering problem — see the classifier row's
// rationale for what actually happened.
func callConfidence(call *types.ClassifierCallInfo) float64 {
	var best float64
	for _, c := range call.AxisConfidence {
		if c > best {
			best = c
		}
	}
	return best
}

// rationalePreview bounds how much of a classifier's input is echoed into the
// routing rationale. The rationale is rendered in a list row (and again in the
// detail page's <pre>), so this is sized to be recognisable at a glance rather
// than to carry the whole message — the full text is in the captured content.
const rationalePreview = 120

// classifierRationale renders one classifier call's outcome. A classifier that
// supplied its own Verdict owns its wording — a decisions call's outcome is a
// set of axis values with probabilities, which the LLM classifier's phrasing
// cannot express. Every other caller gets that phrasing, unchanged from before
// Verdict existed, so nothing already in the store reads differently.
func classifierRationale(call *types.ClassifierCallInfo) string {
	if call.Verdict != "" {
		return call.Verdict
	}
	if call.Error != "" {
		return fmt.Sprintf("LLM classifier failed (%s), fell back to heuristic", call.Error)
	}
	return fmt.Sprintf("LLM classifier replied %q", call.RawReply)
}

// ellipsize shortens s to at most max bytes on a rune boundary. Thin wrapper
// over types.Ellipsize, which is where the rune-boundary logic lives so a
// classifier's outbound input cap and this preview cannot disagree about it.
func ellipsize(s string, max int) string {
	return types.Ellipsize(s, max)
}

// classifierContent builds the captured content for one classifier call: the
// text it classified, plus the prompt it was given as a system block.
//
// The input is byte-identical to a block of the client's own request that
// capture already stored, so it addresses to the same content row and costs one
// reference, not a second body — and the classifier row is then joinable to the
// client request that triggered it. The prompt block is constant across calls
// for a given config, so it addresses to a single row forever and makes the
// classifier's row self-contained: "which rubric produced this verdict" is
// answerable without reconstructing a config from its epoch.
//
// Gated on the same storage.capture_content switch as everything else that
// writes conversation text to disk — a second switch for derived calls is a
// switch nobody keeps in sync. nil means nothing captured, so the store's
// no-content fast path still applies when capture is off.
func (p *Pipeline) classifierContent(call *types.ClassifierCallInfo) *store.CapturedContent {
	if !p.captureContent {
		return nil
	}
	var c store.CapturedContent
	if call.SystemPrompt != "" {
		c.Request = append(c.Request, store.Block{
			Kind: "text", Body: []byte(call.SystemPrompt), Role: "system",
		})
	}
	if call.Input != "" {
		// msg_index 1 so it sits after the prompt block, mirroring how
		// CaptureRequest indexes the system prompt ahead of the messages.
		c.Request = append(c.Request, store.Block{
			Kind: "text", Body: []byte(call.Input), Role: "user", MsgIndex: 1,
		})
	}
	return contentOrNil(c)
}

// recordRejected stores the captured content of a request that will never get
// a requests row. It is a no-op when capture is off or nothing was captured,
// so the error paths cost nothing in the default configuration.
//
// The id passed as the owner is derived from the trace id, which is stable for
// one request across all its error paths and distinguishes it from other
// rejections. It is not a rowid and there is no row: content_refs records the
// owner as ('rejected', <this>) on purpose, so a later change of policy that
// gives rejected requests real rows can promote them without rewriting these
// references (see content_refs in schema.sql).
func (p *Pipeline) recordRejected(traceID string, content store.CapturedContent) {
	if !p.captureContent || content.Empty() {
		return
	}
	if rec, ok := p.store.(store.ContentRecorder); ok {
		rec.RecordRejected(rejectionID(traceID), content)
	}
}

// capturedStreamBlocks turns the per-index text accumulated from a stream into
// capture blocks, matching CaptureResponse's indexing (position == block
// index) so streamed and non-streamed responses store the same shape. Empty
// entries — a block that produced no text, such as tool_use — are skipped
// rather than stored as an empty block that would dedup against every other
// empty block.
func capturedStreamBlocks(orderedText []string) []store.Block {
	var out []store.Block
	for index, text := range orderedText {
		if text == "" {
			continue
		}
		out = append(out, store.Block{
			Kind:     "text",
			Body:     []byte(text),
			Role:     "assistant",
			MsgIndex: 0,
			Position: index,
		})
	}
	return out
}

// contentOrNil returns a pointer to the capture, or nil when there is nothing
// to store, so the Event carries no content field at all in the common case
// (capture off, or nothing extracted) rather than an empty non-nil value.
func contentOrNil(c store.CapturedContent) *store.CapturedContent {
	if c.Empty() {
		return nil
	}
	return &c
}

// rejectionID turns a trace id into the stable numeric owner id used for
// rejected content. Hashing keeps it collision-resistant and independent of
// how the trace id was formed; only the low 63 bits are kept so the value is
// always a positive int64.
func rejectionID(traceID string) int64 {
	sum := sha256.Sum256([]byte("rejected:" + traceID))
	return int64(binary.BigEndian.Uint64(sum[:8]) >> 1)
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

// upstreamFailureStatus is what a failed upstream attempt is *reported* as: the
// upstream's own code when it gave one, else 502.
//
// The distinction matters because upstreamFailure returns 0 for a transport
// failure — no response was ever received, so there is no upstream status to
// record. That is the right value for "the upstream never answered", but it is
// not the status the client was given: writeArbiterError maps the same 0 to 502
// (internal/http/handler.go:257). Recording the raw 0 made every such request
// indistinguishable in the event store from one that has not finished, and left
// error queries (status_code >= 400) blind to the most common failure mode —
// nothing counted them as errors. Rows already written with 0 stay as they are;
// this is a go-forward fix, not a migration.
func upstreamFailureStatus(err error) int {
	status, _ := upstreamFailure(err)
	if status < 400 || status > 599 {
		return badGatewayStatus
	}
	return status
}

// badGatewayStatus is the status a transport failure is reported as. It is a
// literal rather than http.StatusBadGateway so the pipeline does not depend on
// net/http for one constant; internal/http/handler.go's writeArbiterError
// applies the same rule to the same case, and each side has a test asserting
// its half (TestUpstreamTransportFailureRecordsBadGateway here,
// TestWriteArbiterErrorMapsTransportFailureToBadGateway there) so the two
// cannot drift apart silently.
const badGatewayStatus = 502

// upstreamFailureProvider names the provider a failed attempt reached, when the
// error knows one; "" means the caller's own route provider is the answer.
func upstreamFailureProvider(err error) string {
	_, provider := upstreamFailure(err)
	return provider
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
//
// Rule 1 has one exception: a request that is NOT YET part of a session is
// classified for its signals even though its route is already decided. The
// signals are recorded, never applied — see classifyLiteral.
func (p *Pipeline) resolveRoute(ctx context.Context, req *types.NormalizedRequest, hasKey bool) (types.Route, types.Signals, error) {
	if route, ok := p.literalModelRoute(req.Model); ok {
		sig := p.classifyLiteral(ctx, req, hasKey)
		p.logger.LogRouting(ctx, route, sig, 0)
		return route, sig, nil
	}

	if hasKey {
		if provider, model, ok := p.affinity.get(ctx, req.SessionKey, req.Model); ok {
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

	// REQUIREMENTS.md §1: the model field is either a real model name or a
	// configured alias — the two categories /models lists. req.Model already
	// failed the literal-model check above; a force alias is the only other
	// legitimate reason to reach here with req.Model still set (it resolves
	// to no route by design, see above). Anything else is a typo or a stale
	// name the operator never declared, and must not be classified and routed
	// as if it were meaningful — reject rather than silently routing it
	// somewhere on the operator's dime.
	if req.Model != "" && (p.aliasResolver == nil || !p.aliasResolver.Has(req.Model)) {
		return types.Route{}, types.Signals{}, arbitererrors.NewUnknownModelError(
			fmt.Sprintf("model %q is not a configured provider model or alias", req.Model))
	}

	sig, err := p.classify(ctx, req)
	if err != nil {
		return types.Route{}, types.Signals{}, arbitererrors.NewClassificationError("classify request", err)
	}
	p.recordClassifierCalls(req, sig)
	sig = p.applyForceAlias(req, sig)

	routeStart := time.Now()
	route, _, err := p.router.Route(ctx, req, sig)
	if err != nil {
		return types.Route{}, types.Signals{}, arbitererrors.NewRoutingError("route request", err)
	}
	p.logger.LogRouting(ctx, route, sig, time.Since(routeStart))
	return route, sig, nil
}

// classifyLiteral produces signals for a request whose route is already
// decided by its own model name (resolveRoute rule 1).
//
// The signals are for the RECORD, never for the route: the client named a
// concrete model and goes exactly where it asked. What they buy is
// identification — without this, a title-generation request is
// indistinguishable on its row from ordinary traffic, because it shares the
// main model's name, fills no axis, and carries the generic
// `explicit model "..." -> provider "..."` rationale. The operator's own
// words: "if it happened, it should be shown."
//
// It runs only when the request is NOT YET part of a session (hasKey false, or
// no pin recorded yet). That is the rule from #1 — "every request that is not
// yet part of a session gets classified" — and it is also what keeps the cost
// bounded: the pin recorded after a successful call means turn 2 onward of a
// normal conversation skips this entirely. A title-gen request is the case
// that keeps classifying, and correctly so: its session key is derived from
// conversation text that changes on every call, so it is never part of a
// session and every one of its requests is a first request.
//
// A decisive signature (a title-gen matcher, say) ends the merge, so the
// model-backed classifiers behind it never run: an identified request costs no
// upstream call at all. Anything else pays for classification once per
// session, which is the accepted cost of #1.
//
// A classification failure is not fatal here, unlike on the classify+rules
// path. There the signals decide the route, so failing to produce them must
// fail the request; here the route is already known and only the row's
// annotations are lost, and failing a request the client would otherwise have
// been served is the worse outcome.
func (p *Pipeline) classifyLiteral(ctx context.Context, req *types.NormalizedRequest, hasKey bool) types.Signals {
	if hasKey {
		// A pin is the proof that this session already exists, whatever model
		// it was recorded under. Deliberately not affinity.get: that returns a
		// hit only when the client is still requesting the model the pin was
		// recorded under, so a client that switched models would look like a
		// brand-new session and be re-classified on every turn.
		if _, ok := p.affinity.pinned(ctx, req.SessionKey); ok {
			return types.Signals{}
		}
	}

	sig, err := p.classify(ctx, req)
	if err != nil {
		p.logger.LogError(ctx, "warn", err,
			map[string]interface{}{"stage": "classify_literal_model", "model": req.Model})
		return types.Signals{}
	}
	p.recordClassifierCalls(req, sig)
	// applyForceAlias is deliberately NOT called: a force alias exists to
	// shape what the router matches on, and nothing is matched here.
	return sig
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
		// The one place this is set: the client named a concrete model, so a
		// rate-limited provider must surface rather than be substituted.
		ExplicitModel: true,
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
func (p *Pipeline) executeStream(ctx context.Context, traceID string, route types.Route, req *types.NormalizedRequest, sessionKey string, hasKey bool, sig types.Signals, start time.Time, content store.CapturedContent) (interface{}, error) {
	headers := headersFromContext(ctx)
	// Send the request upstream (with fallback/retry handling) and get the
	// event channel. A 429/5xx fails SendStream synchronously — the HTTP
	// status is known before any SSE bytes flow — so fallback works exactly
	// as on the non-streaming path.
	_, eventChan, errChan, served, err := p.tryUpstream(ctx, route, req)
	if err != nil {
		p.logger.LogError(ctx, "error", err, map[string]interface{}{"provider": route.Provider})
		status, provider := upstreamFailureStatus(err), upstreamFailureProvider(err)
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
			RequestKind:      sig.RequestKind,
			Effort:           sig.Effort,
			CostClass:        sig.CostClass,
			Confidence:       sig.Confidence,
			LatencyMs:        time.Since(start).Milliseconds(),
			StatusCode:       status,
			Error:            err.Error(),
			Stream:           true,
			Content:          contentOrNil(content),
			Headers:          headers,
		})
		return nil, err
	}
	if hasKey {
		p.affinity.pin(ctx, sessionKey, req.Model, served.Provider, served.Model, p.cacheTTLFor(served.Provider))
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
		// actualModel captures the upstream's own reported model before the
		// fallback below overwrites an absent one with served.Model — same
		// reasoning as the non-streaming path's actualModel.
		var actualModel string
		// orderedText accumulates text deltas per block index so the streamed
		// response body can be captured after the fact. A slice indexed by
		// BlockIndex preserves the block order the client saw.
		var orderedText []string
		for evt := range eventChan {
			evt.TraceID = traceID
			if evt.MessageModel != "" && evt.MessageModel != served.Model && actualModel == "" {
				actualModel = evt.MessageModel
			}
			if evt.MessageModel == "" {
				evt.MessageModel = served.Model
			}
			if evt.InputTokens > 0 {
				usage.InputTokens = evt.InputTokens
			}
			if evt.OutputTokens > 0 {
				usage.OutputTokens = evt.OutputTokens
			}
			// Cache counters and provider-reported cost arrive on the usage
			// event; copying them here is what makes a streamed row show real
			// token counts and let a cache-affinity check be read off the store.
			if evt.CacheReadTokens > 0 {
				usage.CacheRead = evt.CacheReadTokens
			}
			if evt.CacheWriteTokens > 0 {
				usage.CacheWrite = evt.CacheWriteTokens
			}
			if evt.CostUSD > 0 {
				usage.CostUSD = evt.CostUSD
			}
			if evt.TextDelta != "" {
				if evt.BlockIndex >= len(orderedText) {
					// Grow to the index; a gap (a block that produced no text,
					// such as tool_use) leaves an empty entry rather than
					// shifting later blocks into the wrong position.
					orderedText = append(orderedText, make([]string, evt.BlockIndex-len(orderedText)+1)...)
				}
				orderedText[evt.BlockIndex] += evt.TextDelta
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
		// Content capture on the streaming path. The response half is rebuilt
		// from the accumulated deltas, since the text arrives in fragments; the
		// REQUEST half is the capture taken before pre-guardrails at the top of
		// Execute and handed down here. Both halves must ride on the event: an
		// earlier version recorded only the response, so every streamed row had
		// a reply with no prompt, and a session transcript read as a list of
		// answers to questions nobody asked. Only successfully completed text is
		// captured — tool_use arguments arrive as JSON fragments and are not
		// reassembled yet, so those blocks are stored hash-only rather than
		// guessed at.
		respContent := content
		if p.captureContent {
			respContent.Response = capturedStreamBlocks(orderedText)
		}
		p.record(store.Event{
			TraceID:          traceID,
			SessionKey:       sessionKey,
			Format:           req.OriginalFormat,
			Provider:         served.Provider,
			Model:            served.Model,
			ActualModel:      actualModel,
			AliasUsed:        p.aliasName(req.Model),
			RoutingRationale: served.Rationale,
			Domain:           sig.Domain,
			RequestKind:      sig.RequestKind,
			Effort:           sig.Effort,
			CostClass:        sig.CostClass,
			Confidence:       sig.Confidence,
			Usage:            usage,
			LatencyMs:        time.Since(start).Milliseconds(),
			StatusCode:       status,
			Error:            errMsg,
			Stream:           true,
			Content:          contentOrNil(respContent),
			Headers:          headers,
		})
	}()

	// The HTTP handler consumes this and flushes events as SSE.
	return &upstream.StreamResponse{EventChan: out}, nil
}

// upstreamAction is what to do after a failed upstream attempt.
type upstreamAction int

const (
	actionRetrySame     upstreamAction = iota // transient (5xx): try this provider again
	actionNextCandidate                       // move on to the next fallback provider
	actionFailFast                            // give up; a retry can't help
	// actionRetryThisProvider is for a 429 on an explicit model: retry the SAME
	// provider (a retry is not substitution), but never fall through to a
	// fallback — the client named this model, so once the attempts are exhausted
	// the 429 goes back to the client rather than becoming a different model's
	// answer.
	actionRetryThisProvider
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

	// The output cap the upstream should honour, resolved per candidate. The
	// translator cannot answer this: it is a pure wire-format converter with no
	// catalog access, and a request that arrived as OpenAI never carried a
	// max_tokens in the first place. Left to itself it substitutes a fixed 4096
	// for Anthropic, which silently caps every reply at 4096 tokens — the
	// catalog's max_output_tokens is the real limit, and this is the only place
	// that knows both.
	//
	// A client that DID send max_tokens keeps it: this fills a gap, it does not
	// override an explicit instruction. The value is recomputed per candidate,
	// since a fallback may have a different (or no) catalog row.
	originalMaxTokens := req.MaxTokens
	defer func() { req.MaxTokens = originalMaxTokens }()

candidates:
	for _, cand := range candidates {
		if until, cooling := p.onCooldown(cand.Provider); cooling {
			p.logger.LogUpstreamCooldown(ctx, cand.Provider, until, 0, "skipped")

			// An explicit model is not substituted. The client named this model,
			// so a rate-limited provider is reported rather than quietly routed
			// around — falling through here would serve a different model with a
			// 200 and no indication, which is the one outcome an explicit request
			// must never get. This is also the cheaper refusal: the upstream is
			// known to be rate-limited, so it is not contacted at all.
			if cand.ExplicitModel {
				return nil, nil, nil, types.Route{}, p.cooldownError(cand, until)
			}
			continue
		}

		maxAttempts := 1
		if cand.Config.RetryMax > 0 {
			maxAttempts = cand.Config.RetryMax + 1
		}

		// Applied per candidate, left set for the caller: on success the
		// caller records the event from this request, and what it must record
		// is the cap that was actually sent upstream. The deferred restore
		// puts the caller's own value back on every return path.
		req.MaxTokens = p.outputCapFor(cand, originalMaxTokens)

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
				case actionRetryThisProvider:
					// Retry this provider while attempts remain, then surface the
					// error. Falling out of the attempt loop instead would advance
					// to the next candidate, which is the substitution this action
					// exists to prevent.
					if attempt < maxAttempts {
						continue
					}
					return nil, nil, nil, types.Route{}, lastErr
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
			case actionRetryThisProvider:
				// See the streaming branch: exhausting the attempts must surface
				// the error, not fall through to another provider.
				if attempt < maxAttempts {
					continue
				}
				return nil, nil, nil, types.Route{}, lastErr
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

// outputCapFor is the output-token cap to send upstream for a candidate route.
// clientCap is what the client itself asked for (0 when it asked for nothing).
//
// A client-supplied cap always wins: this fills a gap, it does not override an
// explicit instruction. With no client cap, the catalog's max_output_tokens is
// used when the model states one — that is the model's real limit, and it is
// what a request arriving as OpenAI never carried.
//
// Falling back to 0 rather than a number is deliberate. A 0 reaches the
// translator, which substitutes its own default only for Anthropic (where the
// field is required); an OpenAI-format upstream simply sees no cap, which is
// the correct wire behaviour for a client that sent none. Inventing a limit
// here would reproduce the bug this exists to fix, on a second path.
func (p *Pipeline) outputCapFor(route types.Route, clientCap int) int {
	if clientCap > 0 {
		return clientCap
	}
	if p.costCatalog == nil {
		return 0
	}
	mc, ok := p.costCatalog.Lookup(route.Provider, route.Model)
	if !ok || mc.MaxOutputTokens == nil || *mc.MaxOutputTokens <= 0 {
		return 0
	}
	return *mc.MaxOutputTokens
}

// classifyUpstreamError logs a failed upstream attempt and decides what to
// do next: retry the same provider (5xx — transient), try the next fallback
// (429 — the cooldown is recorded here), or fail fast (other 4xx — a
// different provider won't fix a bad request; non-HTTP failures are
// Arbiter's own and aren't retryable either).
//
// cooldownError builds the refusal for an explicit model whose provider is still
// in its 429 cooldown. The status is 429 so writeArbiterError passes it through
// unchanged, and the remaining time is included because it is the one piece of
// information that makes the refusal actionable — the client knows when retrying
// is worth it rather than guessing.
func (p *Pipeline) cooldownError(route types.Route, until time.Time) error {
	remaining := time.Until(until).Round(time.Second)
	if remaining < 0 {
		remaining = 0
	}
	err := arbitererrors.NewUpstreamError(
		route.Provider, http.StatusTooManyRequests,
		fmt.Sprintf("%s is rate limited; retry in %s", route.Model, remaining), nil)
	err.RetryAfter = remaining
	return err
}

// classifyUpstreamError decides what to do after a failed attempt. cand is
// needed because the decision depends on whether the route was explicit — an
// explicit model must never be substituted, so a 429 fails fast for it while
// every other route falls through to its fallback chain.
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
		// The cooldown is recorded even for an explicit route: it is a routing
		// heuristic, and later non-explicit traffic should route around this
		// provider normally. Only the response to THIS client differs.
		p.markCooldown(ue.Provider, until)
		p.logger.LogUpstreamCooldown(ctx, ue.Provider, until, ue.RetryAfter, "recorded")

		// An explicit model is not substituted: the client named it, so a 429
		// goes back to the client rather than to the fallback chain. Retrying the
		// same provider is still allowed — a retry is not substitution — so this
		// is not actionFailFast, which would skip the remaining attempts.
		if cand.ExplicitModel {
			return actionRetryThisProvider
		}
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
	return p.cooldowns.onCooldown(provider)
}

// markCooldown extends a provider's cooldown to `until` (never shortens an
// existing one — a second 429 with a longer Retry-After extends, a shorter
// one doesn't cut the current cooldown).
func (p *Pipeline) markCooldown(provider string, until time.Time) {
	p.cooldowns.markCooldown(provider, until)
}

// ClearCooldowns drops every provider's 429 backoff and returns how many were
// active. Exposed for the deliberate reset the operator asked for: a config edit
// must not clear cooldowns as a side effect, so this is its own action.
func (p *Pipeline) ClearCooldowns() int {
	return p.cooldowns.clear()
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
