package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// writeJSON writes a JSON body, and writeJSONError is the failure half of the
// same contract.
//
// These live here because the Sessions tail is their only consumer: they
// arrived with the old Overview's chart endpoint (series.json) and the old
// flat-request tail, both deleted in the #54/#56/#57 rip-outs. The tail is
// fetched by script rather than by htmx, so both its successes and its
// failures must be JSON while every page's are HTML.
func (h *Handler) writeJSON(w http.ResponseWriter, r *http.Request, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.logger.LogError(r.Context(), "warn", err,
			map[string]interface{}{"phase": "admin_ui_tail_encode"})
	}
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// sessionTailLane is one lane in a tail response: its session key (so the
// client can find the .lane-row to replace) and its freshly rendered markup.
type sessionTailLane struct {
	Key  string `json:"key"`
	HTML string `json:"html"`
}

// sessionTailResponse is what SessionsTailHandler returns.
//
// It carries no cursor, unlike a request-list tail would: a lane's
// "newness" is not what a poll is answering here. A pinned session's own
// recency (its own MAX(ts)) already appears inside its re-rendered lane, so
// there is nothing this response needs to remember between polls.
type sessionTailResponse struct {
	// Lanes is every currently-*active* session's freshly rendered lane —
	// see SessionsTailHandler for what "active" means here and why an
	// inactive lane is never included. The client matches each one to the
	// .lane-row already on screen by Key: it swaps it in place if the markup
	// changed, leaves it alone if it did not, and — for a session that went
	// live after the page loaded — inserts it, which is why the rendered lane
	// carries its own data-last-seen (see laneRow.html). A lane already on
	// screen that is NOT in this list is deliberately left standing: whether
	// it still belongs depends on the page's filters and window, which only
	// the server knows, so removal is left to the next load or filter change.
	// The list is unordered — the tail walks a map of active pins, and the
	// client positions an inserted lane itself, so no sort is needed here.
	Lanes []sessionTailLane `json:"lanes"`

	// ActiveCount is the nav bar's "N active" Sessions stat, refreshed on
	// every poll alongside the lanes — see Handler.base's own ActiveSessionCount
	// call, which this mirrors so the two paths cannot report different
	// numbers for the same instant.
	ActiveCount int64 `json:"active_count"`

	// Error carries a query failure in the body rather than as an HTTP
	// status: a poll fails, retries a few seconds later, and a 500 per
	// poll would spam the console without telling the page anything a
	// body field does not.
	Error string `json:"error,omitempty"`
}

// SessionsTailHandler serves GET /admin/ui/sessions/tail: a fresh render of
// every currently *active* lane (session with a live affinity pin — not
// merely one inside the "live only" filter's grace window; see
// sessionsView.LiveOnly), plus the nav bar's active count.
//
// Polled by laneLive.js, not htmx, for the same reasons live.js exists
// instead of `hx-trigger="every 5s"` on the request list: it must not poll a
// hidden tab, and it must not treat a query failure as a page-breaking error.
//
// It deliberately rebuilds only active lanes rather than every lane on
// screen: an inactive session's next turn, if there is one, will not reuse
// this session's pinned route anyway, so nothing about its lane is expected
// to change between polls — refreshing it every 5 seconds would be pure
// query load for a lane that is, by definition, not moving. A session whose
// pin expires between polls simply stops being refreshed; its lane goes
// stale until the next full page load, the same trade-off the "live only"
// filter's grace window already accepts.
//
// The query string is the same one the page itself was loaded with (since,
// q, errors, client_only) — laneLive.js echoes location.search verbatim —
// so a lane rebuilt here matches the filters the page is actually showing
// under, and a lane that would not pass the current search/errors filter is
// left out rather than reappearing out of nowhere.
func (h *Handler) SessionsTailHandler(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeJSONError(w, http.StatusServiceUnavailable,
			"the event store is disabled: set storage.path to record and follow requests")
		return
	}
	q := r.URL.Query()
	ctx := r.Context()

	since := defaultWindow
	if raw := q.Get("since"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			writeJSONError(w, http.StatusBadRequest,
				`since must be a positive Go duration, e.g. "24h" or "168h"`)
			return
		}
		since = d
	}
	errorsOnly := q.Get("errors") == "1"
	clientOnly := q.Has("client_only")
	needle := strings.ToLower(strings.TrimSpace(q.Get("q")))

	now := time.Now()
	// since=now, not the liveOnlyGraceWindow floor: this is the "still
	// routing-pinned right now" set, the same definition ActiveSessionCount
	// and the lane header's hot dot use — a lane merely lingering in the
	// live-only filter's grace window is history, and history does not need
	// a refresh.
	activePins, err := h.reader.SessionPinExpiry(ctx, now)
	if err != nil {
		h.logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "admin_ui_sessions_tail"})
		h.writeJSON(w, r, sessionTailResponse{Error: "query failed: " + err.Error()})
		return
	}

	sinceTime := now.UTC().Add(-since)
	resp := sessionTailResponse{ActiveCount: int64(len(activePins))}
	for key := range activePins {
		s, ok, err := h.reader.SessionSummaryFor(ctx, key, sinceTime)
		if err != nil {
			h.logger.LogError(ctx, "warn", err,
				map[string]interface{}{"phase": "admin_ui_sessions_tail_lane", "session": key})
			continue
		}
		if !ok {
			// The pin outlived the window, or every row it names is
			// non-client — either way there is no lane on screen for it.
			continue
		}
		if errorsOnly && s.Errors == 0 {
			continue
		}

		lane, err := h.buildLane(ctx, s, activePins, now, sinceTime, clientOnly)
		if err != nil {
			h.logger.LogError(ctx, "warn", err,
				map[string]interface{}{"phase": "admin_ui_sessions_tail_lane", "session": key})
			continue
		}

		if needle != "" {
			haystack := strings.ToLower(s.Key + " " + s.Providers + " " + lane.Preview)
			if !strings.Contains(haystack, needle) {
				continue
			}
		}

		resp.Lanes = append(resp.Lanes, sessionTailLane{
			Key:  s.Key,
			HTML: renderLaneRow(ctx, h, lane),
		})
	}
	h.writeJSON(w, r, resp)
}

// renderLaneRow renders one lane through the same "lane-row" partial the page
// itself uses, so a polled lane and a page-loaded lane cannot drift apart in
// markup. A render failure yields an empty string, which the client already
// treats as "nothing to swap".
func renderLaneRow(ctx context.Context, h *Handler, lane laneRow) string {
	set, ok := h.fragments["fragments"]
	if !ok {
		return ""
	}
	var buf strings.Builder
	if err := set.ExecuteTemplate(&buf, "lane-row", lane); err != nil {
		h.logger.LogError(ctx, "error", err,
			map[string]interface{}{"phase": "admin_ui_sessions_tail_render"})
		return ""
	}
	return buf.String()
}
