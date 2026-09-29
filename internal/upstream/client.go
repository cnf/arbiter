// Package upstream makes the actual HTTP calls to provider APIs and parses
// their responses (including rate-limit/cost signals from headers and body)
// back into NormalizedResponse.
package upstream

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// Client sends a NormalizedRequest to whatever provider a Route names and
// returns a NormalizedResponse (or a stream channel if streaming).
type Client interface {
	Send(ctx context.Context, route types.Route, req *types.NormalizedRequest) (*types.NormalizedResponse, error)
	// SendStream returns the event channel plus a one-shot error channel that
	// receives the stream's terminal outcome (nil on a clean finish, non-nil
	// if the SSE read failed mid-stream) once the event channel has been
	// closed. Callers must drain the event channel before reading it.
	SendStream(ctx context.Context, route types.Route, req *types.NormalizedRequest) (<-chan *types.NormalizedStreamEvent, <-chan error, error)
}

// StreamResponse carries a channel of normalized stream events from the upstream.
type StreamResponse struct {
	EventChan <-chan *types.NormalizedStreamEvent
}

// Translator is the subset of translator.Translator the upstream client
// needs, declared locally to avoid an import cycle (translator doesn't
// depend on upstream, and doesn't need to).
type Translator interface {
	NormalizedToAnthropicRequest(req *types.NormalizedRequest) (*types.AnthropicRequest, error)
	NormalizedToOpenAIRequest(req *types.NormalizedRequest) (*types.OpenAIRequest, error)
	AnthropicResponseToNormalized(resp *types.AnthropicResponse) (*types.NormalizedResponse, error)
	OpenAIResponseToNormalized(resp *types.OpenAIResponse) (*types.NormalizedResponse, error)
}

// HTTPClient calls upstream providers over plain HTTP(S).
type HTTPClient struct {
	translator Translator
	http       *http.Client
}

// NewHTTPClient creates an upstream HTTP client. Per-request timeouts come
// from each provider's own config (types.ProviderConfig.Timeout) rather
// than a single client-wide value, applied via context in Send.
func NewHTTPClient(translator Translator) *HTTPClient {
	return &HTTPClient{
		translator: translator,
		http:       &http.Client{},
	}
}

// Send translates req into the target provider's wire format, POSTs it,
// and translates the response (or error) back. For streaming requests
// (req.Stream == true), use SendStream instead.
func (c *HTTPClient) Send(ctx context.Context, route types.Route, req *types.NormalizedRequest) (*types.NormalizedResponse, error) {
	if route.Config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, route.Config.Timeout)
		defer cancel()
	}

	wf, ok := wireFor(route.Config.Type)
	if !ok {
		return nil, arbitererrors.NewUpstreamError(route.Provider, 0, fmt.Sprintf("unknown provider type %q", route.Config.Type), nil)
	}

	// Route.Model is the resolved model to actually send upstream, which
	// may differ from what the client originally asked for (e.g. fallback
	// routing rewrote it) — always use it over req.Model.
	reqCopy := *req
	reqCopy.Model = route.Model

	httpReq, err := buildWireRequest(ctx, c.translator, route, &reqCopy, wf)
	if err != nil {
		return nil, err
	}

	body, respHeader, err := c.doAndRead(route.Provider, httpReq, "read response body")
	if err != nil {
		return nil, err
	}

	normalized, err := wf.fromWire(c.translator, body)
	if err != nil {
		return nil, err
	}
	if wf.rateLimit != nil {
		normalized.RateLimit = wf.rateLimit(respHeader)
	}
	return normalized, nil
}

// SendStream sends a streaming request to the upstream provider and returns
// a channel of normalized stream events plus a one-shot channel carrying the
// stream's terminal error (nil on success), sent once eventChan is closed.
// The caller is responsible for closing the returned channel; SendStream
// closes it when done.
func (c *HTTPClient) SendStream(ctx context.Context, route types.Route, req *types.NormalizedRequest) (<-chan *types.NormalizedStreamEvent, <-chan error, error) {
	// The stream is read by a background goroutine that outlives this call,
	// so the cancel func must be released there (once the read is done)
	// rather than deferred here — deferring here would cancel ctx, and with
	// it the in-flight body read, the instant SendStream returns.
	//
	// route.Config.Timeout is deliberately NOT applied as a total deadline:
	// on a stream that would sever a long generation mid-flight at an
	// arbitrary wall-clock point, and a slow answer is not a failed one. It
	// bounds time-to-headers and then the idle gap between events instead —
	// see streamWatchdog.
	ctx, cancel := context.WithCancel(ctx)
	watchdog := newStreamWatchdog(route.Config.Timeout, cancel)

	wf, ok := wireFor(route.Config.Type)
	if !ok {
		watchdog.stop()
		return nil, nil, arbitererrors.NewUpstreamError(route.Provider, 0, fmt.Sprintf("unknown provider type %q", route.Config.Type), nil)
	}

	// Route.Model is the resolved model to actually send upstream.
	reqCopy := *req
	reqCopy.Model = route.Model

	eventChan := make(chan *types.NormalizedStreamEvent, 10)
	errChan := make(chan error, 1)

	if err := c.streamFrom(ctx, route, &reqCopy, wf, eventChan, errChan, watchdog); err != nil {
		watchdog.stop()
		close(eventChan)
		return nil, nil, err
	}

	return eventChan, errChan, nil
}

