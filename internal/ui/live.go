package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// tailRow is one row in a tail response: the rendered <tr>, plus the identity
// the client needs to know *where* that row belongs.
//
// It exists because a grouped view renders one line per group, so a naive tail
// that prepends a row per poll would put two lines of the same group on screen
// and make the table disagree with itself. The key is computed by the same
// function that folded the page (foldRequestLines), so the server's idea of a
// group and the client's cannot drift apart — see live.js's placement logic.
type tailRow struct {
	// HTML is the fully rendered <tr>, escaped by the template.
	HTML string `json:"html"`

	// ID is the row's request id, so the client can skip one it already holds
	// (the tail's own defence against a cursor that ever slips) without parsing
	// the markup for it.
	ID int64 `json:"id"`

	// Key is the folded group's short attribute id, or empty when the row is
	// not a collapsible run (a non-streamed request, or the only row of its
	// group so far). Empty means "render me as my own row", which is exactly
	// what Flat mode and a single request want.
	Key string `json:"key,omitempty"`
}

// tailResponse is what the live tail's endpoint returns: the rows to append, plus
// the cursor to poll from next time.
//
// It is JSON carrying an HTML fragment rather than a bare HTML response, for one
// reason that matters: the cursor. The tail's cursor is the stored `ts` text,
// which is variable-length and contains "+0000 UTC" — so putting it in a data
// attribute and reading it back through `getAttribute` would round-trip it
// through HTML escaping, and the value that comes out is not byte-for-byte the
// value that went in. A cursor that differs by one character compares wrongly
// against the column, which is the `ts` landmine again, and the symptom is a tail
// that repeats or skips rows rather than an error.
//
// So the cursor stays an opaque base64 token (the same encoding the request list
// already uses for paging) and travels as a JSON string, where no escaping step
// sits between the server and the client. The rows ride along as pre-rendered
// HTML so the row markup has exactly one definition.
type tailResponse struct {
	// Rows is the <tr> sequence to append, already escaped and rendered, each
	// with the group identity it belongs to.
	Rows []tailRow `json:"rows"`

	// Cursor is the opaque token for the next poll. Empty means nothing was
	// returned, in which case the client keeps the cursor it already had.
	Cursor string `json:"cursor"`

	// NewestID is the id of the newest row in this response, so the client can
	// skip a row it already has without parsing HTML.
	NewestID int64 `json:"newest_id"`

	// NewestKey is the folded group of that newest row, so the client can tell
	// whether the newest arrival belongs to a line already on screen (and update
	// its count) or is a new line to prepend.
	NewestKey string `json:"newest_key,omitempty"`

	// Grouped says the receiving page is rendering folded lines, so the client
	// must place rows by group rather than prepending them one at a time.
	Grouped bool `json:"grouped"`

	// Truncated says the poll hit its cap, so there may be more rows than were
	// returned. A tail that silently dropped the excess would look like a quiet
	// period instead of a burst.
	Truncated bool `json:"truncated"`

	// Error is set when the query failed. The tail reports its own failures in
	// the body rather than as an HTTP error, because the client polls repeatedly
	// and a 500 per poll would spam the console without telling the operator
	// anything the page does not.
	Error string `json:"error,omitempty"`
}

// writeJSON writes a JSON body, and writeJSONError is the failure half of the
// same contract.
//
// These live here rather than in a page's own file because the live tail is now
// their only consumer: they arrived with the old Overview's chart endpoint
// (series.json), which #54's rebuild deleted along with the rest of the pivot
// explorer. The tail is fetched by script rather than by htmx, so both its
// successes and its failures must be JSON while every page's are HTML.
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

