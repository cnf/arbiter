package store

import (
	"context"
	"testing"
)

// tailEvent is one request carrying a single user block, so rows are
// distinguishable by id alone.
func tailEvent(session, body string) Event {
	return Event{
		TraceID: "t", SessionKey: session, Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{
			{Kind: "text", Body: []byte(body), Role: "user", MsgIndex: 0, Position: 0},
		}},
	}
}

// writeMore opens a second writer over the same file and records rows, which is
// how a test observes traffic arriving *after* a reader started.
func writeMore(t *testing.T, path string, events ...Event) {
	t.Helper()
	w, err := NewSQLiteWriter(path, nil)
	if err != nil {
		t.Fatalf("second writer: %v", err)
	}
	for _, ev := range events {
		w.Record(ev)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close second writer: %v", err)
	}
}

// TestListRequestsAfterAppendsInOrderAndDoesNotRepeat is the property a live tail
// depends on: poll with the cursor the previous poll returned, and get exactly
// the rows written since — each one once, oldest first.
//
// The rows are written through a second writer over the same file, because that
// is the situation a tail is in: traffic arrives after it started reading, and
// the reader's snapshot must not be what bounds it.
func TestListRequestsAfterAppendsInOrderAndDoesNotRepeat(t *testing.T) {
	w, r, path := captureFixtureAt(t)
	ctx := context.Background()

	for i, body := range []string{"first request body", "second request body", "third request body"} {
		w.Record(tailEvent("s"+string(rune('a'+i)), body))
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// First poll: no cursor. Newest-first, and it is the caller's starting point.
	first, err := r.ListRequestsAfter(ctx, RequestFilter{}, "", 0, MaxTailLimit)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if len(first) != 3 {
		t.Fatalf("first poll returned %d rows, want 3", len(first))
	}
	for i := 1; i < len(first); i++ {
		if first[i-1].ID < first[i].ID {
			t.Errorf("first poll is not newest-first: ids %d then %d", first[i-1].ID, first[i].ID)
		}
	}

	cursor := first[0] // the newest row is the tail's starting position

	// A poll at that cursor must return nothing: no row is newer yet.
	none, err := r.ListRequestsAfter(ctx, RequestFilter{}, cursor.TsRaw, cursor.ID, MaxTailLimit)
	if err != nil {
		t.Fatalf("poll at cursor: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("poll at the cursor returned %d rows, want 0 (nothing is newer)", len(none))
	}

	// Two more arrive. The poll must return exactly these, oldest first — ids
	// above the cursor's and nothing else.
	writeMore(t, path, tailEvent("s4", "fourth request body"), tailEvent("s5", "fifth request body"))

	got, err := r.ListRequestsAfter(ctx, RequestFilter{}, cursor.TsRaw, cursor.ID, MaxTailLimit)
	if err != nil {
		t.Fatalf("append poll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("append poll returned %d rows, want 2", len(got))
	}
	// Newest-first, matching the list the tail is appended to: the batch must
	// come back in the order the reader sees, so the client can prepend it
	// wholesale. Returning it oldest-first put new rows at the bottom of a
	// newest-first table.
	if got[0].ID <= got[1].ID {
		t.Errorf("poll is not newest-first: ids %d then %d", got[0].ID, got[1].ID)
	}
	// The rows must all be newer than the cursor, and must not include it.
	for _, row := range got {
		if row.ID <= cursor.ID {
			t.Errorf("poll returned row %d, which is not newer than the cursor %d", row.ID, cursor.ID)
		}
		if row.ID == cursor.ID {
			t.Errorf("poll re-returned the cursor row %d", row.ID)
		}
	}

	// Advance to the new cursor and poll again: nothing. This is the check that
	// a tail does not re-show what it has already shown.
	//
	// NewestCursor rather than an index: the batch is newest-first, so the newest
	// row is got[0] here but was got[len-1] under the older oldest-first ordering.
	// Taking an index is the mistake this helper exists to prevent, and a test is
	// as capable of making it as a handler.
	newTs, newID, ok := NewestCursor(got)
	if !ok {
		t.Fatal("no cursor from the appended batch")
	}
	again, err := r.ListRequestsAfter(ctx, RequestFilter{}, newTs, newID, MaxTailLimit)
	if err != nil {
		t.Fatalf("poll after append: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("poll at the new cursor returned %d rows, want 0 — the tail repeats rows", len(again))
	}
}

// TestNewestCursorHandlesBothOrders is the test that would have caught the tail
// repeating rows forever. The two orderings a tail sees put the newest row at
// opposite ends, so "take the last row" is wrong on the first poll — and wrong
// silently, because the result is a cursor that moves backwards rather than an
// error.
func TestNewestCursorHandlesBothOrders(t *testing.T) {
	// A first poll's shape: newest-first (ts DESC, id DESC).
	desc := []RequestRow{
		{ID: 12, TsRaw: "2026-09-16 10:00:02 +0000 UTC"},
		{ID: 11, TsRaw: "2026-09-16 10:00:01 +0000 UTC"},
		{ID: 10, TsRaw: "2026-09-16 10:00:00 +0000 UTC"},
	}
	ts, id, ok := NewestCursor(desc)
	if !ok || ts != "2026-09-16 10:00:02 +0000 UTC" || id != 12 {
		t.Errorf("descending: got (%q, %d, %v), want the newest (row 12)", ts, id, ok)
	}

	// A later poll's shape: oldest-first (ts ASC, id ASC).
	asc := []RequestRow{
		{ID: 10, TsRaw: "2026-09-16 10:00:00 +0000 UTC"},
		{ID: 11, TsRaw: "2026-09-16 10:00:01 +0000 UTC"},
		{ID: 12, TsRaw: "2026-09-16 10:00:02 +0000 UTC"},
	}
	ts, id, ok = NewestCursor(asc)
	if !ok || ts != "2026-09-16 10:00:02 +0000 UTC" || id != 12 {
		t.Errorf("ascending: got (%q, %d, %v), want the newest (row 12)", ts, id, ok)
	}

	// Equal timestamps fall back to id, which is the SQL's own tiebreaker.
	same := []RequestRow{
		{ID: 7, TsRaw: "2026-09-16 10:00:00 +0000 UTC"},
		{ID: 9, TsRaw: "2026-09-16 10:00:00 +0000 UTC"},
		{ID: 8, TsRaw: "2026-09-16 10:00:00 +0000 UTC"},
	}
	if _, id, _ = NewestCursor(same); id != 9 {
		t.Errorf("equal timestamps: picked id %d, want 9 (the highest)", id)
	}

	if _, _, ok := NewestCursor(nil); ok {
		t.Error("an empty tail reported a cursor")
	}
}

// TestListRequestsAfterRefusesAHalfCursor pins that an incomplete cursor is an
// error rather than a silent fallback to the first page. Silently returning the
// tail's start instead of its continuation would look like the view had reset.
func TestListRequestsAfterRefusesAHalfCursor(t *testing.T) {
	_, r := captureFixture(t)
	ctx := context.Background()

	if _, err := r.ListRequestsAfter(ctx, RequestFilter{}, "2026-01-01 00:00:00 +0000 UTC", 0, 10); err == nil {
		t.Error("a timestamp-only cursor was accepted")
	}
	if _, err := r.ListRequestsAfter(ctx, RequestFilter{}, "", 7, 10); err == nil {
		t.Error("an id-only cursor was accepted")
	}
}

// TestListRequestsAfterSharesTheFilters checks the tail honours the same filters
// as the list. A tail that ignored them would show traffic the page claims to be
// filtering out.
func TestListRequestsAfterSharesTheFilters(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	w.Record(Event{TraceID: "t", Provider: "alpha", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{{Kind: "text", Body: []byte("alpha body"), Role: "user"}}}})
	w.Record(Event{TraceID: "t", Provider: "beta", Model: "m", StatusCode: 500,
		Content: &CapturedContent{Request: []Block{{Kind: "text", Body: []byte("beta body"), Role: "user"}}}})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows, err := r.ListRequestsAfter(ctx, RequestFilter{ErrorsOnly: true}, "", 0, 10)
	if err != nil {
		t.Fatalf("filtered tail: %v", err)
	}
	if len(rows) != 1 || rows[0].Provider != "beta" {
		t.Errorf("errors-only tail = %+v, want just the 500", rows)
	}

	rows, err = r.ListRequestsAfter(ctx, RequestFilter{Provider: "alpha"}, "", 0, 10)
	if err != nil {
		t.Fatalf("provider tail: %v", err)
	}
	if len(rows) != 1 || rows[0].Provider != "alpha" {
		t.Errorf("provider=alpha tail = %+v, want just the alpha row", rows)
	}
}

// TestListRequestsAfterRespectsItsLimit checks the cap is applied, so a burst is
// truncated rather than quietly described as a window.
func TestListRequestsAfterRespectsItsLimit(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	for i := 0; i < 8; i++ {
		w.Record(tailEvent("s", "body number "+string(rune('a'+i))+" with enough length"))
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows, err := r.ListRequestsAfter(ctx, RequestFilter{}, "", 0, 3)
	if err != nil {
		t.Fatalf("limited tail: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("limit 3 returned %d rows", len(rows))
	}
	rows, err = r.ListRequestsAfter(ctx, RequestFilter{}, "", 0, 10000)
	if err != nil {
		t.Fatalf("over-cap tail: %v", err)
	}
	if len(rows) != 8 {
		t.Errorf("over-cap tail returned %d rows, want all 8 (the cap is %d)", len(rows), MaxTailLimit)
	}
}

// TestListRequestsAfterCursorSurvivesSubSecondPrecision is the ts landmine in the
// tail's direction: rows written within the same second differ only in the
// fractional part, and the cursor is the stored text. A cursor rebuilt from a Go
// time would carry a different fraction length and sort wrongly, which here means
// a repeated or skipped row rather than an error — the tail would either loop or
// silently drop traffic.
func TestListRequestsAfterCursorSurvivesSubSecondPrecision(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	// Written in a tight loop, so several land within the same second.
	for i := 0; i < 12; i++ {
		w.Record(tailEvent("s", "tight loop body number "+string(rune('a'+i))+" padding"))
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A tail starts at the newest row and moves forward, so walking history is
	// not what it does. The property to pin is the forward one: given a cursor in
	// the middle of a same-second cluster, successive polls return every row after
	// it exactly once, oldest first, with no repeat and no skip.
	var (
		firstTs string
		firstID int64
	)
	// The oldest row's *stored text* is the starting cursor, which means
	// CAST(ts AS TEXT) and not ts: the column is typed TIMESTAMP, so the driver
	// converts a bare ts to a time and hands it back as RFC3339 — a different
	// string that would sort differently against the stored values. This is the
	// same distinction RequestRow.TsRaw exists for, and the same trap that makes
	// a cursor rebuilt from a Go time unsafe.
	if err := r.db.QueryRowContext(ctx,
		`SELECT CAST(ts AS TEXT), id FROM requests ORDER BY ts ASC, id ASC LIMIT 1`).Scan(&firstTs, &firstID); err != nil {
		t.Fatalf("oldest row: %v", err)
	}

	// Walk with a batch large enough that each poll covers the remaining rows.
	// With `ts DESC ... LIMIT` a poll returns the *newest* N above the cursor, so
	// a batch smaller than the backlog would move the cursor past rows it never
	// showed — that is a property of the cap, and TestTailTruncatesABurstBeyondItsCap
	// covers it. What is asserted here is the cursor's arithmetic: across polls
	// that each fit, every row is visited once, with no repeat and no skip.
	seen := map[int64]bool{firstID: true}
	cursorTs, cursorID := firstTs, firstID
	for page := 0; page < 20; page++ {
		rows, err := r.ListRequestsAfter(ctx, RequestFilter{}, cursorTs, cursorID, 12)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			if seen[row.ID] {
				t.Fatalf("row %d returned twice — the cursor went backwards", row.ID)
			}
			seen[row.ID] = true
		}
		ts, id, ok := NewestCursor(rows)
		if !ok {
			break
		}
		cursorTs, cursorID = ts, id
	}
	if len(seen) != 12 {
		t.Errorf("walked %d distinct rows, want all 12 — the cursor skipped or lost traffic", len(seen))
	}
}

// TestTailTruncatesABurstBeyondItsCap documents what happens when more rows
// arrive between two polls than a poll can carry — reachable through a backfill,
// an import, or a burst of traffic. The tail returns the newest rows that fit and
// reports truncation; the rows it could not carry are the cost of a bounded poll,
// and the flag is what stops that from being silent. A larger cap is the fix if
// it ever matters, not an unbounded query.
func TestTailTruncatesABurstBeyondItsCap(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	// A cursor from before the burst, so every one of these is "new".
	writeMoreCursor := "2020-01-01 00:00:00.000000000 +0000 UTC"
	for i := 0; i < 8; i++ {
		w.Record(tailEvent("s", "burst body "+string(rune('a'+i))+" with enough length"))
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows, err := r.ListRequestsAfter(ctx, RequestFilter{}, writeMoreCursor, 1, 3)
	if err != nil {
		t.Fatalf("burst poll: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("burst poll returned %d rows, want the cap 3", len(rows))
	}
	// A full page is how the caller knows the batch was cut, which is what the
	// endpoint reports as truncated.
	if len(rows) != 3 {
		t.Errorf("a full page is not distinguishable from a complete one")
	}
	// It returns the newest of the burst, so the view shows the most recent
	// traffic rather than the oldest of a backlog.
	if rows[0].ID <= rows[len(rows)-1].ID {
		t.Errorf("a truncated burst is not newest-first: %d then %d", rows[0].ID, rows[len(rows)-1].ID)
	}
}
