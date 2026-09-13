// Package upstream makes the actual HTTP calls to provider APIs and parses
// their responses (including rate-limit/cost signals from headers and body)
// back into NormalizedResponse.
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	SendStream(ctx context.Context, route types.Route, req *types.NormalizedRequest) (<-chan *types.NormalizedStreamEvent, error)
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

	// Route.Model is the resolved model to actually send upstream, which
	// may differ from what the client originally asked for (e.g. fallback
	// routing rewrote it) — always use it over req.Model.
	reqCopy := *req
	reqCopy.Model = route.Model

	switch route.Config.Type {
	case "anthropic":
		return c.sendAnthropic(ctx, route, &reqCopy)
	case "openai", "ollama":
		return c.sendOpenAI(ctx, route, &reqCopy)
	default:
		return nil, arbitererrors.NewUpstreamError(route.Provider, 0, fmt.Sprintf("unknown provider type %q", route.Config.Type), nil)
	}
}

// SendStream sends a streaming request to the upstream provider and returns
// a channel of normalized stream events. The caller is responsible for
// closing the returned channel; SendStream closes it when done.
func (c *HTTPClient) SendStream(ctx context.Context, route types.Route, req *types.NormalizedRequest) (<-chan *types.NormalizedStreamEvent, error) {
	// The stream is read by a background goroutine that outlives this call,
	// so the timeout's cancel func must be released there (once the read is
	// done) rather than deferred here — deferring here would cancel ctx, and
	// with it the in-flight body read, the instant SendStream returns.
	cancel := func() {}
	if route.Config.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, route.Config.Timeout)
	}

	// Route.Model is the resolved model to actually send upstream.
	reqCopy := *req
	reqCopy.Model = route.Model

	eventChan := make(chan *types.NormalizedStreamEvent, 10)

	switch route.Config.Type {
	case "anthropic":
		if err := c.sendAnthropicStream(ctx, route, &reqCopy, eventChan, cancel); err != nil {
			cancel()
			close(eventChan)
			return nil, err
		}
	case "openai", "ollama":
		if err := c.sendOpenAIStream(ctx, route, &reqCopy, eventChan, cancel); err != nil {
			cancel()
			close(eventChan)
			return nil, err
		}
	default:
		cancel()
		close(eventChan)
		return nil, arbitererrors.NewUpstreamError(route.Provider, 0, fmt.Sprintf("unknown provider type %q", route.Config.Type), nil)
	}

	return eventChan, nil
}

func (c *HTTPClient) sendAnthropic(ctx context.Context, route types.Route, req *types.NormalizedRequest) (*types.NormalizedResponse, error) {
	wireReq, err := c.translator.NormalizedToAnthropicRequest(req)
	if err != nil {
		return nil, arbitererrors.NewTranslationError("post_routing", "normalized to anthropic request", err)
	}

	body, err := json.Marshal(wireReq)
	if err != nil {
		return nil, arbitererrors.NewTranslationError("post_routing", "marshal anthropic request", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, route.Config.Endpoint+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, arbitererrors.NewUpstreamError(route.Provider, 0, "build request", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", route.Config.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	for k, v := range route.Config.Headers {
		httpReq.Header.Set(k, v)
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, arbitererrors.NewUpstreamError(route.Provider, 0, "request failed", err)
	}
	defer func() {
		_ = httpResp.Body.Close()
	}()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, arbitererrors.NewUpstreamError(route.Provider, httpResp.StatusCode, "read response body", err)
	}

	if httpResp.StatusCode >= 400 {
		return nil, upstreamErrorFrom(route.Provider, httpResp, respBody)
	}

	var wireResp types.AnthropicResponse
	if err := json.Unmarshal(respBody, &wireResp); err != nil {
		return nil, arbitererrors.NewTranslationError("post_routing", "unmarshal anthropic response", err)
	}

	normalized, err := c.translator.AnthropicResponseToNormalized(&wireResp)
	if err != nil {
		return nil, arbitererrors.NewTranslationError("post_routing", "anthropic response to normalized", err)
	}
	normalized.RateLimit = parseAnthropicRateLimitHeaders(httpResp.Header)
	return normalized, nil
}

func (c *HTTPClient) sendOpenAI(ctx context.Context, route types.Route, req *types.NormalizedRequest) (*types.NormalizedResponse, error) {
	wireReq, err := c.translator.NormalizedToOpenAIRequest(req)
	if err != nil {
		return nil, arbitererrors.NewTranslationError("post_routing", "normalized to openai request", err)
	}

	body, err := json.Marshal(wireReq)
	if err != nil {
		return nil, arbitererrors.NewTranslationError("post_routing", "marshal openai request", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, route.Config.Endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, arbitererrors.NewUpstreamError(route.Provider, 0, "build request", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+route.Config.APIKey)
	for k, v := range route.Config.Headers {
		httpReq.Header.Set(k, v)
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, arbitererrors.NewUpstreamError(route.Provider, 0, "request failed", err)
	}
	defer func() {
		_ = httpResp.Body.Close()
	}()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, arbitererrors.NewUpstreamError(route.Provider, httpResp.StatusCode, "read response body", err)
	}

	if httpResp.StatusCode >= 400 {
		return nil, upstreamErrorFrom(route.Provider, httpResp, respBody)
	}

	var wireResp types.OpenAIResponse
	if err := json.Unmarshal(respBody, &wireResp); err != nil {
		return nil, arbitererrors.NewTranslationError("post_routing", "unmarshal openai response", err)
	}

	normalized, err := c.translator.OpenAIResponseToNormalized(&wireResp)
	if err != nil {
		return nil, arbitererrors.NewTranslationError("post_routing", "openai response to normalized", err)
	}
	return normalized, nil
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
