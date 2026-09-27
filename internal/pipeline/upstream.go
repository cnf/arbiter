package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/cnf/arbiter/internal/classifier"
	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

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
