package ui

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/cnf/arbiter/internal/store"
)

// defaultListLimit is the UI's own default page size. It is *below* the
// reader's cap (maxRequestListLimit, 500) deliberately: paging in 100-row
// steps means the cap is rarely reached, so the "list is capped" note is an
// exception rather than the normal case.
const defaultListLimit = 100

// requestFilterView is the filter form's state. Raw strings are kept for the
// fields the operator types, so an invalid value is echoed back into the form
// beside the error instead of being silently normalised away.
type requestFilterView struct {
	SinceRaw       string
	Provider       string
	Alias          string
	StatusRaw      string
	SessionKey     string
	SessionKeyless bool
	ErrorsOnly     bool
	LimitRaw       string

	// Any records whether any filter is set, so the empty state can offer
	// "widen" only when there is something to widen.
	Any bool
}

// requestRowView pairs a stored row with its display-only short session key.
//
// The shortening is a separate field rather than a transformation of SessionKey,
// because the full value still has to reach the filter link and the session
// link: a truncated key in an href fetches the wrong conversation.
type requestRowView struct {
	store.RequestRow
	ShortSession string
}

// rowsView is what the request table renders. It is carried by the page and by
// the htmx fragment alike, so a swapped table and a loaded page cannot
// disagree about the rows, the pager, or the filter state.
type rowsView struct {
	Rows    []requestRowView
	More    bool
	MoreURL string
	F       requestFilterView
}

// requestsView is the full page: the table view plus the chrome.
type requestsView struct {
	viewBase
	rowsView
}

