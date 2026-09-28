package pipeline

import (
	"context"
	"net/http"
	"time"

	"github.com/cnf/arbiter/internal/store"
	arbitererrors "github.com/cnf/arbiter/pkg/errors"
)

// caller's already-known wire format ("anthropic" or "openai") — the HTTP
// layer knows this from which endpoint was hit, so Execute never needs to
// sniff it. sessionHint is an inbound session identifier (e.g. an
// X-Session-Id header), used to pin this conversation to whichever
// provider/model actually serves it — see resolveRoute.
func (p *Pipeline) Execute(ctx context.Context, payload []byte, format string, traceID string, sessionHint string) (interface{}, error) {
	ctx = p.logger.WithTraceID(ctx, traceID)
	start := time.Now().UTC()
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

	// promptHash separates prompt FAMILIES sharing one sessionKey (the main
	// thread, a title-generation call, a subagent run each have a different
	// system prompt) so the affinity pin cannot let one family silently
	// overwrite another's target. Computed from req.SystemPrompt here,
	// BEFORE pre-guardrails run, for the same reason sessionKey is: hashing
	// afterward would mix in Arbiter's own injected text and rotate every
	// pin at once whenever that guardrail's prompt is edited. This is the
	// same value ClientSystemPrompt is about to be set to, just captured
	// slightly earlier because affinityKey needs it before that assignment.
	promptHash := PromptHash(req.SystemPrompt)

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
	// This capture stays "as sent" and feeds the dedup corpus; it is not what
	// the UI shows by default (see the post-guardrail capture below and #13).
	var content store.CapturedContent
	if p.captureContent {
		content.Request = store.CaptureRequest(req)
	}

	var preGuardrailApplied bool
	for _, g := range p.preGuardrails {
		if !g.ShouldRun(req) {
			continue
		}
		mutated, err := g.ApplyPre(ctx, req)
		if err != nil {
			p.logger.LogGuardrail(ctx, g.Name(), "rejected", false)
			// A guardrail rejection is a real request the operator will want
			// to see ("why was this refused?") — #5: "nothing invisible", it
			// gets a real requests row like any other failed client request,
			// not a content-only stub under owner_kind="rejected".
			p.recordFailed(ctx, traceID, sessionKey, format, req.Model, start, err, content)
			return nil, err
		}
		p.logger.LogGuardrail(ctx, g.Name(), "applied", true)
		req = mutated
		preGuardrailApplied = true
	}

	// Capture again AFTER pre-guardrails, but only when one actually ran —
	// with none configured (or none matching ShouldRun) the two captures
	// would be byte-identical, and storing both would double every block's
	// reference rows for zero benefit. This is the request as it actually
	// went upstream, and is #13's default/primary UI view; the pre-guardrail
	// form above is only shown on demand.
	if p.captureContent && preGuardrailApplied {
		content.RequestGuardrailed = store.CaptureRequest(req)
	}

	route, sig, err := p.resolveRoute(ctx, req, hasKey, promptHash, start)
	if err != nil {
		// Routing failed (no rule matched and no fallback router, a config
		// the request can't be routed under, or a deliberate `stop` rule).
		// Same reasoning as a guardrail rejection (#5): give it a real row.
		p.recordFailed(ctx, traceID, sessionKey, format, req.Model, start, err, content)
		return nil, err
	}

	// Streaming vs. non-streaming: different code paths
	if req.Stream {
		return p.executeStream(ctx, traceID, route, req, sessionKey, promptHash, hasKey, sig, start, content)
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
			ArrivalTs:        start,
			LatencyMs:        time.Since(start).Milliseconds(),
			StatusCode:       status,
			Error:            err.Error(),
			Content:          contentOrNil(content),
			Headers:          headers,
		})
		return nil, err
	}
	if hasKey && p.pins(sig.RequestKind) {
		p.affinity.pin(ctx, sessionKey, promptHash, req.Model, served.Provider, served.Model, p.cacheTTLFor(served.Provider))
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
		ArrivalTs:        start,
		LatencyMs:        time.Since(start).Milliseconds(),
		StatusCode:       http.StatusOK,
		ToolCalls:        toolCallNames(resp.Content),
		Headers:          headers,
	})
	return out, nil
}
