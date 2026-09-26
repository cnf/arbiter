package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// defaultWindow is the sessions index's default window. It matches the JSON
// admin surface's own default (internal/http's defaultStatsWindow) so the two
// describe "recently" the same way, without this package reaching into that one
// for an unexported constant.
const defaultWindow = 7 * 24 * time.Hour

// sessionKeyDisplayLen is how much of a session key the UI shows.
//
// A content-derived key is 64 hex characters (a sha256 of the client's system
// prompt plus its first user turn), and a header-supplied one can be anything.
// Neither is meant to be read character by character — it is an opaque handle —
// so rendering it in full turns every table column and every link into a wall
// of hex that pushes the columns a reader actually wants off screen. The full
// value is still what goes in the href, the fetch and the query: truncation is
// display-only, and the tooltip carries the whole thing.
const sessionKeyDisplayLen = 10

// shortSessionKey renders a session key for display, keeping the full value
// available to the caller for links. It is a display helper and nothing else —
// it must never be used to build a query or a href, which is why the templates
// take it as a separate field rather than filtering the value in place.
func shortSessionKey(key string) string {
	if len(key) <= sessionKeyDisplayLen {
		return key
	}
	return key[:sessionKeyDisplayLen]
}

// previewBytes caps how much of a session's opening message the lane header
// shows — a one-line preview, not a transcript excerpt (the full text is a
// click away, on the session's own transcript page).
const previewBytes = 200

// noClientBodyPreview stands in for a session whose earliest client row has
// no captured request body — a pre-guardrail rejection with content capture
// on (#5: "nothing invisible", so the row itself still exists), content
// capture off entirely, or a client row whose only content was a system
// preamble with no user turn. All three read identically from here: there is
// nothing to preview, and the reason is a capture-page question, not a
// lanes-page one.
const noClientBodyPreview = "(no client body captured)"

// laneRow is one conversation in the sessions lane view: the aggregate,
// its already-folded/nested request lines (the exact computation the flat
// requests page uses — see foldRequestLines/attachTraceChildren), and the
// display-only bits the lane header needs.
type laneRow struct {
	store.SessionSummary
	ShortKey string

	// Active is true when this session has a live affinity pin — the same
	// "still within cache TTL" definition the nav bar's "N active" stat
	// uses (store.ActiveSessionCount / store.ActiveSessionKeys). Drives the
	// lane header's subtle "hot" dot: not merely "had a request recently",
	// but "the next turn, if there is one, still reuses this session's
	// prompt cache instead of re-routing from scratch."
	Active bool

	// Lines is this session's requests, folded and nested exactly as the
	// requests page computes them: a streamed run collapses into one Run
	// line (the lane's "stack" node), and a classifier call nests under the
	// client line sharing its trace_id (a "satellite" node on that line's
	// stem). No new query or grouping logic — this is the same
	// requestLineView tree, rendered as a timeline instead of table rows.
	Lines []requestLineView

	// SatelliteCount totals every nested (non-top-level) line across Lines,
	// for the lane header's "N satellites" count. Only one nesting level
	// exists today (attachTraceChildren does not recurse), so this is a
	// flat sum of each top-level line's Children.
	SatelliteCount int

	// Preview is the opening client message of the session (its earliest
	// kind="client" row's first user-role request block), truncated to
	// previewBytes. Empty when there is nothing to show — see
	// PreviewNote for why.
	Preview string

	// PreviewNote explains an empty Preview: capture is off, the row's
	// content was never captured (a guardrail rejection before capture),
	// or the session's earliest row carried no user-role block at all.
	// Rendered in place of Preview so an empty lane header reads as
	// "nothing to show and here is why", not as a blank cell.
	PreviewNote string
}

// sessionsView is the sessions lane page.
type sessionsView struct {
	viewBase

	Rows []laneRow

	// SessionlessCount is how many requests in the window have no session key.
	// It gets its own row rather than being folded into the list, because those
	// requests are not one conversation — but hiding them entirely would lose
	// real traffic (a conversation that opens with too little text is never
	// pinned, on any of its turns).
	SessionlessCount int64

	SinceRaw string
	Since    time.Duration

	// Capped says the index hit its limit, so a truncated list is not read as
	// the whole picture.
	Capped bool
	Limit  int

	// Filter state, echoed back into the toolbar — the same "the address bar
	// is the view" convention the requests page's filter form uses.
	Query      string
	ErrorsOnly bool
	ClientOnly bool

	// InView totals the lanes actually rendered (post-filter), for the
	// detail panel's default "in view" stat grid.
	InViewSessions int
	InViewRequests int64
	InViewErrors   int64
	InViewCostUSD  float64
}

