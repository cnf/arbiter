// Package http contains Arbiter's HTTP ingress: the two endpoints clients
// speak to (Anthropic-style /v1/messages, OpenAI-style /chat/completions)
// and the plumbing shared between them (trace IDs, reverse-proxy header
// handling, error mapping).
package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/pipeline"
	"github.com/cnf/arbiter/internal/translator"
	"github.com/cnf/arbiter/internal/upstream"
	arbitererrors "github.com/cnf/arbiter/pkg/errors"
)

// Handler handles incoming HTTP requests.
type Handler struct {
	runtime atomic.Pointer[Runtime]
	logger  logging.Logger
}

// Model describes a model exposed by a configured provider.
type Model struct {
	ID       string
	Provider string
}

// defaultSessionHeader is the inbound header Arbiter reads for a
// client-supplied session identifier when config sets no override —
// matching the convention used by OpenRouter/LiteLLM/Bifrost.
const defaultSessionHeader = "X-Session-Id"

// Runtime is one generation of Arbiter's configuration: the pipeline that
// executes requests and the model list /models advertises. They are published
// as a single unit so the two can never be observed disagreeing, and replaced
// whole on a config reload.
type Runtime struct {
	pipeline      *pipeline.Pipeline
	models        []Model
	sessionHeader string
}

// NewRuntime pairs a built pipeline with the model list derived from the same
// config, so a Runtime always describes one consistent configuration.
// sessionHeader is the inbound header read for session affinity; "" falls
// back to defaultSessionHeader.
func NewRuntime(p *pipeline.Pipeline, models []Model, sessionHeader string) *Runtime {
	if sessionHeader == "" {
		sessionHeader = defaultSessionHeader
	}
	return &Runtime{pipeline: p, models: models, sessionHeader: sessionHeader}
}

// NewHandler creates a new HTTP handler serving the given runtime.
func NewHandler(rt *Runtime, l logging.Logger) *Handler {
	h := &Handler{logger: l}
	h.runtime.Store(rt)
	return h
}

// Swap atomically replaces the active runtime. Requests already in flight
// loaded the previous runtime at entry and finish against it; requests that
// begin after this call see the new one. Load-then-swap is safe here because
// the pointer is only ever published in a fully built state.
func (h *Handler) Swap(rt *Runtime) {
	h.runtime.Store(rt)
}

// current returns the runtime to use for this request, read once so a reload
// mid-request cannot change the config underneath it.
func (h *Handler) current() *Runtime {
	return h.runtime.Load()
}

// MessagesHandler handles Anthropic-style /v1/messages requests.
func (h *Handler) MessagesHandler(w http.ResponseWriter, r *http.Request) {
	h.handle(w, r, "anthropic")
}

// CompletionsHandler handles OpenAI-style /chat/completions requests.
func (h *Handler) CompletionsHandler(w http.ResponseWriter, r *http.Request) {
	h.handle(w, r, "openai")
}

// handle is the shared body for both endpoints: read, run the pipeline,
// write. The only difference between the two ingress points is which wire
// format they declare up front, so Execute never has to sniff it.
func (h *Handler) handle(w http.ResponseWriter, r *http.Request, format string) {
	ctx := pipeline.WithHeaders(r.Context(), captureHeaders(r.Header))
	traceID := traceIDFor(r)

	// Read the runtime once, at request entry: a reload during this request
	// must not swap the pipeline mid-flight.
	rt := h.current()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer func() {
		if err := r.Body.Close(); err != nil {
			h.logger.LogError(ctx, "warn", err, map[string]interface{}{"phase": "close_request_body"})
		}
	}()

	sessionHint := r.Header.Get(rt.sessionHeader)
	out, err := rt.pipeline.Execute(ctx, body, format, traceID, sessionHint)
	if err != nil {
		h.logger.LogError(h.logger.WithTraceID(ctx, traceID), "error", err, map[string]interface{}{
			"path":      requestPath(r),
			"client_ip": clientIP(r),
			"format":    format,
		})
		writeArbiterError(w, err)
		return
	}

	// Check if this is a streaming response
	if streamResp, ok := out.(*upstream.StreamResponse); ok {
		h.handleStream(ctx, w, traceID, streamResp, format)
		return
	}

	// Non-streaming response
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Arbiter-Trace-Id", traceID)
	if err := json.NewEncoder(w).Encode(out); err != nil {
		h.logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "encode_response"})
	}
}

