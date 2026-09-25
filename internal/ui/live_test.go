package ui

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/cnf/arbiter/internal/store"
)

// tailBody decodes the tail endpoint's response, so a test asserts on fields
// rather than pattern-matching JSON.
func tailBody(t *testing.T, h *Handler, target string) (tailResponse, int) {
	t.Helper()
	rec := serve(t, h, "GET", target, false)
	var resp tailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("tail response is not JSON: %v\n%s", err, rec.Body.String())
	}
	return resp, rec.Code
}

// liveEvents seeds three requests in distinct seconds, so a tail's first poll and
// its cursor are unambiguous.
func liveEvents() []store.Event {
	var out []store.Event
	for i, body := range []string{"live body one here", "live body two here", "live body three here"} {
		out = append(out, store.Event{
			TraceID: "t", Provider: "p", Model: "m", StatusCode: 200,
			Content: &store.CapturedContent{Request: []store.Block{
				{Kind: "text", Body: []byte(body), Role: "user", MsgIndex: i, Position: 0},
			}},
		})
	}
	return out
}

// tailHTML joins a tail response's rendered rows, so a test can assert on the
// markup the client receives. It is one helper rather than a per-test loop so the
// shape of a tail row (html + id + key) is decoded in exactly one place.
func tailHTML(resp tailResponse) string {
	var b strings.Builder
	for _, row := range resp.Rows {
		b.WriteString(row.HTML)
	}
	return b.String()
}

// TestTailFirstPollReturnsTheNewestFirstAndACursor is the endpoint's contract: no
// cursor means "give me somewhere to start", newest-first, plus the token to
// continue from.
// TestTailCursorIsOpaqueAndRoundTrips is the property the whole design rests on.
// The cursor is the stored `ts` text inside a base64 token; anything that
// reconstructed it (a Date, a reformat) would change its length and compare
// wrongly against the column, which fails as repeated or skipped rows rather than
// as an error — so the bytes must survive the round trip exactly.
func TestTailCursorIsOpaqueAndRoundTrips(t *testing.T) {
	h, _ := newSeededHandler(t, liveEvents()...)

	first, code := tailBody(t, h, "/admin/ui/requests/tail")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}

	// Polling with that cursor must return nothing: everything is at or behind it.
	resp, code := tailBody(t, h, "/admin/ui/requests/tail?cursor="+first.Cursor)
	if code != 200 {
		t.Fatalf("cursor poll status = %d", code)
	}
	if strings.Count(tailHTML(resp), `data-id="`) != 0 {
		t.Errorf("polling at the cursor returned %d rows, want 0", strings.Count(tailHTML(resp), `data-id="`))
	}
	// And nothing new means no new cursor, so the client keeps the one it has
	// rather than overwriting it with an empty value.
	if resp.Cursor != "" {
		t.Errorf("an empty poll returned a cursor %q; the client would lose its position", resp.Cursor)
	}

	// The cursor token must decode to exactly the stored text of the newest row,
	// which is what makes it correct against the column.
	ts, id, err := decodeCursor(first.Cursor)
	if err != nil {
		t.Fatalf("the cursor does not decode: %v", err)
	}
	if id != first.NewestID {
		t.Errorf("cursor id %d != newest id %d", id, first.NewestID)
	}
	if !strings.Contains(ts, "+0000 UTC") {
		t.Errorf("cursor timestamp %q is not the stored form; it has been reformatted", ts)
	}
}

// TestTailRefusesABadCursor says bad input rather than an empty tail: a client
// with a malformed cursor must be told, or it will poll forever showing nothing.
func TestTailRefusesABadCursor(t *testing.T) {
	h, _ := newSeededHandler(t, liveEvents()...)

	for _, bad := range []string{"not-base64!!", "YWJj", "MTIz"} {
		rec := serve(t, h, "GET", "/admin/ui/requests/tail?cursor="+bad, false)
		if rec.Code != 400 {
			t.Errorf("cursor=%q: status = %d, want 400", bad, rec.Code)
		}
	}
}

// TestTailFilterQueryExcludesThePagingCursor is the exclusion stated directly. It
// is a unit test rather than a page assertion because the case cannot arise on a
// page that has a tail: a cursor makes the page a later one, and a later page has
// no tail. Driving the function is the only way to check the rule holds — and the
// rule matters, because a tail that inherited `after` would poll from the middle
// of history and show traffic newer than a page the reader scrolled to.
func TestTailFilterQueryExcludesThePagingCursor(t *testing.T) {
	values := url.Values{}
	values.Set("provider", "alpha")
	values.Set("since", "24h")
	values.Set("after", encodeCursor("2026-09-16 10:00:00.000000000 +0000 UTC", 1))
	values.Set("limit", "50")

	got := tailFilterQuery(values)
	if strings.Contains(got, "after") {
		t.Errorf("the tail's filter query carries the paging cursor: %q", got)
	}
	if strings.Contains(got, "limit") {
		t.Errorf("the tail's filter query carries the page size: %q", got)
	}
	for _, want := range []string{"provider=alpha", "since=24h"} {
		if !strings.Contains(got, want) {
			t.Errorf("the tail's filter query %q is missing %q", got, want)
		}
	}
}