// RequestsHandler handles GET /admin/ui/requests: the request list, newest
// first, with the 7a filters as a real form.
//
// Query parameters mirror the JSON endpoint's (since, provider, session,
// alias, status, errors, limit) plus no_session and the keyset cursor
// (before_ts/before_id). A malformed value is a 400 naming the parameter, never
// a silently ignored filter: a filter that quietly returns unfiltered data
// shows wrong numbers with no indication anything was dropped.
func (h *Handler) RequestsHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	q := r.URL.Query()
	fv := requestFilterView{
		SinceRaw:   q.Get("since"),
		Provider:   q.Get("provider"),
		Alias:      q.Get("alias"),
		StatusRaw:  q.Get("status"),
		SessionKey: q.Get("session"),
		LimitRaw:   q.Get("limit"),
	}

	f := store.RequestFilter{
		Provider:       fv.Provider,
		Alias:          fv.Alias,
		SessionKey:     fv.SessionKey,
		SessionKeyless: q.Has("no_session"),
		ErrorsOnly:     q.Has("errors"),
		Limit:          defaultListLimit,
	}
	// since: a Go duration, matching the JSON surface's own parameter so the
	// two read surfaces describe one window the same way.
	if raw := q.Get("since"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			h.fail(w, r, http.StatusBadRequest,
				`since must be a positive Go duration, e.g. "24h" or "168h"`)
			return
		}
		f.Since = time.Now().UTC().Add(-d)
	}
	if raw := q.Get("status"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 100 || n > 599 {
			h.fail(w, r, http.StatusBadRequest, "status must be an HTTP status code (100-599)")
			return
		}
		f.StatusCode = n
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			h.fail(w, r, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		f.Limit = n
	}
	// The keyset cursor, as one opaque token (see encodeCursor).
	if raw := q.Get("after"); raw != "" {
		ts, id, err := decodeCursor(raw)
		if err != nil {
			h.fail(w, r, http.StatusBadRequest, "after is not a valid cursor: "+err.Error())
			return
		}
		f.BeforeTs, f.BeforeID = ts, id
	}

	// The cursor is not part of the filter form's own state, so the form never
	// shows it; but it *is* part of "is anything filtered", because a cursor
	// means this is a later page rather than the first.
	fv.Any = f.Provider != "" || f.Alias != "" || f.SessionKey != "" || f.StatusCode != 0 ||
		f.ErrorsOnly || f.SessionKeyless || !f.Since.IsZero()

	view := requestsView{viewBase: h.base("Requests"), rowsView: rowsView{F: fv}}

	if h.reader != nil {
		rows, err := h.reader.ListRequests(r.Context(), f)
		if err != nil {
			h.logger.LogError(r.Context(), "error", err,
				map[string]interface{}{"phase": "admin_ui_requests"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
		for _, row := range rows {
			view.Rows = append(view.Rows, requestRowView{
				RequestRow:   row,
				ShortSession: shortSessionKey(row.SessionKey),
			})
		}
		// moreURL needs the stored rows (it reads the cursor off the last one),
		// not the view rows.
		view.MoreURL = moreURL(q, rows, f.Limit)
		view.More = view.MoreURL != ""
	}

	h.render(w, r, "requests", "req-rows", view)
}

// moreURL builds the keyset continuation link: the current filter set plus the
// cursor from the last row rendered. It returns "" when the list is complete.
//
// "Complete" is decided by the page size, not by a COUNT: the reader clamps the
// limit itself, so a short page is the only signal available without a second
// query, and an empty page is the definitive end. A full page may therefore
// offer one more page that turns out to be empty — a harmless extra click,
// where the alternative (a COUNT over the same filter) costs a query on every
// page view.
func moreURL(q url.Values, rows []store.RequestRow, limit int) string {
	if len(rows) == 0 || len(rows) < limit || limit <= 0 {
		return ""
	}
	last := rows[len(rows)-1]
	if last.TsRaw == "" {
		return ""
	}
	next := url.Values{}
	for _, k := range []string{"since", "provider", "alias", "status", "session", "errors", "limit"} {
		if v := q.Get(k); v != "" || (k == "errors" && q.Has(k)) {
			next.Set(k, v)
		}
	}
	if q.Has("no_session") {
		next.Set("no_session", "1")
	}
	next.Set("after", encodeCursor(last.TsRaw, last.ID))
	return "/admin/ui/requests?" + next.Encode()
}

// blockView pairs a captured block with the request it belongs to, so the
// block partial can link back to its own full body. The store's ContentBlock
// is deliberately owner-agnostic (a rejected request's content has no request
// id), so the owner is supplied by whoever knows it.
type blockView struct {
	store.ContentBlock
	OwnerID int64
}

// cursorSeparator splits the two halves of a cursor payload. It cannot occur in
// either half: a timestamp is digits and punctuation, an id is digits.
const cursorSeparator = "\x00"

// encodeCursor packs the keyset position into one opaque, URL-safe token.
//
// It is opaque deliberately. The honest cursor is the row's stored timestamp
// text, which is `2026-09-16 11:59:39.812343302 +0000 UTC` — a value whose `+`
// characters a query string is entitled to read as spaces, and a client or
// proxy that does so produces a bound matching no row at all. That failure is
// silent: the page renders "no results" rather than an error, which is exactly
// the shape this codebase refuses elsewhere. Base64 keeps the payload exact and
// URL-safe, and hides the store's internal time format from the address bar.
func encodeCursor(ts string, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(ts + cursorSeparator + strconv.FormatInt(id, 10)))
}

// decodeCursor reverses encodeCursor. A malformed cursor is an error the caller
// reports as a 400, never a silently ignored page position.
func decodeCursor(raw string) (string, int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", 0, fmt.Errorf("not base64")
	}
	parts := strings.SplitN(string(b), cursorSeparator, 2)
	if len(parts) != 2 || parts[0] == "" {
		return "", 0, fmt.Errorf("malformed payload")
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id < 1 {
		return "", 0, fmt.Errorf("malformed id")
	}
	return parts[0], id, nil
}

// detailView is the request detail page: metadata, the conversation path
// through the session, and the captured content when it was asked for.
type detailView struct {
	viewBase
	D store.RequestDetail

	// Turns is the session's requests up to and including this one, so the
	// page can show where this request sits in a conversation and link to any
	// other point in it. Empty for a request with no session key, which is not
	// a conversation.
	Turns  []store.SessionRequest
	Blocks []blockView

	// ContentLoaded distinguishes "the content fragment was requested" from
	// "this request has no captured content" — capture off and nothing
	// captured are different answers.
	ContentLoaded bool
}

// RequestHandler handles GET /admin/ui/requests/{id}.
func (h *Handler) RequestHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil || id < 1 {
		h.fail(w, r, http.StatusBadRequest, "request id must be a positive integer")
		return
	}
	view := detailView{viewBase: h.base("Requests")}

	if h.reader != nil {
		detail, ok, err := h.reader.GetRequest(r.Context(), id)
		if err != nil {
			h.logger.LogError(r.Context(), "error", err,
				map[string]interface{}{"phase": "admin_ui_request"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
		if !ok {
			h.fail(w, r, http.StatusNotFound, "no request with that id")
			return
		}
		view.D = detail
		view.Turns = h.turnsFor(r, detail)
	}
	view.Title = "request " + strconv.FormatInt(id, 10)

	h.render(w, r, "request", "request-content", view)
}

// turnsFor returns the session's requests in order, truncated at d. It gives
// the detail page a position in a conversation, which is what makes "which
// turn was this" answerable; a request with no session key has no such
// position and gets none. An error here degrades the page rather than failing
// it — the request's own metadata is already loaded and remains correct.
func (h *Handler) turnsFor(r *http.Request, d store.RequestDetail) []store.SessionRequest {
	if d.SessionKey == "" {
		return nil
	}
	rows, err := h.reader.Session(r.Context(), d.SessionKey, 500)
	if err != nil {
		h.logger.LogError(r.Context(), "warn", err,
			map[string]interface{}{"phase": "admin_ui_request_session"})
		return nil
	}
	for i, row := range rows {
		if row.ID == d.ID {
			return rows[:i+1]
		}
	}
	return nil
}

// wrapBlocks attaches the owning request id to each block.
func wrapBlocks(id int64, blocks []store.ContentBlock) []blockView {
	out := make([]blockView, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, blockView{ContentBlock: b, OwnerID: id})
	}
	return out
}

// RequestContentHandler handles GET /admin/ui/requests/{id}/content: the
// captured blocks, as an htmx fragment loaded lazily so a multi-megabyte
// prompt never delays the metadata view.
//
// It is also the "pull one point out" path: ?turn=N returns the conversation
// from its start up to turn N, so any earlier point in a session can be read
// on its own without scrolling a full transcript.
func (h *Handler) RequestContentHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil || id < 1 {
		h.fail(w, r, http.StatusBadRequest, "request id must be a positive integer")
		return
	}
	view := detailView{viewBase: h.base("Requests"), ContentLoaded: true}

	if h.reader != nil {
		blocks, err := h.reader.ContentForRequest(r.Context(), id)
		if err != nil {
			h.logger.LogError(r.Context(), "error", err,
				map[string]interface{}{"phase": "admin_ui_request_content"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
		view.Blocks = wrapBlocks(id, blocks)
		view.ContentLoaded = true
		if _, ok, err := h.reader.GetRequest(r.Context(), id); err == nil && ok {
			view.D.ID = id
		}
	}
	h.render(w, r, "request", "request-content", view)
}
