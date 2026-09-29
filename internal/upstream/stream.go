package upstream

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// streamFrom builds the provider's wire request, POSTs it, and hands the
// response body to the SSE reader on a goroutine that outlives this call.
//
// The non-2xx path is handled here (before the goroutine) so a provider
// error surfaces as a return value the caller can act on — retry, fall back,
// cool the provider down — rather than as the first event on a stream that
// looks like it started fine. That distinction is the whole reason the
// status check is not left to the reader.
//
// watchdog bounds upstream silence and releases the stream context once the
// goroutine reading the SSE body has finished; the caller must not cancel
// ctx before that on success.
func (c *HTTPClient) streamFrom(ctx context.Context, route types.Route, req *types.NormalizedRequest, wf wireFormat, eventChan chan<- *types.NormalizedStreamEvent, errChan chan<- error, watchdog *streamWatchdog) error {
	httpReq, err := buildWireRequest(ctx, c.translator, route, req, wf)
	if err != nil {
		return err
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
	// readSSEStream closes the body itself, so this must not close it again.
	go func() {
		defer watchdog.stop()
		defer close(eventChan)
		errChan <- c.readSSEStream(ctx, httpResp.Body, route.Config.Type, eventChan, watchdog)
	}()

	return nil
}
