package ui

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// discoveryDefaultMinSessions is the default minimum session spread.
//
// The JSON endpoint defaults this to 0, and for a raw query that is right. For
// the page it is not: a block repeated within one conversation is the ordinary
// shape of a multi-turn chat — every turn re-sends the system prompt — so a list
// ordered by request count at min_sessions=0 is topped by that, which is not a
// finding. The number that answers "is some client injecting this?" is the
// session count, so the page leads with it. It is a default, not a floor: 0 is
// still accepted.
const discoveryDefaultMinSessions = 2

// discoveryDefaultLimit matches the JSON endpoint's own default.
const discoveryDefaultLimit = 50

// repeatedBlockView is one repeated block plus the display-only handling of its
// hash. The full hash is what goes in the drill-down link; the short form is what
// a table can show without turning a column into 64 characters of hex.
type repeatedBlockView struct {
	store.RepeatedContent
	ShortHash string
}

// discoveryView is the repeated-content page: which blocks recur, how widely, and
// the drill-down from a block to the requests containing it.
type discoveryView struct {
	viewBase

	Rows []repeatedBlockView

	SinceRaw    string
	Since       time.Duration
	MinRequests int
	MinSessions int
	Limit       int
	MinReqRaw   string
	MinSessRaw  string
	LimitRaw    string

	// Total/Matching report how many distinct blocks exist in the window
	// against how many pass the thresholds. A filter that removed everything
	// must read as a filter, not as an empty store.
	Total    int64
	Matching int64

	// Sessionless is how many requests in the window carry no session key. It
	// is here because min_sessions counts *distinct session keys*, so on a
	// store where most traffic is unpinned a high session threshold will match
	// very little — and without this the result looks like an absence of
	// boilerplate rather than a consequence of the filter.
	Sessionless int64

	// Capped says the list hit its limit, so a truncated list is not read as
	// the whole picture.
	Capped bool

	// Shown is how many rows are on the page, so the summary can say
	// "showing N of M matching" without a second COUNT.
	Shown int

	// Bounds is the accepted parameter range, from the store, so the page copy
	// and the 400 body cannot disagree.
	Bounds string
}

