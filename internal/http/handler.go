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
//
// It carries capability data alongside the identity because a client gates on
// it: a client that sees no capability metadata assumes a model is text-only
// and refuses to send an image before making any request at all. The values
// come from the catalog (see config.ModelCatalogEntry); a model with no catalog
// row has them all nil/empty, which means UNKNOWN rather than "none".
type Model struct {
	ID       string
	Provider string

	// InputModalities is what the model accepts. nil means unknown, and is
	// deliberately distinguishable from an empty list: it is the difference
	// between "nothing is known" and "nothing is accepted", and rendering the
	// first as the second is a confident claim derived from no data.
	InputModalities []string

	// MaxInputTokens / MaxOutputTokens are nil when unstated.
	MaxInputTokens  *int
	MaxOutputTokens *int

	// Metadata is free-form extra data carried through from the catalog.
	// Nothing interprets it.
	Metadata map[string]interface{}
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

	// An Anthropic stream is framed as `event: <type>` followed by
	// `data: {...}`, and its clients dispatch on that event NAME — both
	// official SDKs match sse.event against "message_start",
	// "content_block_delta", etc., and silently discard any frame whose event
	// is unset. Emitting only a data line produces a stream that parses as
	// valid JSON and is then thrown away whole, which is what an Anthropic
	// client saw as "didn't go through".
	isAnthropic := format == "anthropic"

	// Streamed event IDs share the request's trace ID ("chatcmpl-<traceID>"),
	// matching the non-streaming path where the response ID is built from
	// resp.TraceID — lets a client correlate a stream with Arbiter's logs.
	// created is fixed per response too, as OpenAI clients expect all chunks
	// of one completion to carry the same timestamp.
	messageID := traceID
	created := time.Now().Unix()
	for evt := range streamResp.EventChan {
		// Each translator returns a concrete pointer and returns nil for an
		// event that has no representation in the target wire format — a
		// deliberate drop, not an absence of data. The nil must be caught
		// HERE, on the concrete type: assigned into an interface{} a nil
		// *T is non-nil, so a later `wireEvent == nil` check would pass and
		// json.Marshal would write the literal `null` as a frame
		// (`data: null`) — not a valid event on either wire, and enough to
		// abort a strict client's parse mid-reply.
		var wireEvent interface{}
		var eventName string
		if isAnthropic {
			if e := translator.NormalizedToAnthropicStreamEvent(evt); e != nil {
				wireEvent = e
				eventName = e.Type
			}
		} else {
			if e := translator.NormalizedToOpenAIStreamEvent(evt, messageID, created); e != nil {
				wireEvent = e
			}
		}
		if wireEvent == nil {
			continue
		}

		// Serialize to JSON
		eventJSON, err := json.Marshal(wireEvent)
		if err != nil {
			h.logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "marshal_stream_event"})
			break
		}

		// Write SSE event, naming the event type first on the Anthropic wire.
		if eventName != "" {
			if _, err := fmt.Fprintf(w, "event: %s\n", eventName); err != nil {
				h.logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "write_stream_event"})
				break
			}
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", string(eventJSON)); err != nil {
			h.logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "write_stream_event"})
			break
		}
		flusher.Flush()
	}

	// Signal end of stream. `[DONE]` is an OpenAI convention: an Anthropic
	// client does not know it, and there is no event line to name it with,
	// so it is written on the OpenAI wire only. An Anthropic stream ends at
	// message_stop, which the translator has already emitted.
	if !isAnthropic {
		if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
			h.logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "write_stream_done"})
		}
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
// GuardrailError, StopError, and UpstreamError carry their own status codes
// (e.g. a rate-limit guardrail returns 429, an operator's `stop` rule
// returns whatever it configured, a 5xx from upstream is passed through);
// everything else -> 500, since routing/translation/classification
// failures are Arbiter's own bugs or misconfiguration, not client error.
func writeArbiterError(w http.ResponseWriter, err error) {
	var guardrailErr *arbitererrors.GuardrailError
	var stopErr *arbitererrors.StopError
	var upstreamErr *arbitererrors.UpstreamError
	var translationErr *arbitererrors.TranslationError
	var unknownModelErr *arbitererrors.UnknownModelError

	switch {
	case errors.As(err, &guardrailErr):
		writeError(w, guardrailErr.StatusCode, guardrailErr.Message)
	case errors.As(err, &stopErr):
		writeError(w, stopErr.StatusCode, stopErr.Message)
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