// handleStream writes SSE events from the upstream to the client, translating
// them to the client's requested format (Anthropic or OpenAI).
func (h *Handler) handleStream(ctx context.Context, w http.ResponseWriter, traceID string, streamResp *upstream.StreamResponse, format string) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Arbiter-Trace-Id", traceID)

	flusher, ok := w.(http.Flusher)
	if !ok {
		h.logger.LogError(ctx, "error", fmt.Errorf("http response doesn't support flushing"), map[string]interface{}{})
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Streamed event IDs share the request's trace ID ("chatcmpl-<traceID>"),
	// matching the non-streaming path where the response ID is built from
	// resp.TraceID — lets a client correlate a stream with Arbiter's logs.
	// created is fixed per response too, as OpenAI clients expect all chunks
	// of one completion to carry the same timestamp.
	messageID := traceID
	created := time.Now().Unix()
	for evt := range streamResp.EventChan {
		var wireEvent interface{}

		if format == "anthropic" {
			wireEvent = translator.NormalizedToAnthropicStreamEvent(evt)
		} else {
			wireEvent = translator.NormalizedToOpenAIStreamEvent(evt, messageID, created)
		}

		// Serialize to JSON
		eventJSON, err := json.Marshal(wireEvent)
		if err != nil {
			h.logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "marshal_stream_event"})
			break
		}

		// Write SSE event
		if _, err := fmt.Fprintf(w, "data: %s\n\n", string(eventJSON)); err != nil {
			h.logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "write_stream_event"})
			break
		}
		flusher.Flush()
	}

	// Signal end of stream
	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		h.logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "write_stream_done"})
	}
	flusher.Flush()
}

// traceIDFor returns the trace ID to use for this request: an
// upstream-supplied one (if a reverse proxy or client set X-Trace-Id),
// otherwise a freshly generated UUID. Honoring an inbound trace ID lets a
// caller (e.g. a client that generated one before hitting Caddy) correlate
// its own logs with Arbiter's.
func traceIDFor(r *http.Request) string {
	if id := r.Header.Get("X-Trace-Id"); id != "" {
		return id
	}
	return uuid.NewString()
}

// clientIP returns the originating client's address, preferring
// X-Forwarded-For (set by Caddy/any reverse proxy in front of Arbiter) over
// RemoteAddr, which — behind a proxy — would otherwise just be the proxy's
// own address. X-Forwarded-For can carry a comma-separated chain
// (client, proxy1, proxy2, ...); the first entry is the original client.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	return r.RemoteAddr
}

// requestPath returns the path clients used to reach this request,
// accounting for X-Forwarded-Prefix: when Arbiter is reverse-proxied under
// a subpath (e.g. Caddy strips "/arbiter" before forwarding), r.URL.Path is
// missing that prefix. Logging/tracing wants the path as the client saw it.
func requestPath(r *http.Request) string {
	prefix := r.Header.Get("X-Forwarded-Prefix")
	if prefix == "" {
		return r.URL.Path
	}
	return strings.TrimRight(prefix, "/") + r.URL.Path
}

// sensitiveHeaderSubstrings marks a header for redaction if its lowercased
// name contains any of these. Substring, not an exact deny-list: a future
// per-client API key header (REQUIREMENTS §4) is exactly the kind of thing
// this must catch without needing its name enumerated here first.
var sensitiveHeaderSubstrings = []string{
	"authorization", "cookie", "token", "secret", "api-key", "apikey",
	"password", "credential",
}

// captureHeaders converts the inbound header set into the flat map the event
// store records, masking anything that looks like a credential — this is a
// single-operator tool (REQUIREMENTS.md), so the concern is not exposing
// headers to the operator, only not persisting secrets into the db file.
// Multi-value headers are joined with ", "; Arbiter has no header it expects
// to see twice, so collapsing is simpler than modeling http.Header's list
// shape all the way into storage.
func captureHeaders(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for name, values := range h {
		v := strings.Join(values, ", ")
		if isSensitiveHeader(name) {
			v = "[REDACTED]"
		}
		out[name] = v
	}
	return out
}

func isSensitiveHeader(name string) bool {
	lower := strings.ToLower(name)
	for _, s := range sensitiveHeaderSubstrings {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// writeError writes a plain JSON error body.
func writeError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// writeArbiterError maps a pipeline error to an HTTP status code.
// GuardrailError and UpstreamError carry their own status codes (e.g. a
// rate-limit guardrail returns 429, a 5xx from upstream is passed through);
// everything else -> 500, since routing/translation/classification
// failures are Arbiter's own bugs or misconfiguration, not client error.
func writeArbiterError(w http.ResponseWriter, err error) {
	var guardrailErr *arbitererrors.GuardrailError
	var upstreamErr *arbitererrors.UpstreamError
	var translationErr *arbitererrors.TranslationError
	var unknownModelErr *arbitererrors.UnknownModelError

	switch {
	case errors.As(err, &guardrailErr):
		writeError(w, guardrailErr.StatusCode, guardrailErr.Message)
	case errors.As(err, &unknownModelErr):
		writeError(w, unknownModelErr.StatusCode, unknownModelErr.Message)
	case errors.As(err, &upstreamErr):
		status := upstreamErr.StatusCode
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		writeError(w, status, upstreamErr.Message)
	case errors.As(err, &translationErr):
		writeError(w, translationErr.StatusCode, translationErr.Message)
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
