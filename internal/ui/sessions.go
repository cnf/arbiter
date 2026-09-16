package ui

import (
	"net/http"
	"net/url"
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

// sessionRow is one conversation in the sessions index: the aggregate plus the
// display-only shortening of its key.
type sessionRow struct {
	store.SessionSummary
	ShortKey string
}

// sessionsView is the sessions index page.
type sessionsView struct {
	viewBase

	Rows []sessionRow

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
}

// sessionsHandler handles GET /admin/ui/sessions: one row per conversation,
// most recently active first.
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
		viewBase: h.base("Sessions"),
		SinceRaw: q.Get("since"),
		Since:    since,
		Limit:    store.MaxSessionListLimit,
	}
	view.Title = "sessions"

	if h.reader != nil {
		w0 := store.Window{Since: time.Now().UTC().Add(-since)}
		rows, err := h.reader.Sessions(r.Context(), w0, view.Limit)
		if err != nil {
			h.logger.LogError(r.Context(), "error", err,
				map[string]interface{}{"phase": "admin_ui_sessions"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
		for _, s := range rows {
			view.Rows = append(view.Rows, sessionRow{SessionSummary: s, ShortKey: shortSessionKey(s.Key)})
		}
		view.Capped = len(rows) == view.Limit

		n, err := h.reader.SessionlessRequestCount(r.Context(), w0)
		if err != nil {
			// Degrade rather than fail: the sessions themselves loaded, and the
			// sessionless row is a courtesy. Its absence is visible (the row is
			// simply not there), not silently wrong.
			h.logger.LogError(r.Context(), "warn", err,
				map[string]interface{}{"phase": "admin_ui_sessions_sessionless"})
		}
		view.SessionlessCount = n
	}

	h.render(w, r, "sessions", "session-rows", view)
}

// transcriptBlock is one block of one turn's content, with its hash carried
// separately so turns can be compared against each other.
//
// The stored hash is also what makes the "already sent" split exact rather than
// heuristic: a client re-sends its whole conversation every turn, so a block
// whose hash appeared in an earlier turn of this same session is, byte for byte,
// the same text — not merely similar.
type transcriptBlock struct {
	store.ContentBlock
	RequestID int64
	turnIndex int
}

// transcriptTurn is one request/response exchange in a conversation.
//
// It distinguishes what the turn *introduced* from what it merely re-sent. A
// conversation read turn by turn shows each prompt next to the answer it
// produced; showing the replayed system prompt and history inline at every turn
// instead makes the page a multiple of the conversation rather than a record of
// it. The distinction comes from the content hashes, so it needs no guessing.
type transcriptTurn struct {
	Request store.SessionRequest
	Index   int

	// New is what this turn added: every request-side block that was never
	// captured before in this session, plus the whole response. It is what a
	// reader actually wants — the new question and its answer.
	New []transcriptBlock

	// Preamble is this turn's system-role blocks, and it is populated only for
	// the turn that *introduced* them — normally the first. It is rendered as
	// its own collapsible field, because a client's standing instructions are
	// worth having separately from the conversation and are usually the largest
	// single thing in a turn.
	Preamble []transcriptBlock

	// CaptureOff is true when there are no blocks at all. It is rendered as its
	// own message because "nothing was captured" and "this turn had no content
	// to capture" are different answers, and the store keeps them apart by
	// whether any block rows exist.
	CaptureOff bool

	// The three sizes, so a turn can show how much it introduced, how much was
	// standing instructions, and how much it merely repeated.
	NewChars      int
	PreambleChars int
	ReplayChars   int
}

// sessionView is the conversation transcript page.
type sessionView struct {
	viewBase

	Key      string
	ShortKey string
	Turns    []transcriptTurn

	// Totals over the turns rendered (not the whole session, which is
	// unbounded here — see store.Sessions on the windowed index).
	TotalCost   float64
	TotalInput  int64
	TotalOutput int64
	Capped      bool

	// EmptyKey distinguishes "no such session" from "a session with no turns
	// in this window", which are different answers for a reader who followed a
	// link from a request.
	Found bool
}

// sessionHandler handles GET /admin/ui/session?key=…: one conversation's turns
// in order, each with its captured content.
//
// The key is a query parameter rather than a path segment because session keys
// are opaque and may be arbitrary client-supplied header values — a `/` or a
// `:` in one would break the route. It mirrors /admin/stats/session's own
// `?key=`.
func (h *Handler) SessionHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		h.fail(w, r, http.StatusBadRequest, "missing required query parameter: key")
		return
	}

	view := sessionView{
		viewBase: h.base("Sessions"),
		Key:      key,
		ShortKey: shortSessionKey(key),
	}
	view.Title = "session " + shortSessionKey(key)

	if h.reader != nil {
		turns, err := h.reader.Session(r.Context(), key, transcriptTurnLimit)
		if err != nil {
			h.logger.LogError(r.Context(), "error", err,
				map[string]interface{}{"phase": "admin_ui_session"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
		if len(turns) == transcriptTurnLimit {
			view.Capped = true
		}

		// seen accumulates every block hash the session has shown so far, so a
		// turn's replayed preamble and history can be told from its new content.
		// This is exact, not heuristic: the client re-sends earlier turns
		// verbatim, and the store addresses blocks by hash, so "same hash" means
		// "the same bytes". Read in turn order, which is what makes the first
		// appearance the new one.
		seen := map[string]int{}

		for i, t := range turns {
			turn := transcriptTurn{Request: t, Index: i + 1}
			view.TotalCost += t.CostUSD
			view.TotalInput += t.InputTokens
			view.TotalOutput += t.OutputTokens

			blocks, err := h.reader.ContentForRequest(r.Context(), t.ID)
			if err != nil {
				// A turn whose content could not be read still belongs in the
				// transcript: the turn happened, and its metadata is correct.
				h.logger.LogError(r.Context(), "warn", err,
					map[string]interface{}{"phase": "admin_ui_session_content", "request_id": t.ID})
			}
			turn.CaptureOff = len(blocks) == 0

			for _, b := range blocks {
				tb := transcriptBlock{ContentBlock: b, RequestID: t.ID}
				_, previouslySeen := seen[b.Hash]
				if b.Direction == "request" && previouslySeen {
					// Request-side and already shown: this turn is re-sending
					// context, not saying anything new. Its body is neither
					// rendered nor announced — the text is already on the page
					// at the turn that introduced it, so a pointer at every turn
					// is noise a reader has to skip past.
					//
					// It is still counted: the header reports how much the turn
					// replayed, which is useful without a block-by-block account
					// of it.
					turn.ReplayChars += len(b.Body)
					continue
				}
				seen[b.Hash] = i + 1
				tb.turnIndex = i + 1
				// The preamble is split out only for the turn that introduces
				// it, so a client's standing instructions are readable on their
				// own rather than mixed into the conversation. It is *moved into*
				// Preamble, not copied there: a block appended to both would be
				// rendered twice by the turn that introduced it.
				if b.Role == "system" && b.Direction == "request" {
					turn.Preamble = append(turn.Preamble, tb)
					turn.PreambleChars += len(b.Body)
					continue
				}
				turn.New = append(turn.New, tb)
				turn.NewChars += len(b.Body)
			}
			view.Turns = append(view.Turns, turn)
		}
		view.Found = len(turns) > 0
	}

	h.render(w, r, "session", "session-turns", view)
}

// transcriptTurnLimit bounds one transcript. A long agent conversation can run
// to hundreds of turns; the page is a reading view, not an export.
const transcriptTurnLimit = 200

// requestsForSession links to the flat request list filtered to one session —
// the request-level view of the same conversation, for when the transcript is
// not what you want.
func requestsForSession(key string) string {
	return "/admin/ui/requests?session=" + url.QueryEscape(key)
}
