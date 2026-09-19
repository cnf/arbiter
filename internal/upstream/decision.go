package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// DecisionClient sends a decision-model request to a provider and returns its
// typed answers.
//
// A separate interface from Client on purpose. Client's two methods both
// return a NormalizedResponse — the hub-and-spoke representation of a chat
// completion. A decision call has a different endpoint, a different body
// (`state` + `questions`) and a different response (`answers`), so expressing
// it as a chat request would mean fabricating a NormalizedResponse that
// carries JSON in a text block. That fiction would then have to be seen
// through by every consumer, starting with the store.
type DecisionClient interface {
	Decide(ctx context.Context, route types.Route, req *types.DecisionRequest) (*types.DecisionResponse, error)
}

// Decide POSTs a decision request and decodes the typed answers.
//
// The URL is route.Config.Endpoint verbatim: for a provider of type
// "decisions", `endpoint` is the COMPLETE decisions URL, not an API root.
// Every other provider type treats Endpoint as a root and this package
// appends its own suffix ("/v1/messages", "/chat/completions") — decisions
// deliberately does not, because the endpoint path is known to be unstable
// (OpenRouter's /api/alpha/decisions is alpha, TypeSafe's own is
// /v1/systemone). Keeping the whole URL in config makes a vendor path change
// a config edit rather than a code change, which is the whole point.
//
// Auth is the OpenAI convention (bearer token), because that is what the
// endpoint that serves this today expects; a vendor needing something else is
// covered by the provider's own `headers` map, applied last like every other
// provider type.
func (c *HTTPClient) Decide(ctx context.Context, route types.Route, req *types.DecisionRequest) (*types.DecisionResponse, error) {
	if route.Config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, route.Config.Timeout)
		defer cancel()
	}

	// Route.Model is the resolved model to send upstream, which may differ
	// from what the caller named (an alias resolves to a member; a fallback
	// rewrites it) — the same rule Send applies.
	reqCopy := *req
	reqCopy.Model = route.Model

	body, err := json.Marshal(&reqCopy)
	if err != nil {
		return nil, arbitererrors.NewTranslationError("post_routing", "marshal decision request", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, route.Config.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, arbitererrors.NewUpstreamError(route.Provider, 0, "build decision request", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+route.Config.APIKey)
	for k, v := range route.Config.Headers {
		httpReq.Header.Set(k, v)
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, arbitererrors.NewUpstreamError(route.Provider, 0, "decision request failed", err)
	}
	defer func() {
		_ = httpResp.Body.Close()
	}()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, arbitererrors.NewUpstreamError(route.Provider, httpResp.StatusCode, "read decision response body", err)
	}

	// Reuses upstreamErrorFrom so a 429 here carries its Retry-After the same
	// way a chat call's does — the fallback/cooldown path is identical, and a
	// decision call must not be the one upstream call that ignores a cooldown.
	if httpResp.StatusCode >= 400 {
		return nil, upstreamErrorFrom(route.Provider, httpResp, respBody)
	}

	var decisionResp types.DecisionResponse
	if err := json.Unmarshal(respBody, &decisionResp); err != nil {
		return nil, arbitererrors.NewTranslationError("post_routing", "unmarshal decision response", err)
	}
	if len(decisionResp.Answers) == 0 {
		// A 200 with no answers is not a usable verdict, and it is the one
		// failure mode a caller cannot detect from a zero-valued answer — an
		// absent answer and an answered-zero look identical downstream. Fail
		// here, where the body is still in hand, so the caller falls back to
		// its configured fallback classifier instead of acting on nothing.
		return nil, arbitererrors.NewUpstreamError(route.Provider, httpResp.StatusCode,
			fmt.Sprintf("decision response carried no answers: %s", truncateForError(respBody)), nil)
	}
	decisionResp.Raw = string(respBody)
	return &decisionResp, nil
}

// truncateForError bounds how much of an unusable response body is carried in
// an error message, which ends up in a stored row and in the log. Enough to
// recognise the shape, not enough to paste a payload into the store.
func truncateForError(body []byte) string {
	const max = 200
	if len(body) <= max {
		return string(body)
	}
	return string(body[:max]) + "…"
}
