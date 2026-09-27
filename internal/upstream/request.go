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
// Header ordering matters and is deliberate: Content-Type, then the
// credential, then any static provider header, then the client's forwarded
// beta opt-in, and finally the operator's configured Headers — so a static
// config value cannot silently win over what the client negotiated, and the
// config block remains the operator's last word.
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
	// The client's beta opt-ins, forwarded. The body is rebuilt, so a beta
	// the client negotiated is lost unless the header is carried, and
	// interleaved thinking is gated on one.
	if req.ClientBeta != "" {
		httpReq.Header.Set("anthropic-beta", req.ClientBeta)
	}
	for k, v := range route.Config.Headers {
		httpReq.Header.Set(k, v)
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