// streamWatchdog bounds a stream by *silence* rather than total duration.
// It arms a timer for idle; every event read pushes the deadline out
// again, so a stream that keeps producing runs as long as it likes, while
// one whose upstream goes quiet (hung connection, half-open socket that
// never returns EOF) is cancelled instead of hanging forever. stop()
// releases the context and must run exactly once, when the read is done.
//
// A zero or negative idle means "no watchdog" — the stream is bounded only
// by the upstream finishing or the client disconnecting.
type streamWatchdog struct {
	cancel context.CancelFunc
	idle   time.Duration
	timer  *time.Timer // nil when no idle timeout is configured
}

func newStreamWatchdog(idle time.Duration, cancel context.CancelFunc) *streamWatchdog {
	w := &streamWatchdog{cancel: cancel}
	if idle > 0 {
		w.timer = time.AfterFunc(idle, cancel)
		w.idle = idle
	}
	return w
}

// keepalive restarts the idle countdown. Safe to call from the single
// goroutine reading the stream.
func (w *streamWatchdog) keepalive() {
	if w.timer != nil {
		w.timer.Reset(w.idle)
	}
}

// stop cancels the stream context and disarms the watchdog.
func (w *streamWatchdog) stop() {
	if w.timer != nil {
		w.timer.Stop()
	}
	w.cancel()
}

// upstreamErrorFrom builds an UpstreamError from an HTTP error response,
// capturing the Retry-After header on a 429 so callers can honor the
// upstream's cooldown instead of retrying into a wall.
func upstreamErrorFrom(provider string, httpResp *http.Response, body []byte) *arbitererrors.UpstreamError {
	e := arbitererrors.NewUpstreamError(provider, httpResp.StatusCode, string(body), nil)
	if httpResp.StatusCode == http.StatusTooManyRequests {
		e.RetryAfter = parseRetryAfter(httpResp.Header)
	}
	return e
}

// parseRetryAfter parses a Retry-After header value: either delay-seconds or
// an HTTP-date. Returns 0 when absent or unparseable — callers apply their
// own default cooldown in that case.
func parseRetryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs > 0 {
			return time.Duration(secs) * time.Second
		}
		return 0
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// parseAnthropicRateLimitHeaders extracts quota state from Anthropic's
// anthropic-ratelimit-* response headers. Missing/unparseable headers are
// left as zero values rather than erroring — rate-limit info is a nice-to
// -have for routing/logging, not something worth failing the request over.
func parseAnthropicRateLimitHeaders(h http.Header) *types.RateLimitInfo {
	info := &types.RateLimitInfo{Unified5hRemaining: -1}

	info.RequestsRemaining = atoiOrZero(h.Get("anthropic-ratelimit-requests-remaining"))
	info.RequestsLimit = atoiOrZero(h.Get("anthropic-ratelimit-requests-limit"))
	info.TokensRemaining = atoiOrZero(h.Get("anthropic-ratelimit-tokens-remaining"))
	info.TokensLimit = atoiOrZero(h.Get("anthropic-ratelimit-tokens-limit"))

	if reset := h.Get("anthropic-ratelimit-requests-reset"); reset != "" {
		if t, err := time.Parse(time.RFC3339, reset); err == nil {
			info.ResetAt = t
		}
	}

	if unified := h.Get("anthropic-ratelimit-unified-5h-remaining"); unified != "" {
		if f, err := strconv.ParseFloat(unified, 64); err == nil {
			info.Unified5hRemaining = f
		}
	}
	if reset := h.Get("anthropic-ratelimit-unified-5h-reset"); reset != "" {
		if t, err := time.Parse(time.RFC3339, reset); err == nil {
			info.Unified5hReset = t
		}
	}

	return info
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