// TailHandler serves GET /admin/ui/requests/tail: the requests written since a
// cursor, for the live view.
//
// Polled by live.js, not by htmx. htmx's declarative `every 5s` cannot pause on a
// hidden tab, cannot stop after repeated failures, and cannot say how many rows
// arrived — all of which a tail needs, and the last of which is the whole point
// of looking at one. See live.js for what it does instead; the server side is an
// ordinary filtered list query in the other direction.
//
// A malformed cursor is a 400 naming the parameter. A malformed *filter* is also
// a 400, for the same reason the list refuses it: a filter silently ignored shows
// unwelcome traffic in a view the operator believes is narrowed.
func (h *Handler) TailHandler(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		writeJSONError(w, http.StatusServiceUnavailable,
			"the event store is disabled: set storage.path to record and follow requests")
		return
	}
	q := r.URL.Query()

	f := store.RequestFilter{
		Provider:       q.Get("provider"),
		Alias:          q.Get("alias"),
		SessionKey:     q.Get("session"),
		SessionKeyless: q.Has("no_session"),
		ErrorsOnly:     q.Has("errors"),
		Limit:          store.MaxTailLimit,
		Kind:           requestKindFilter(q.Get("kind")),
	}
	if raw := q.Get("since"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			writeJSONError(w, http.StatusBadRequest,
				`since must be a positive Go duration, e.g. "24h" or "168h"`)
			return
		}
		f.Since = time.Now().UTC().Add(-d)
	}
	if raw := q.Get("status"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 100 || n > 599 {
			writeJSONError(w, http.StatusBadRequest, "status must be an HTTP status code (100-599)")
			return
		}
		f.StatusCode = n
	}

	var (
		afterTs string
		afterID int64
	)
	if raw := q.Get("cursor"); raw != "" {
		ts, id, err := decodeCursor(raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "cursor is not valid: "+err.Error())
			return
		}
		afterTs, afterID = ts, id
	}

	rows, err := h.reader.ListRequestsAfter(r.Context(), f, afterTs, afterID, f.Limit)
	if err != nil {
		h.logger.LogError(r.Context(), "error", err,
			map[string]interface{}{"phase": "admin_ui_tail"})
		// Reported in the body, not as a status: a poll failing is a condition the
		// page shows and retries, not an exceptional response.
		h.writeJSON(w, r, tailResponse{Error: "query failed: " + err.Error()})
		return
	}

	view := rowsView{F: requestFilterView{}}
	for _, row := range rows {
		view.Rows = append(view.Rows, requestRowView{
			RequestRow:   row,
			ShortSession: shortSessionKey(row.SessionKey),
		})
	}

	// One response row per stored row, each carrying the group key of the request
	// itself.
	//
	// The poll deliberately does *not* fold its own batch. Folding here answered
	// "which group is this row in" from the batch's contents, and a poll is
	// normally a single new turn — so it both lost rows (two requests of one
	// group collapsed into one emitted row, which is the list hiding traffic) and
	// returned a key relative to a batch the page never saw. The client's question
	// is the row's own, and lineAttr answers it identically for a polled row and a
	// page-loaded one, which is what makes the keys match.
	//
	// Grouped is the *page's* mode, carried on the tail's own query string (see
	// tailFilterQuery). The tail follows the list it sits under, so a flat list
	// gets a flat tail; a mismatch would have the client placing rows by group in
	// a table that has none.
	resp := tailResponse{
		Grouped:   !q.Has("flat"),
		Truncated: len(rows) >= f.Limit && len(rows) > 0,
	}
	for _, row := range view.Rows {
		key := ""
		if resp.Grouped {
			key = tailRowKey(row)
		}
		resp.Rows = append(resp.Rows, tailRow{
			HTML: renderTailRow(r.Context(), h, row, key),
			ID:   row.ID,
			Key:  key,
		})
	}
	if ts, id, ok := store.NewestCursor(rows); ok {
		resp.Cursor = encodeCursor(ts, id)
		resp.NewestID = id
		for _, sent := range resp.Rows {
			if sent.ID == id {
				resp.NewestKey = sent.Key
				break
			}
		}
	}
	h.writeJSON(w, r, resp)
}

// tailRowKey names the group a polled row belongs to, independent of the poll's
// own batch.
//
// It is the row-level `lineAttr` — the same function the page's rows are marked
// with — rather than the folded line's key, and that is the point. A poll is
// almost always one new turn, so answering from the folded batch would return
// "no group" for exactly the rows the client must place, and every new turn of a
// conversation already on screen would prepend a duplicate line instead of
// bumping the line it belongs to.
func tailRowKey(row requestRowView) string {
	return lineAttr(row)
}

// renderTailRow renders one request row.
//
// A failure here yields an empty string rather than a broken response: the
// tail's next poll retries, and the alternative is one malformed row taking out
// the whole view.
//
// The row is rendered through the same "req-line" partial the page uses, so a
// polled row and a page-loaded row cannot drift apart in markup. key is the
// group the row travels with, already decided by the caller (empty in Flat
// mode): setting it here is what lets the client find the line the row belongs
// to, and without it a new turn of a conversation on screen could not join its
// line and would be prepended as a duplicate. Count stays 1 and Run stays false,
// because one polled row stands for exactly itself.
func renderTailRow(ctx context.Context, h *Handler, row requestRowView, key string) string {
	set, ok := h.fragments["fragments"]
	if !ok {
		return ""
	}
	var buf strings.Builder
	line := requestLineView{Rows: []requestRowView{row}, Head: row, Count: 1, Attr: key}
	if err := set.ExecuteTemplate(&buf, "req-line", line); err != nil {
		h.logger.LogError(ctx, "error", err,
			map[string]interface{}{"phase": "admin_ui_tail_render"})
		return ""
	}
	return buf.String()
}