// sessionsHandler handles GET /admin/ui/sessions: one lane per conversation,
// most recently active first, its requests (and the classifier calls they
// triggered) laid out as a timeline instead of cross-referenced against a
// separate requests list.
func (h *Handler) SessionsHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	q := r.URL.Query()

	since := defaultWindow
	if raw := q.Get("since"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			h.fail(w, r, http.StatusBadRequest,
				`since must be a positive Go duration, e.g. "24h" or "168h"`)
			return
		}
		since = d
	}

	view := sessionsView{
		viewBase:   h.base(r.Context(), "Sessions"),
		SinceRaw:   q.Get("since"),
		Since:      since,
		Limit:      store.MaxSessionListLimit,
		Query:      q.Get("q"),
		ErrorsOnly: q.Get("errors") == "1",
		ClientOnly: q.Has("client_only"),
	}
	view.Title = "sessions"

	if h.reader != nil {
		ctx := r.Context()
		w0 := store.Window{Since: time.Now().UTC().Add(-since)}
		sessions, err := h.reader.Sessions(ctx, w0, view.Limit)
		if err != nil {
			h.logger.LogError(ctx, "error", err,
				map[string]interface{}{"phase": "admin_ui_sessions"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
		view.Capped = len(sessions) == view.Limit

		activeKeys, err := h.reader.ActiveSessionKeys(ctx)
		if err != nil {
			// Degrade rather than fail: the lane list itself loaded fine, and
			// the "hot" dot is a courtesy annotation on top of it — losing it
			// for one request is preferable to losing the whole page.
			h.logger.LogError(ctx, "warn", err,
				map[string]interface{}{"phase": "admin_ui_sessions_active_keys"})
			activeKeys = map[string]bool{}
		}

		needle := strings.ToLower(strings.TrimSpace(view.Query))
		for _, s := range sessions {
			if view.ErrorsOnly && s.Errors == 0 {
				continue
			}

			kindFilter := ""
			if view.ClientOnly {
				kindFilter = "client"
			}
			rows, err := h.reader.ListRequests(ctx, store.RequestFilter{
				SessionKey: s.Key,
				Since:      w0.Since,
				Limit:      maxRequestRows,
				Kind:       kindFilter,
			})
			if err != nil {
				h.logger.LogError(ctx, "error", err,
					map[string]interface{}{"phase": "admin_ui_sessions_lane", "session": s.Key})
				h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
				return
			}

			lane := laneRow{SessionSummary: s, ShortKey: shortSessionKey(s.Key), Active: activeKeys[s.Key]}
			lane.Preview, lane.PreviewNote = h.lanePreview(ctx, rows)

			views := make([]requestRowView, 0, len(rows))
			for _, row := range rows {
				views = append(views, requestRowView{RequestRow: row, ShortSession: lane.ShortKey})
			}
			lane.Lines = attachTraceChildren(foldRequestLines(views))
			for _, line := range lane.Lines {
				lane.SatelliteCount += len(line.Children)
			}

			if needle != "" {
				haystack := strings.ToLower(s.Key + " " + s.Providers + " " + lane.Preview)
				if !strings.Contains(haystack, needle) {
					continue
				}
			}

			view.InViewSessions++
			view.InViewRequests += s.Turns
			view.InViewErrors += s.Errors
			view.InViewCostUSD += s.CostUSD
			view.Rows = append(view.Rows, lane)
		}

		n, err := h.reader.SessionlessRequestCount(ctx, w0)
		if err != nil {
			// Degrade rather than fail: the sessions themselves loaded, and the
			// sessionless row is a courtesy. Its absence is visible (the row is
			// simply not there), not silently wrong.
			h.logger.LogError(ctx, "warn", err,
				map[string]interface{}{"phase": "admin_ui_sessions_sessionless"})
		}
		view.SessionlessCount = n
	}

	h.render(w, r, "sessions", "session-rows", view)
}

// lanePreview finds a lane's opening message: the earliest kind="client" row
// among rows (which ListRequests returns newest-first, so the oldest client
// row is the last one seen), and the first user-role request block it
// captured.
//
// This is a per-lane content fetch, not a new aggregate query — the same
// N+1 shape the requests page already accepts for attachTitleChildren's
// per-line ParentSessionForTitle call. Single-user, single-digit-concurrency
// scale (per the deployment this UI serves) makes that a non-issue here.
func (h *Handler) lanePreview(ctx context.Context, rows []store.RequestRow) (preview, note string) {
	if !h.captureContent.Load() {
		return "", "content capture is off — set storage.capture_content to preview messages"
	}
	var earliest *store.RequestRow
	for i := range rows {
		if rows[i].Kind == "client" {
			earliest = &rows[i]
		}
	}
	if earliest == nil {
		return "", noClientBodyPreview
	}
	blocks, _, err := h.reader.ContentForRequest(ctx, earliest.ID, false)
	if err != nil {
		h.logger.LogError(ctx, "warn", err,
			map[string]interface{}{"phase": "admin_ui_sessions_preview", "request_id": earliest.ID})
		return "", noClientBodyPreview
	}
	for _, b := range blocks {
		if b.Direction == "request" && b.Role == "user" && b.Captured {
			body := b.Body
			if len(body) > previewBytes {
				body = truncBody(body[:min(len(body), previewBytes+1)])
			}
			return strings.TrimSpace(body), ""
		}
	}
	return "", noClientBodyPreview
}

// requestsForSession links to the flat request list filtered to one session —
// the request-level view of the same conversation, for when the transcript is
// not what you want.
func requestsForSession(key string) string {
	return "/admin/ui/requests?session=" + url.QueryEscape(key)
}