// DiscoveryHandler handles GET /admin/ui/content/repeated: the blocks that recur
// across requests, most widespread first.
//
// A bad parameter is a 400 naming the parameter, never a silent clamp — with the
// one documented exception that min_requests below 2 is refused rather than
// raised, because the operator asked for something the query cannot mean.
func (h *Handler) DiscoveryHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	q := r.URL.Query()

	view := discoveryView{
		viewBase:    h.base(r.Context(), "Discovery"),
		SinceRaw:    q.Get("since"),
		MinReqRaw:   q.Get("min_requests"),
		MinSessRaw:  q.Get("min_sessions"),
		LimitRaw:    q.Get("limit"),
		Since:       defaultWindow,
		MinRequests: store.MinRepeatedRequests,
		MinSessions: discoveryDefaultMinSessions,
		Limit:       discoveryDefaultLimit,
		Bounds:      store.RepeatedBoundsNote(),
	}
	view.Title = "discovery"

	if raw := q.Get("since"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			h.fail(w, r, http.StatusBadRequest,
				`since must be a positive Go duration, e.g. "24h" or "168h"`)
			return
		}
		view.Since = d
	}
	if raw := q.Get("min_requests"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < store.MinRepeatedRequests {
			h.fail(w, r, http.StatusBadRequest,
				"min_requests must be an integer of at least "+strconv.Itoa(store.MinRepeatedRequests)+
					" — a block seen once is not repeated")
			return
		}
		view.MinRequests = n
	}
	if raw := q.Get("min_sessions"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			h.fail(w, r, http.StatusBadRequest, "min_sessions must be a non-negative integer")
			return
		}
		view.MinSessions = n
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxRepeatedLimit {
			h.fail(w, r, http.StatusBadRequest,
				"limit must be an integer from 1 to "+strconv.Itoa(store.MaxRepeatedLimit))
			return
		}
		view.Limit = n
	}

	if h.reader != nil {
		w0 := store.Window{Since: time.Now().UTC().Add(-view.Since)}

		blocks, err := h.reader.RepeatedContent(r.Context(), w0, view.MinRequests, view.MinSessions, view.Limit)
		if err != nil {
			h.logger.LogError(r.Context(), "error", err,
				map[string]interface{}{"phase": "admin_ui_discovery"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
		for _, b := range blocks {
			view.Rows = append(view.Rows, repeatedBlockView{
				RepeatedContent: b,
				ShortHash:       shortHash(b.Hash),
			})
		}
		view.Shown = len(blocks)
		view.Capped = len(blocks) == view.Limit

		// Both are extras: the block list already loaded and is the point of
		// the page, so a failure here degrades the explanation rather than the
		// result. Each is logged, and its absence is visible as a missing line
		// rather than as a wrong number.
		total, matching, err := h.reader.ContentHashCounts(r.Context(), w0, view.MinRequests, view.MinSessions)
		if err != nil {
			h.logger.LogError(r.Context(), "warn", err,
				map[string]interface{}{"phase": "admin_ui_discovery_counts"})
		}
		view.Total, view.Matching = total, matching

		n, err := h.reader.SessionlessRequestCount(r.Context(), w0)
		if err != nil {
			h.logger.LogError(r.Context(), "warn", err,
				map[string]interface{}{"phase": "admin_ui_discovery_sessionless"})
		}
		view.Sessionless = n
	}

	h.render(w, r, "discovery", "repeated-rows", view)
}

// blockRequestsView is the drill-down: one block's text and the requests that
// contain it.
type blockRequestsView struct {
	viewBase

	Hash      string
	ShortHash string
	Block     store.ContentBlock

	// Captured is false when the hash is known but no body was stored — the
	// block is still real and still repeated, which is why the page says so
	// rather than showing an empty box.
	Captured bool

	Rows      []requestRowView
	Total     int64
	Limit     int
	Capped    bool
	Sessionle bool
}

// BlockRequestsHandler handles GET /admin/ui/content/block?hash=…: the requests
// that contain one block, newest first.
//
// The hash is a query parameter rather than a path segment because it is hex and
// long, and a path segment would need its own escaping rules for a value that is
// already opaque.
func (h *Handler) BlockRequestsHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	q := r.URL.Query()
	hash := q.Get("hash")
	if hash == "" {
		h.fail(w, r, http.StatusBadRequest, "hash is required")
		return
	}

	view := blockRequestsView{
		viewBase:  h.base(r.Context(), "Discovery"),
		Hash:      hash,
		ShortHash: shortHash(hash),
		Limit:     store.MaxRepeatedLimit,
	}
	view.Title = "block"

	seen := defaultWindow
	if raw := q.Get("since"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			seen = d
		}
	}

	if h.reader != nil {
		rows, err := h.reader.RequestsForContent(r.Context(), hash, view.Limit)
		if err != nil {
			// A malformed hash is the operator's input, not a server failure:
			// the store refuses it by shape, and reporting that as a 500 would
			// send them looking for a bug in the wrong place.
			if errors.Is(err, store.ErrBadContentHash) {
				h.fail(w, r, http.StatusBadRequest, err.Error())
				return
			}
			h.logger.LogError(r.Context(), "error", err,
				map[string]interface{}{"phase": "admin_ui_block_requests"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
		for _, row := range rows {
			view.Rows = append(view.Rows, requestRowView{
				RequestRow:   row,
				ShortSession: shortSessionKey(row.SessionKey),
			})
		}
		// Total is the number of rows found. It was declared and never set, so
		// the page announced "0 requests contain this block" above a table of
		// five — the stated count and the rendered rows must come from one
		// value, which is why this is set here and the template reads only this.
		view.Total = int64(len(rows))
		view.Capped = len(rows) == view.Limit

		// The body and the sessionless count are extras; the request list is
		// the answer. A failure on either still leaves a usable page.
		if block, ok, err := h.reader.ContentByHash(r.Context(), hash); err != nil {
			if errors.Is(err, store.ErrBadContentHash) {
				h.fail(w, r, http.StatusBadRequest, err.Error())
				return
			}
			h.logger.LogError(r.Context(), "warn", err,
				map[string]interface{}{"phase": "admin_ui_block_body"})
		} else if ok {
			view.Block = block
			view.Captured = true
		}

		w0 := store.Window{Since: time.Now().UTC().Add(-seen)}
		if n, err := h.reader.SessionlessRequestCount(r.Context(), w0); err == nil {
			view.Sessionle = n > 0
		}
	}

	h.render(w, r, "block", "block-requests", view)
}

// requestsForBlockURL builds the drill-down link. It is a func rather than an
// inline template expression because the hash arrives already in hex and the
// template must not be building a query string by hand.
func requestsForBlockURL(hash string) string {
	return "/admin/ui/content/block?hash=" + url.QueryEscape(hash)
}

// shortHash is shortSessionKey's counterpart for a content hash. Both are
// delibately the same idea: an opaque 64-character handle is shown truncated and
// carried in full in links, because truncation is a display concern and a query
// built from the truncated form would match nothing.
func shortHash(hash string) string { return shortSessionKey(hash) }
