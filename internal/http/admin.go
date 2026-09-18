package http

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cnf/arbiter/internal/logging"
)

// ReloadFunc reloads Arbiter's configuration, returning an error if the
// config failed to load, validate, or build (in which case the previous
// configuration keeps serving). It is supplied by the caller rather than
// implemented here so the HTTP layer never owns config policy — the same
// reload the file watcher uses is the one this endpoint triggers.
type ReloadFunc func(ctx context.Context) error

// ClearCooldownsFunc drops every provider's 429 backoff, returning how many were
// active. Supplied by the caller for the same reason ReloadFunc is: the HTTP
// layer does not own routing state.
//
// This exists because a config reload deliberately does NOT clear cooldowns. The
// user's reasoning: a reload is what you do while fixing something, so clearing
// as a side effect of an unrelated edit re-opens a flood you were already
// backing off from. Clearing therefore wants its own deliberate action.
type ClearCooldownsFunc func() int

// AdminHandler serves Arbiter's /admin/* surface. Requests reaching it are
// assumed already gated (see Gate); it carries no auth of its own.
type AdminHandler struct {
	logger         logging.Logger
	reload         ReloadFunc
	clearCooldowns ClearCooldownsFunc
}

// NewAdminHandler builds the admin surface around a reload it may trigger.
func NewAdminHandler(reload ReloadFunc, l logging.Logger) *AdminHandler {
	return &AdminHandler{logger: l, reload: reload}
}

// SetClearCooldowns supplies the cooldown-reset action. Kept as a setter rather
// than a constructor argument so existing callers (and tests) that do not care
// about cooldowns are unaffected — the same shape SetCaptureContent uses on the
// pipeline.
func (h *AdminHandler) SetClearCooldowns(f ClearCooldownsFunc) {
	h.clearCooldowns = f
}

// ClearCooldownsHandler handles POST /admin/cooldowns/clear: it drops every
// provider's 429 backoff and reports how many were active, so the operator gets
// feedback rather than silence.
func (h *AdminHandler) ClearCooldownsHandler(w http.ResponseWriter, r *http.Request) {
	if h.clearCooldowns == nil {
		writeError(w, http.StatusServiceUnavailable, "cooldown state is not available")
		return
	}
	n := h.clearCooldowns()
	h.logger.LogError(r.Context(), "info",
		fmt.Errorf("cooldowns cleared"), map[string]interface{}{"phase": "admin_clear_cooldowns", "active": n})
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"status":"cleared","active":%d}`, n)
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
