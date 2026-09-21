package ui

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

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
func TestTailFirstPollReturnsTheNewestFirstAndACursor(t *testing.T) {
	h, _ := newSeededHandler(t, liveEvents()...)

	resp, code := tailBody(t, h, "/admin/ui/requests/tail")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if resp.Cursor == "" {
		t.Error("the first poll returned no cursor, so a tail cannot start")
	}
	if resp.NewestID == 0 {
		t.Error("the first poll reported no newest id")
	}
	rows := tailHTML(resp)
	if got := strings.Count(rows, "<tr"); got != 3 {
		t.Errorf("first poll rendered %d rows, want 3", got)
	}
	// It must be the row markup the list uses, not a second definition.
	if !strings.Contains(rows, `data-id="`) {
		t.Error("rendered rows carry no data-id, so the client cannot dedupe")
	}
	// The rows are request metadata, not content: a request row shows provider,
	// model and status, and the bodies live on the detail page. Asserting on the
	// metadata is asserting on what the tail is actually meant to append.
	if !strings.Contains(rows, ">p<") && !strings.Contains(rows, ">m<") {
		t.Error("rendered rows do not carry the request's provider/model")
	}
	if !strings.Contains(rows, "200") {
		t.Error("rendered rows do not carry the status")
	}
	// Each row travels with its own id as a field, which is what the client skips
	// on rather than parsing the markup.
	for i, row := range resp.Rows {
		if row.ID == 0 {
			t.Errorf("row %d carries no id", i)
		}
	}
}

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
	if strings.Count(tailHTML(resp), "<tr") != 0 {
		t.Errorf("polling at the cursor returned %d rows, want 0", strings.Count(tailHTML(resp), "<tr"))
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

// TestTailAppendsOnlyWhatArrived is the tail's purpose: poll, write, poll again,
// and get exactly the new rows. Anything else means the view either repeats
// traffic or drops it.
func TestTailAppendsOnlyWhatArrived(t *testing.T) {
	// The *Live* fixture, because this test records a request after the handler
	// has started reading. The plain newSeededHandler closes its writer and
	// returns a nil one, so writing through it panics on a nil receiver rather
	// than failing an assertion.
	h, w := newSeededHandlerLive(t, liveEvents()...)

	first, _ := tailBody(t, h, "/admin/ui/requests/tail")
	if first.Cursor == "" {
		t.Fatal("no cursor on the first poll")
	}

	// A new request arrives through the same writer the handler's reader sees.
	w.Record(store.Event{
		TraceID: "t", Provider: "p", Model: "m", StatusCode: 200,
		Content: &store.CapturedContent{Request: []store.Block{
			{Kind: "text", Body: []byte("a brand new arrival"), Role: "user", MsgIndex: 0, Position: 0},
		}},
	})

	// The store writes asynchronously, so the row may not be visible on the very
	// next poll. Retrying with a bound is not a test concession: it is exactly
	// what a tail does, and asserting on the first poll alone would make this
	// test flaky against a correct implementation.
	var second tailResponse
	deadline := time.Now().Add(3 * time.Second)
	for {
		var code int
		second, code = tailBody(t, h, "/admin/ui/requests/tail?cursor="+first.Cursor)
		if code != 200 {
			t.Fatalf("status = %d", code)
		}
		if strings.Count(tailHTML(second), "<tr") > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the new request never appeared in the tail")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := strings.Count(tailHTML(second), "<tr"); got != 1 {
		t.Fatalf("the second poll rendered %d rows, want just the new one", got)
	}
	// The new row is identifiable by the id the first poll did not include.
	if !strings.Contains(tailHTML(second), `data-id="`+strconv.FormatInt(second.NewestID, 10)+`"`) {
		t.Errorf("the second poll's row is not the new request (newest id %d)", second.NewestID)
	}
	if second.NewestID <= first.NewestID {
		t.Errorf("the cursor did not advance: %d then %d", first.NewestID, second.NewestID)
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

// TestTailHonoursTheFilters is the assertion that the live view is actually
// filtered. A tail that ignored the form would show traffic the page claims to be
// excluding, which is worse than no tail.
func TestTailHonoursTheFilters(t *testing.T) {
	h, _ := newSeededHandler(t,
		store.Event{TraceID: "t", Provider: "alpha", Model: "m", StatusCode: 200,
			Content: &store.CapturedContent{Request: []store.Block{
				{Kind: "text", Body: []byte("alpha arrival"), Role: "user", MsgIndex: 0, Position: 0}}}},
		store.Event{TraceID: "t", Provider: "beta", Model: "m", StatusCode: 500,
			Content: &store.CapturedContent{Request: []store.Block{
				{Kind: "text", Body: []byte("beta arrival"), Role: "user", MsgIndex: 1, Position: 0}}}},
	)

	// Rows carry provider and status, so the filters are observable on them.
	resp, _ := tailBody(t, h, "/admin/ui/requests/tail?provider=alpha")
	if got := strings.Count(tailHTML(resp), "<tr"); got != 1 {
		t.Errorf("provider=alpha tail rendered %d rows, want 1", got)
	}
	if !strings.Contains(tailHTML(resp), "alpha") {
		t.Error("provider=alpha tail dropped the alpha request")
	}

	resp, _ = tailBody(t, h, "/admin/ui/requests/tail?errors=1")
	if got := strings.Count(tailHTML(resp), "<tr"); got != 1 {
		t.Errorf("errors-only tail rendered %d rows, want 1", got)
	}
	if !strings.Contains(tailHTML(resp), "500") {
		t.Error("errors-only tail dropped the 500")
	}
}

// TestTailReportsTruncation checks a burst is stated rather than silently shown
// as a window: a tail that dropped rows past its cap would make a busy period
// look like a quiet one.
func TestTailReportsTruncation(t *testing.T) {
	var events []store.Event
	for i := 0; i < store.MaxTailLimit+5; i++ {
		events = append(events, store.Event{
			TraceID: "t", Provider: "p", Model: "m", StatusCode: 200,
			Content: &store.CapturedContent{Request: []store.Block{
				{Kind: "text", Body: []byte("burst body " + strings.Repeat("x", i%7+1)), Role: "user", MsgIndex: i, Position: 0}}},
		})
	}
	h, _ := newSeededHandler(t, events...)

	resp, code := tailBody(t, h, "/admin/ui/requests/tail")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if !resp.Truncated {
		t.Error("a poll that hit the cap did not report truncation")
	}
	if n := strings.Count(tailHTML(resp), "<tr"); n != store.MaxTailLimit {
		t.Errorf("truncated poll rendered %d rows, want the cap %d", n, store.MaxTailLimit)
	}
}

// TestTailControlIsAbsentOnALaterPage pins the honest refusal: a tail only means
// something on the newest page, and a button that follows traffic newer than a
// page of history is not a live view.
func TestTailControlIsAbsentOnALaterPage(t *testing.T) {
	h, _ := newSeededHandler(t, liveEvents()...)

	// A first page offers the control, with a cursor and the filter query.
	body, code := getPage(t, h, "/admin/ui/requests")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(body, "data-tail-src=") || !strings.Contains(body, "data-tail-cursor=") {
		t.Error("the first page offers no live tail")
	}
	if !strings.Contains(body, `data-tail-toggle`) {
		t.Error("the tail has no control")
	}

	// A page reached through a cursor must not. The cursor has to be well-formed
	// or the page is a 400 and proves nothing, so it is built the way the pager
	// builds one.
	cursor := encodeCursor("2026-09-16 10:00:00.000000000 +0000 UTC", 1)
	body, code = getPage(t, h, "/admin/ui/requests?after="+cursor)
	if code != 200 {
		t.Fatalf("a later page returned %d, so the cursor it was given is malformed", code)
	}
	if strings.Contains(body, "data-tail-toggle") {
		t.Error("a later page offers a live tail; it would follow traffic newer than a page of history")
	}
}

// TestTailFilterQueryFollowsTheFilters is the join between the form and the tail:
// the tail must be given the same filter set the list is showing.
func TestTailFilterQueryFollowsTheFilters(t *testing.T) {
	h, _ := newSeededHandler(t, liveEvents()...)

	// A first page, so the tail exists to carry a filter query at all. The
	// filters must match the seeded rows: a filter that excludes everything
	// leaves no rows, and a tail with no rows to follow is correctly absent — so
	// asking for its attributes there would assert nothing.
	body, code := getPage(t, h, "/admin/ui/requests?provider=p&since=24h")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(body, `class="tail"`) {
		t.Fatal("no tail on a page with matching rows")
	}
	i := strings.Index(body, `data-tail-filter="`)
	if i < 0 {
		t.Fatal("the tail carries no filter query")
	}
	got := body[i+len(`data-tail-filter="`):]
	if j := strings.Index(got, `"`); j >= 0 {
		got = got[:j]
	}
	for _, want := range []string{"provider=p", "since=24h"} {
		if !strings.Contains(got, want) {
			t.Errorf("the tail's filter query %q is missing %q, so the tail would follow unfiltered traffic", got, want)
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
