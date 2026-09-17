package ui

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

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
// sits between the server and the client. The rows ride along as a pre-rendered
// HTML fragment so the row markup has exactly one definition.
type tailResponse struct {
	// Rows is the <tr> sequence to append, already escaped and rendered.
	Rows string `json:"rows"`

	// Cursor is the opaque token for the next poll. Empty means nothing was
	// returned, in which case the client keeps the cursor it already had.
	Cursor string `json:"cursor"`

	// NewestID is the id of the newest row in this response, so the client can
	// skip a row it already has without parsing HTML.
	NewestID int64 `json:"newest_id"`

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

	resp := tailResponse{
		Rows:      renderTailRows(r.Context(), h, view),
		Truncated: len(rows) == f.Limit,
	}
	if ts, id, ok := store.NewestCursor(rows); ok {
		resp.Cursor = encodeCursor(ts, id)
		resp.NewestID = id
	}
	h.writeJSON(w, r, resp)
}

// renderTailRows renders just the row sequence. A failure here is answered with
// an empty fragment rather than a broken response: the tail's next poll retries,
// and the alternative is one malformed row taking out the whole view.
func renderTailRows(ctx context.Context, h *Handler, view rowsView) string {
	var buf strings.Builder
	set, ok := h.fragments["fragments"]
	if !ok {
		return ""
	}
	if err := set.ExecuteTemplate(&buf, "req-rows-tail", view); err != nil {
		h.logger.LogError(ctx, "error", err,
			map[string]interface{}{"phase": "admin_ui_tail_render"})
		return ""
	}
	return buf.String()
}
