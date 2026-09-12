package http

import (
	"io"
	"net/http"

	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/pipeline"
)

// Handler handles incoming HTTP requests.
type Handler struct {
	pipeline *pipeline.Pipeline
	logger   logging.Logger
}

// NewHandler creates a new HTTP handler.
func NewHandler(p *pipeline.Pipeline, l logging.Logger) *Handler {
	return &Handler{
		pipeline: p,
		logger:   l,
	}
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// TODO: implement request handling
	// 1. Extract trace ID or generate new one
	// 2. Read request body
	// 3. Detect format
	// 4. Run pipeline
	// 5. Write response
	w.WriteHeader(http.StatusNotImplemented)
	io.WriteString(w, "Handler not yet implemented")
}

// MessagesHandler handles Anthropic-style /v1/messages requests.
func (h *Handler) MessagesHandler(w http.ResponseWriter, r *http.Request) {
	// TODO: implement
}

// CompletionsHandler handles OpenAI-style /chat/completions requests.
func (h *Handler) CompletionsHandler(w http.ResponseWriter, r *http.Request) {
	// TODO: implement
}
