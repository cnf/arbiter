package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// wireFormat describes how one provider type's HTTP API differs from the
// others: the URL path, the auth header, the extra static headers, and the
// two translator calls that convert the request and response bodies.
//
// Anthropic and OpenAI-compatible providers share everything else — build,
// POST, read, check status, translate back — so that shared shape lives once
// in buildWireRequest and the two table entries below carry only the deltas.
// The alternative, four near-identical send functions, is how a fix to one
// (the beta-header forwarding, or the streaming error path) ends up applied
// to the other three only sometimes.
type wireFormat struct {
	// path is appended to route.Config.Endpoint.
	path string
	// setAuth writes the provider's credential header onto req.
	setAuth func(req *http.Request, cfg types.ProviderConfig)
	// setStatic writes any provider-specific static headers (e.g. an API
	// version). Runs after setAuth and before the config Headers loop.
	setStatic func(req *http.Request)
	// toWire marshals the normalized request into the provider's wire body.
	toWire func(t Translator, req *types.NormalizedRequest) (any, error)
	// fromWire parses the provider's response body back into a normalized one.
	fromWire func(t Translator, body []byte) (*types.NormalizedResponse, error)
	// rateLimit extracts quota state from the response headers; nil when the
	// provider exposes none.
	rateLimit func(h http.Header) *types.RateLimitInfo
}

// anthropicWire and openaiWire are the two supported shapes. "ollama" is
// OpenAI-compatible and shares its entry.
var (
	anthropicWire = wireFormat{
		path: "/v1/messages",
		setAuth: func(req *http.Request, cfg types.ProviderConfig) {
			req.Header.Set("x-api-key", cfg.APIKey)
		},
		setStatic: func(req *http.Request) {
			req.Header.Set("anthropic-version", "2023-06-01")
		},
		toWire: func(t Translator, req *types.NormalizedRequest) (any, error) {
			return t.NormalizedToAnthropicRequest(req)
		},
		fromWire: func(t Translator, body []byte) (*types.NormalizedResponse, error) {
			var wire types.AnthropicResponse
			if err := json.Unmarshal(body, &wire); err != nil {
				return nil, arbitererrors.NewTranslationError("post_routing", "unmarshal anthropic response", err)
			}
			return t.AnthropicResponseToNormalized(&wire)
		},
		rateLimit: parseAnthropicRateLimitHeaders,
	}

	openaiWire = wireFormat{
		path: "/chat/completions",
		setAuth: func(req *http.Request, cfg types.ProviderConfig) {
			req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		},
		toWire: func(t Translator, req *types.NormalizedRequest) (any, error) {
			return t.NormalizedToOpenAIRequest(req)
		},
		fromWire: func(t Translator, body []byte) (*types.NormalizedResponse, error) {
			var wire types.OpenAIResponse
			if err := json.Unmarshal(body, &wire); err != nil {
				return nil, arbitererrors.NewTranslationError("post_routing", "unmarshal openai response", err)
			}
			return t.OpenAIResponseToNormalized(&wire)
		},
	}
)

// wire format for a route's provider type, or false when the type is unknown.
func wireFor(providerType string) (wireFormat, bool) {
	switch providerType {
	case "anthropic":
		return anthropicWire, true
	case "openai", "ollama":
		return openaiWire, true
	default:
		return wireFormat{}, false
	}
}

// buildWireRequest translates req into the provider's wire format and builds
// the outbound HTTP request. This is the part all four send paths share.
//
// Header precedence is deliberate and is stated per header, not as one global
// ordering, because different headers want opposite winners:
//
//	Content-Type, credential, provider defaults   lowest — Arbiter's own
//	                                              minimum for the request
//	route.Config.Headers                          operator's explicit choice;
//	                                              overrides all of the above
//	the client's negotiated opt-in (ClientBeta)   highest — a static config
//	                                              value must never silently
//	                                              mask what the client asked
//	                                              for
//
// The last step is the one that matters. Anthropic gates extended thinking on
// `anthropic-beta`, and the outbound body is rebuilt rather than relayed — so
// if a provider's `headers:` map writes that header after the client's value,
// the config quietly wins, interleaved thinking stops working mid-chain, and
// nothing logs it. Writing the negotiated value last is what makes the client's
// opt-in unsuppressable.
//
// The consequence for the operator is deliberate: `headers:` cannot be used to
// pin `anthropic-beta` against a client that negotiates a different value. Pin
// it by not routing those clients here. `anthropic-version` is unaffected and
// stays overridable, since no client negotiates it and providers genuinely do
// need to pin it — see anthropicWire.setStatic.
func buildWireRequest(ctx context.Context, t Translator, route types.Route, req *types.NormalizedRequest, wf wireFormat) (*http.Request, error) {
	wireReq, err := wf.toWire(t, req)
	if err != nil {
		return nil, arbitererrors.NewTranslationError("post_routing", "normalized to "+route.Config.Type+" request", err)
	}

	body, err := json.Marshal(wireReq)
	if err != nil {
		return nil, arbitererrors.NewTranslationError("post_routing", "marshal "+route.Config.Type+" request", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, route.Config.Endpoint+wf.path, bytes.NewReader(body))
	if err != nil {
		return nil, arbitererrors.NewUpstreamError(route.Provider, 0, "build request", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	wf.setAuth(httpReq, route.Config)
	if wf.setStatic != nil {
		wf.setStatic(httpReq)
	}
	for k, v := range route.Config.Headers {
		httpReq.Header.Set(k, v)
	}
	// Last, so it wins. See the precedence note above: the client's
	// negotiated beta opt-ins are the request's own, and a provider's static
	// `headers:` entry must not be able to mask them.
	if req.ClientBeta != "" {
		httpReq.Header.Set("anthropic-beta", req.ClientBeta)
	}
	return httpReq, nil
}

// doAndRead sends the built request, reads the body, and converts a non-2xx
// status into an UpstreamError carrying the Retry-After cooldown on a 429.
// The body is always closed by the time this returns; the response headers
// are returned by value because the caller needs them after the close (for
// rate-limit extraction) and http.Header is safe to read once copied.
func (c *HTTPClient) doAndRead(provider string, httpReq *http.Request, readErrContext string) ([]byte, http.Header, error) {
	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, nil, arbitererrors.NewUpstreamError(provider, 0, "request failed", err)
	}
	defer func() {
		_ = httpResp.Body.Close()
	}()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, nil, arbitererrors.NewUpstreamError(provider, httpResp.StatusCode, readErrContext, err)
	}

	if httpResp.StatusCode >= 400 {
		return nil, nil, upstreamErrorFrom(provider, httpResp, body)
	}
	return body, httpResp.Header, nil
}
