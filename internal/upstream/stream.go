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
// The caller must close eventChan when done.
func (c *HTTPClient) readSSEStream(ctx context.Context, body io.ReadCloser, providerType string, eventChan chan<- *types.NormalizedStreamEvent) error {
	defer func() {
		_ = body.Close()
	}()

	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

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
			normalized, err = c.parseOpenAISSEEvent(data)
		default:
			return fmt.Errorf("unknown provider type %q", providerType)
		}

		if err != nil {
			// Log but don't fail on malformed events — send what we have
			continue
		}

		if normalized != nil {
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

// parseOpenAISSEEvent parses a single OpenAI SSE event line.
func (c *HTTPClient) parseOpenAISSEEvent(data string) (*types.NormalizedStreamEvent, error) {
	var evt translator.OpenAIStreamEvent
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		return nil, fmt.Errorf("unmarshal openai SSE event: %w", err)
	}
	return translator.OpenAIStreamEventToNormalized(&evt), nil
}

// sendAnthropicStream sends a streaming request to Anthropic and reads the SSE response.
func (c *HTTPClient) sendAnthropicStream(ctx context.Context, route types.Route, req *types.NormalizedRequest, eventChan chan<- *types.NormalizedStreamEvent) error {
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
		return arbitererrors.NewUpstreamError(route.Provider, httpResp.StatusCode, string(respBody), nil)
	}

	// Read the SSE stream in a goroutine and close the event channel when done
	go func() {
		defer func() { _ = httpResp.Body.Close() }()
		defer close(eventChan)
		_ = c.readSSEStream(ctx, httpResp.Body, "anthropic", eventChan)
	}()

	return nil
}

// sendOpenAIStream sends a streaming request to an OpenAI-compatible provider and reads the SSE response.
func (c *HTTPClient) sendOpenAIStream(ctx context.Context, route types.Route, req *types.NormalizedRequest, eventChan chan<- *types.NormalizedStreamEvent) error {
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
		return arbitererrors.NewUpstreamError(route.Provider, httpResp.StatusCode, string(respBody), nil)
	}

	// Read the SSE stream in a goroutine and close the event channel when done
	go func() {
		defer func() { _ = httpResp.Body.Close() }()
		defer close(eventChan)
		_ = c.readSSEStream(ctx, httpResp.Body, route.Config.Type, eventChan)
	}()

	return nil
}
