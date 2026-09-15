package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"

	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/store"
)

// defaultStatsWindow is how far back a stats query looks when the caller
// gives no ?since. Personal-scale usage cares about "recently", not a
// specific horizon, so a week is a reasonable default rather than a required
// parameter.
const defaultStatsWindow = 7 * 24 * time.Hour

// StatsHandler serves Arbiter's read-only /admin/stats/* surface: aggregates
// over the sqlite event store, for answering "what has this thing actually
// been doing" rather than tailing logs.
type StatsHandler struct {
	reader *store.Reader
	logger logging.Logger
}

// NewStatsHandler builds the stats surface around an already-open Reader.
// A nil reader means the event store is disabled (storage.path unset);
// handlers report 503 rather than panicking on a nil pointer.
func NewStatsHandler(reader *store.Reader, l logging.Logger) *StatsHandler {
	return &StatsHandler{reader: reader, logger: l}
}

// window parses the optional ?since=<Go duration> query parameter (e.g.
// "24h", "168h"), falling back to defaultStatsWindow when absent or invalid.
func window(r *http.Request) store.Window {
	if raw := r.URL.Query().Get("since"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return store.WindowFrom(d)
		}
	}
	return store.WindowFrom(defaultStatsWindow)
}

func (h *StatsHandler) writeJSON(w http.ResponseWriter, r *http.Request, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.logger.LogError(r.Context(), "error", err, map[string]interface{}{"phase": "encode_stats"})
	}
}

// OverallHandler handles GET /admin/stats: headline requests/tokens/cost/
// errors over the window.
func (h *StatsHandler) OverallHandler(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "event store disabled (storage.path unset)")
		return
	}
	stats, err := h.reader.Overall(r.Context(), window(r))
	if err != nil {
		h.logger.LogError(r.Context(), "error", err, map[string]interface{}{"phase": "admin_stats_overall"})
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	h.writeJSON(w, r, stats)
}

// ProvidersHandler handles GET /admin/stats/providers: spend and volume by
// provider/model pair, most expensive first.
func (h *StatsHandler) ProvidersHandler(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "event store disabled (storage.path unset)")
		return
	}
	stats, err := h.reader.ByProvider(r.Context(), window(r))
	if err != nil {
		h.logger.LogError(r.Context(), "error", err, map[string]interface{}{"phase": "admin_stats_providers"})
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	h.writeJSON(w, r, stats)
}

// EpochsHandler handles GET /admin/stats/epochs: spend and volume by config
// epoch, so a config change's cost impact is visible without cross-
// referencing timestamps by hand.
func (h *StatsHandler) EpochsHandler(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "event store disabled (storage.path unset)")
		return
	}
	stats, err := h.reader.ByEpoch(r.Context(), window(r))
	if err != nil {
		h.logger.LogError(r.Context(), "error", err, map[string]interface{}{"phase": "admin_stats_epochs"})
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	h.writeJSON(w, r, stats)
}

// ToolsHandler handles GET /admin/stats/tools: tool-name usage counts over
// the window.
func (h *StatsHandler) ToolsHandler(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "event store disabled (storage.path unset)")
		return
	}
	stats, err := h.reader.Tools(r.Context(), window(r))
	if err != nil {
		h.logger.LogError(r.Context(), "error", err, map[string]interface{}{"phase": "admin_stats_tools"})
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	h.writeJSON(w, r, stats)
}

// RequestsHandler handles GET /admin/requests: the request list, newest first.
// This is the read the aggregate /admin/stats* endpoints cannot serve — a UI
// needs the rows themselves, not just their sums.
//
// Query parameters, all optional: since, provider, session, alias, status,
// errors (presence = only status >= 400), limit.
func (h *StatsHandler) RequestsHandler(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "event store disabled (storage.path unset)")
		return
	}
	q := r.URL.Query()

	f := store.RequestFilter{
		Since:      window(r).Since,
		Provider:   q.Get("provider"),
		SessionKey: q.Get("session"),
		Alias:      q.Get("alias"),
		ErrorsOnly: q.Has("errors"),
	}
	if raw := q.Get("status"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 100 || n > 599 {
			writeError(w, http.StatusBadRequest, "status must be an HTTP status code (100-599)")
			return
		}
		f.StatusCode = n
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		f.Limit = n // the reader clamps to its own maximum
	}

	rows, err := h.reader.ListRequests(r.Context(), f)
	if err != nil {
		h.logger.LogError(r.Context(), "error", err, map[string]interface{}{"phase": "admin_requests"})
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	h.writeJSON(w, r, rows)
}

// RequestHandler handles GET /admin/requests/{id}: one request in full. The id
// is the store's rowid, which the list returns as `id`.
func (h *StatsHandler) RequestHandler(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "event store disabled (storage.path unset)")
		return
	}
	raw := mux.Vars(r)["id"]
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "request id must be a positive integer")
		return
	}

	detail, ok, err := h.reader.GetRequest(r.Context(), id)
	if err != nil {
		h.logger.LogError(r.Context(), "error", err, map[string]interface{}{"phase": "admin_request"})
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "no request with that id")
		return
	}
	h.writeJSON(w, r, detail)
}

// SessionHandler handles GET /admin/stats/session?key=<key>&limit=<n>: one
// session's requests in order, so a specific conversation's routing and cost
// can be inspected. limit defaults to 100 and is capped at 1000.
func (h *StatsHandler) SessionHandler(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "event store disabled (storage.path unset)")
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing required query parameter: key")
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 1000 {
		limit = 1000
	}

	rows, err := h.reader.Session(r.Context(), key, limit)
	if err != nil {
		h.logger.LogError(r.Context(), "error", err, map[string]interface{}{"phase": "admin_stats_session"})
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	h.writeJSON(w, r, rows)
}
