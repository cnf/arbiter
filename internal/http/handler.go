// Package http contains Arbiter's HTTP ingress: the two endpoints clients
// speak to (Anthropic-style /v1/messages, OpenAI-style /chat/completions)
// and the plumbing shared between them (trace IDs, reverse-proxy header
// handling, error mapping).
package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/pipeline"
	arbitererrors "github.com/cnf/arbiter/pkg/errors"
)

// Handler handles incoming HTTP requests.
type Handler struct {
	pipeline *pipeline.Pipeline
	logger   logging.Logger
	models   []Model
}

// Model describes a model exposed by a configured provider.
type Model struct {
	ID       string
	Provider string
}

// NewHandler creates a new HTTP handler.
func NewHandler(p *pipeline.Pipeline, l logging.Logger, models []Model) *Handler {
	return &Handler{
		pipeline: p,
		logger:   l,
		models:   models,
	}
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
	ctx := r.Context()
	traceID := traceIDFor(r)

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

	out, err := h.pipeline.Execute(ctx, body, format, traceID)
	if err != nil {
		h.logger.LogError(h.logger.WithTraceID(ctx, traceID), "error", err, map[string]interface{}{
			"path":      requestPath(r),
			"client_ip": clientIP(r),
			"format":    format,
		})
		writeArbiterError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Arbiter-Trace-Id", traceID)
	if err := json.NewEncoder(w).Encode(out); err != nil {
		h.logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "encode_response"})
	}
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

	switch {
	case errors.As(err, &guardrailErr):
		writeError(w, guardrailErr.StatusCode, guardrailErr.Message)
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
