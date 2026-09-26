package ui

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// defaultWindow is the sessions index's default window. It matches the JSON
// admin surface's own default (internal/http's defaultStatsWindow) so the two
// describe "recently" the same way, without this package reaching into that one
// for an unexported constant.
const defaultWindow = 7 * 24 * time.Hour

// affinityDefaultTTL is the pin TTL routing hands out when a provider config
// doesn't set its own — see internal/pipeline. The store only ever persists
// the resulting expires_at, not the TTL that produced it (per-provider TTLs
// can differ), so this is the one place the UI has to assume a number rather
// than read it back.
const affinityDefaultTTL = 25 * time.Hour

// liveOnlyGraceWindow is how long a lane keeps showing under "live only"
// after its pin expires, instead of disappearing the instant the pin does.
// A hard cutoff at expiry would make an in-progress read (you're mid-reply,
// the tab is open) blink out from under you; 2x the default pin TTL gives
// enough slack for that without the filter drifting far from "live" as a
// word — the tradeoff explicitly asked for over an exact per-pin TTL, which
// the store doesn't retain.
const liveOnlyGraceWindow = 2 * affinityDefaultTTL

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

// sessionNodeHref is a lane node's "open" link: every node — a plain client
// request, a satellite (classifier/title) call, or a folded stack of
// streamed turns — opens the SAME destination, the session's own transcript
// page, landed on the turn that node belongs to. There used to be a second
// destination (a flat, filtered requests list) for a folded run's node; that
// page was removed in #54, so this is now the only "open" link any node has
// — see SessionHandler's ?id= handling and store.SessionTurnForRequest,
// which resolves a satellite's own id to its parent client turn.
//
// head.ID is the newest row of a folded/nested group (requestLineView.Head),
// which is a real, resolvable request id in every case: a satellite's Head
// is its own row (folding only merges same-identity streamed repeats, and a
// satellite line is never a Run), and a stack's Head is one of the streamed
// requests it stands for, which SessionTurnForRequest resolves like any
// other client row.
func sessionNodeHref(sessionKey string, head requestRowView) string {
	q := url.Values{}
	q.Set("key", sessionKey)
	q.Set("id", strconv.FormatInt(head.ID, 10))
	return "/admin/ui/session?" + q.Encode()
}

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
	// uses. Drives the lane header's subtle "hot" dot: not merely "had a
	// request recently", but "the next turn, if there is one, still reuses
	// this session's prompt cache instead of re-routing from scratch."
	Active bool

	// PinExpiresAt is this session's affinity pin expiry, when it has one
	// (zero otherwise) — Active's underlying timestamp, kept alongside the
	// bool so the live-updates poller can tell the client when a lane's
	// dot is due to go dark without re-deriving it server-side per poll.
	PinExpiresAt time.Time

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

	// LiveOnly hides lanes with no live (or recently-expired, within
	// liveOnlyGraceWindow) affinity pin. Defaults to on: a query string
	// with no live_only param at all means "on", so a first visit to the
	// page opens already filtered to the sessions a next turn would still
	// route consistently for — everything else is history, not "live".
	// An explicit live_only=0 is the only way to see the unfiltered list.
	LiveOnly bool

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
		LiveOnly:   q.Get("live_only") != "0",
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

		pinExpiry, err := h.reader.SessionPinExpiry(ctx, time.Now().Add(-liveOnlyGraceWindow))
		if err != nil {
			// Degrade rather than fail: the lane list itself loaded fine, and
			// the "hot" dot / live-only filter are courtesy features on top
			// of it — losing them for one request is preferable to losing
			// the whole page. Degrading here means every lane reads as
			// "not active" and live_only=1 (the default) would show nothing;
			// that is a visible, honest failure mode, not a silent wrong one.
			h.logger.LogError(ctx, "warn", err,
				map[string]interface{}{"phase": "admin_ui_sessions_pin_expiry"})
			pinExpiry = map[string]time.Time{}
		}
		now := time.Now()

		needle := strings.ToLower(strings.TrimSpace(view.Query))
		for _, s := range sessions {
			if view.ErrorsOnly && s.Errors == 0 {
				continue
			}

			expiresAt, hasPin := pinExpiry[s.Key]
			if view.LiveOnly && !hasPin {
				// No pin at all within the grace floor already applied to
				// the query — this lane is neither live nor recently live.
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

			lane := laneRow{SessionSummary: s, ShortKey: shortSessionKey(s.Key)}
			if hasPin {
				lane.PinExpiresAt = expiresAt
				lane.Active = !expiresAt.Before(now)
			}
			lane.Preview, lane.PreviewNote = h.lanePreview(ctx, rows)

			views := make([]requestRowView, 0, len(rows))
			for _, row := range rows {
				views = append(views, requestRowView{RequestRow: row, ShortSession: lane.ShortKey})
			}
			lane.Lines = attachTraceChildren(foldRequestLines(views))
			for i := range lane.Lines {
				lane.Lines[i].OpenHref = sessionNodeHref(s.Key, lane.Lines[i].Head)
				for j := range lane.Lines[i].Children {
					lane.Lines[i].Children[j].OpenHref = sessionNodeHref(s.Key, lane.Lines[i].Children[j].Head)
				}
			}
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
