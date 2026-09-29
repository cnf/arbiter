package ui

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

type blockRequestRowView struct {
	requestRowView
	HasDiffLink bool
	DiffMsg     int64
	DiffPos     int64
}

// GuardrailDiffURL builds the on-demand diff fragment's fetch URL for this
// row. A method rather than an inline template expression for the same
// reason requestsForBlockURL is a func: the template must not hand-build a
// query string.
func (r blockRequestRowView) GuardrailDiffURL() string {
	return "/admin/ui/requests/" + strconv.FormatInt(r.ID, 10) +
		"/guardrail-diff?msg=" + strconv.FormatInt(r.DiffMsg, 10) +
		"&pos=" + strconv.FormatInt(r.DiffPos, 10)
}

// blockRequestsView is the drill-down: one block's text and the sessions that
// first sent it — one row per distinct session (the earliest request in that
// session that carries this hash), not one row per raw request. A client
// resends its whole history every turn, so listing every reference or every
// request would repeat the same session many times over; this answers "how
// many different sessions sent this, and what did each look like first".
type blockRequestsView struct {
	viewBase

	Hash      string
	ShortHash string
	Block     store.ContentBlock

	// Captured is false when the hash is known but no body was stored — the
	// block is still real and still repeated, which is why the page says so
	// rather than showing an empty box.
	Captured bool

	Rows      []blockRequestRowView
	Total     int64
	Limit     int
	Capped    bool
	Sessionle bool
}

// discoveryBlockBodyView is the workspace pane's on-demand full-body fragment
// (#50 follow-up): entering workspace mode used to show only the row's
// 200-char SQL preview (data-preview) stretched into a box the CSS still
// clamped to 220px — "full screen" that showed less text than the inline
// pane. This reuses the block drill-down's own store.ContentByHash, but
// renders the body untruncated — the user asked for workspace mode to show
// "the full text, untruncated", so blockPreviewBytes' 8KB render cap (used
// by the drill-down page and everywhere else a stored body renders) is
// deliberately skipped here.
type discoveryBlockBodyView struct {
	Hash      string
	ShortHash string
	Block     store.ContentBlock
	Captured  bool
}

// BlockRequestsHandler handles GET /admin/ui/content/block?hash=…: the
// sessions that first sent one block, most recent first.
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

	seen := discoveryDefaultWindow
	if raw := q.Get("since"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			seen = d
		}
	}

	if h.reader != nil {
		rows, err := h.reader.SessionsForContent(r.Context(), hash, view.Limit)
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
			view.Rows = append(view.Rows, blockRequestRowView{
				requestRowView: requestRowView{
					RequestRow:   row,
					ShortSession: shortSessionKey(row.SessionKey),
				},
			})
		}
		// Total is the number of sessions found (one row per session, see
		// SessionsForContent). It was declared and never set, so the page
		// announced "0 requests contain this block" above a table of five —
		// the stated count and the rendered rows must come from one value,
		// which is why this is set here and the template reads only this.
		view.Total = int64(len(rows))
		view.Capped = len(rows) == view.Limit

		// Attach each row's diff-link coordinates. An extra, like the body and
		// sessionless count below: a lookup failure here loses the "view diff"
		// link, not the request list itself.
		if len(rows) > 0 {
			ids := make([]int64, len(rows))
			for i, row := range rows {
				ids[i] = row.ID
			}
			positions, err := h.reader.PositionsForContent(r.Context(), hash, ids)
			if err != nil {
				h.logger.LogError(r.Context(), "warn", err,
					map[string]interface{}{"phase": "admin_ui_block_positions"})
			}
			for i := range view.Rows {
				if p, ok := positions[view.Rows[i].ID]; ok {
					view.Rows[i].HasDiffLink = true
					view.Rows[i].DiffMsg = p.MsgIndex
					view.Rows[i].DiffPos = p.Position
				}
			}
		}

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

// DiscoveryBlockBodyHandler handles GET /admin/ui/content/block/body?hash=…: a
// fragment-only endpoint (there is no full-page form — workspace mode is the
// only caller) returning one block's full stored body. It exists because the
// ledger row only ever carries a 200-char SQL preview (RepeatedContent's own
// SUBSTR cap, kept small deliberately so a cross-session GROUP BY never drags
// megabyte bodies through the aggregation) — workspace mode is exactly the
// place that preview is not enough, so it fetches the real thing on entry the
// same way the drill-down page already does.

func (h *Handler) DiscoveryBlockBodyHandler(w http.ResponseWriter, r *http.Request) {
	if !fragmentsRequested(r) {
		h.fail(w, r, http.StatusBadRequest, "this endpoint only serves htmx fragment requests")
		return
	}
	if disabled := h.storeDisabled(w, r); disabled {
		return
	}

	hash := r.URL.Query().Get("hash")
	if hash == "" {
		h.fail(w, r, http.StatusBadRequest, "hash is required")
		return
	}

	view := discoveryBlockBodyView{Hash: hash, ShortHash: shortHash(hash)}

	block, ok, err := h.reader.ContentByHash(r.Context(), hash)
	if err != nil {
		if errors.Is(err, store.ErrBadContentHash) {
			h.fail(w, r, http.StatusBadRequest, err.Error())
			return
		}
		h.logger.LogError(r.Context(), "error", err,
			map[string]interface{}{"phase": "admin_ui_block_body"})
		h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	if ok {
		view.Block = block
		view.Captured = true
	}

	h.exec(w, r, h.fragments, "fragments", "discovery-block-body", view)
}

// discoveryStateURL builds the state-cycle POST target for one block. A func
// (not a method on repeatedBlockView) because it also needs LastSeen, which
// the row already carries — kept as a free function since the template calls
// it with two explicit values rather than one struct, matching
// requestsForBlockURL's own shape.
func discoveryStateURL(hash, lastSeen string) string {
	return "/admin/ui/content/repeated/state?hash=" + url.QueryEscape(hash) +
		"&last_seen=" + url.QueryEscape(lastSeen)
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
