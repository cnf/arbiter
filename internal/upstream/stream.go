package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cnf/arbiter/internal/translator"
	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// readSSEStream reads SSE events from a response body and sends them to eventChan.
// It handles both Anthropic and OpenAI SSE formats, translating to normalized events.
// The caller must close eventChan when done. Every line read — including SSE
// comments and keepalives — resets the watchdog's idle countdown, so only a
// genuinely silent upstream trips it.
func (c *HTTPClient) readSSEStream(ctx context.Context, body io.ReadCloser, providerType string, eventChan chan<- *types.NormalizedStreamEvent, watchdog *streamWatchdog) error {
	defer func() {
		_ = body.Close()
	}()

	scanner := bufio.NewScanner(body)
	// finished records that a terminal event has been emitted. It is threaded
	// into the OpenAI parser so the trailing usage/role chunk cannot be
	// mistaken for a second message_start after the stop.
	finished := false
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		watchdog.keepalive()

		line := scanner.Text()
		if line == "" {
			// Empty line signals end of event
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			// Ignore other SSE fields (event type, id, retry, etc.)
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			// OpenAI sends this to signal end of stream
			continue
		}

		// Parse and translate the event
		var normalized *types.NormalizedStreamEvent
		var err error

		switch providerType {
		case "anthropic":
			normalized, err = c.parseAnthropicSSEEvent(data)
		case "openai", "ollama":
			normalized, err = c.parseOpenAISSEEvent(data, finished)
		default:
			return fmt.Errorf("unknown provider type %q", providerType)
		}

		if err != nil {
			// Log but don't fail on malformed events — send what we have
			continue
		}

		if normalized != nil {
			if normalized.Type == "message_stop" {
				finished = true
			}
			select {
			case eventChan <- normalized:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read SSE stream: %w", err)
	}

	return nil
}

// parseAnthropicSSEEvent parses a single Anthropic SSE event line.
func (c *HTTPClient) parseAnthropicSSEEvent(data string) (*types.NormalizedStreamEvent, error) {
	var evt translator.AnthropicStreamEvent
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		return nil, fmt.Errorf("unmarshal anthropic SSE event: %w", err)
	}
	return translator.AnthropicStreamEventToNormalized(&evt), nil
}

// parseOpenAISSEEvent parses a single OpenAI SSE event line. finished reports
// whether a terminal event has already been emitted on this stream; it gates
// the message_start derivation so the trailing usage/role chunk cannot produce
// a start after the stop.
func (c *HTTPClient) parseOpenAISSEEvent(data string, finished bool) (*types.NormalizedStreamEvent, error) {
	var evt translator.OpenAIStreamEvent
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		return nil, fmt.Errorf("unmarshal openai SSE event: %w", err)
	}
	return translator.OpenAIStreamEventToNormalized(&evt, finished), nil
}

// sendAnthropicStream sends a streaming request to Anthropic and reads the SSE response.
// watchdog bounds upstream silence and releases the stream context once the
// goroutine reading the SSE body has finished; the caller must not cancel
// ctx before that on success.
func (c *HTTPClient) sendAnthropicStream(ctx context.Context, route types.Route, req *types.NormalizedRequest, eventChan chan<- *types.NormalizedStreamEvent, errChan chan<- error, watchdog *streamWatchdog) error {
	wireReq, err := c.translator.NormalizedToAnthropicRequest(req)
	if err != nil {
		return arbitererrors.NewTranslationError("post_routing", "normalized to anthropic request", err)
	}

	body, err := json.Marshal(wireReq)
	if err != nil {
		return arbitererrors.NewTranslationError("post_routing", "marshal anthropic request", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, route.Config.Endpoint+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return arbitererrors.NewUpstreamError(route.Provider, 0, "build request", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", route.Config.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	// The client's own beta opt-ins, forwarded. Copied before the provider
	// Headers loop so a static config value cannot silently win over what the
	// client negotiated — a static beta on one route must not mask the
	// client's, and vice versa the client's must reach upstream.
	if req.ClientBeta != "" {
		httpReq.Header.Set("anthropic-beta", req.ClientBeta)
	}
	for k, v := range route.Config.Headers {
		httpReq.Header.Set(k, v)
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return arbitererrors.NewUpstreamError(route.Provider, 0, "request failed", err)
	}

	if httpResp.StatusCode >= 400 {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return arbitererrors.NewUpstreamError(route.Provider, httpResp.StatusCode, "read error response", err)
		}
		return upstreamErrorFrom(route.Provider, httpResp, respBody)
	}

	// Read the SSE stream in a goroutine and close the event channel when done.
	// errChan gets the terminal outcome after eventChan is closed, so a
	// consumer that drains events-then-error never races the signal.
	go func() {
		defer watchdog.stop()
		defer func() { _ = httpResp.Body.Close() }()
		defer close(eventChan)
		err := c.readSSEStream(ctx, httpResp.Body, "anthropic", eventChan, watchdog)
		errChan <- err
	}()

	return nil
}

// sendOpenAIStream sends a streaming request to an OpenAI-compatible provider and reads the SSE response.
// watchdog bounds upstream silence and releases the stream context once the
// goroutine reading the SSE body has finished; the caller must not cancel
// ctx before that on success.
func (c *HTTPClient) sendOpenAIStream(ctx context.Context, route types.Route, req *types.NormalizedRequest, eventChan chan<- *types.NormalizedStreamEvent, errChan chan<- error, watchdog *streamWatchdog) error {
	wireReq, err := c.translator.NormalizedToOpenAIRequest(req)
	if err != nil {
		return arbitererrors.NewTranslationError("post_routing", "normalized to openai request", err)
	}

	body, err := json.Marshal(wireReq)
	if err != nil {
		return arbitererrors.NewTranslationError("post_routing", "marshal openai request", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, route.Config.Endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return arbitererrors.NewUpstreamError(route.Provider, 0, "build request", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+route.Config.APIKey)
	for k, v := range route.Config.Headers {
		httpReq.Header.Set(k, v)
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return arbitererrors.NewUpstreamError(route.Provider, 0, "request failed", err)
	}

	if httpResp.StatusCode >= 400 {
		defer func() { _ = httpResp.Body.Close() }()
		respBody, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return arbitererrors.NewUpstreamError(route.Provider, httpResp.StatusCode, "read error response", err)
		}
		return upstreamErrorFrom(route.Provider, httpResp, respBody)
	}

	// Read the SSE stream in a goroutine and close the event channel when
	// done. errChan gets the terminal outcome after eventChan is closed.
	go func() {
		defer watchdog.stop()
		defer func() { _ = httpResp.Body.Close() }()
		defer close(eventChan)
		err := c.readSSEStream(ctx, httpResp.Body, route.Config.Type, eventChan, watchdog)
		errChan <- err
	}()

	return nil
}
