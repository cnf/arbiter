package http

import (
	"context"
	"net/http"

	"github.com/cnf/arbiter/internal/logging"
)

// ReloadFunc reloads Arbiter's configuration, returning an error if the
// config failed to load, validate, or build (in which case the previous
// configuration keeps serving). It is supplied by the caller rather than
// implemented here so the HTTP layer never owns config policy — the same
// reload the file watcher uses is the one this endpoint triggers.
type ReloadFunc func(ctx context.Context) error

// AdminHandler serves Arbiter's /admin/* surface. Requests reaching it are
// assumed already gated (see Gate); it carries no auth of its own.
type AdminHandler struct {
	logger logging.Logger
	reload ReloadFunc
}

// NewAdminHandler builds the admin surface around a reload it may trigger.
func NewAdminHandler(reload ReloadFunc, l logging.Logger) *AdminHandler {
	return &AdminHandler{logger: l, reload: reload}
}

// ReloadHandler handles POST /admin/reload: it reloads the config and reports
// the outcome. A rejected reload is a 500 — the request was well-formed, but
// Arbiter could not do what it asked, and the previous config is still
// serving (so the caller knows nothing changed).
func (h *AdminHandler) ReloadHandler(w http.ResponseWriter, r *http.Request) {
	if err := h.reload(r.Context()); err != nil {
		h.logger.LogError(r.Context(), "error", err, map[string]interface{}{"phase": "admin_reload"})
		writeError(w, http.StatusInternalServerError, "reload rejected: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"reloaded"}`))
}

// Gate wraps an /admin/* handler with the config-driven forward-auth check: a
// request lacking the configured header is rejected 401 before the inner
// handler runs. An empty header name means the gate is open.
//
// This checks only that the header is *present*; it cannot and does not
// verify who set it. It is therefore an access control only insofar as
// Arbiter's listener is reachable exclusively through a proxy that sets the
// header — the binding (loopback/unix socket/tailnet) is the actual control.
func Gate(header string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if header != "" && r.Header.Get(header) == "" {
			writeError(w, http.StatusUnauthorized, "missing "+header)
			return
		}
		next(w, r)
	}
}
