package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/logging"
)

// The keyset cursor's whole reason for existing is that ts alone is not enough
// to page by. These tests seed through the real SQLiteWriter — not insertRow —
// because the cursor compares the *stored text* of ts, and a hand-written
// timestamp would be a layout production never writes.

// openSeeded writes events through the real writer and opens a reader on the
// same file, returning both.
func openSeeded(t *testing.T, events ...Event) *Reader {
	t.Helper()
	path := filepath.Join(t.TempDir(), "paging.db")
	w, err := NewSQLiteWriter(path, logging.NewStdoutLogger("error"))
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	for _, ev := range events {
		w.Record(ev)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func event(ts time.Time, provider, model string) Event {
	return Event{Ts: ts, ArrivalTs: ts, TraceID: "t", Format: "openai", Provider: provider, Model: model,
		LatencyMs: 1, StatusCode: 200}
}

// TestKeysetPagingWalksTheWholeList hammers the property an id-only cursor
// breaks: rows sharing one timestamp. With ts DESC, id DESC and a second
// granularity in the stored text, a cursor that only compares id repeats or
// skips exactly these rows.
func TestKeysetPagingWalksTheWholeList(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	// Five rows in the same second, with distinct nanosecond fractions, plus
	// two in each of two earlier seconds.
	var events []Event
	for i := 0; i < 5; i++ {
		events = append(events, event(base.Add(time.Duration(i)*time.Millisecond), "p", "same-second"))
	}
	for i := 0; i < 2; i++ {
		events = append(events, event(base.Add(-time.Second), "p", "earlier"))
		events = append(events, event(base.Add(-2*time.Second), "p", "earliest"))
	}
	r := openSeeded(t, events...)

	seen := map[int64]int{}
	const pageSize = 2
	var cursor RequestFilter
	pages := 0
	for {
		rows, err := r.ListRequests(context.Background(), RequestFilter{
			Limit: pageSize, BeforeTs: cursor.BeforeTs, BeforeID: cursor.BeforeID,
		})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			seen[row.ID]++
			if row.ArrivalTsRaw == "" {
				t.Fatal("ArrivalTsRaw is empty; the cursor has nothing to carry")
			}
		}
		last := rows[len(rows)-1]
		cursor = RequestFilter{BeforeTs: last.ArrivalTsRaw, BeforeID: last.ID}
		pages++
		if pages > 20 {
			t.Fatal("paging did not terminate")
		}
	}

	if len(seen) != len(events) {
		t.Errorf("paged over %d distinct rows, want %d", len(seen), len(events))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("row %d appeared %d times; the cursor repeats rows", id, n)
		}
	}

	// And the order the pages produced is the list's own: newest first.
	first, err := r.ListRequests(context.Background(), RequestFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Model != "same-second" {
		t.Errorf("newest row is %q, want the same-second group", first[0].Model)
	}
	if first[len(first)-1].Model != "earliest" {
		t.Errorf("oldest row is %q, want the earliest group", first[len(first)-1].Model)
	}
}

// TestKeysetCursorSurvivesAFractionalTimestamp pins the specific landmine: a
// cursor carrying the stored text of a row with nanoseconds must select
// strictly older rows. A bound rebuilt from a Go time (or a fraction-free
// prefix) would either drop or repeat the rows in that same second.
func TestKeysetCursorSurvivesAFractionalTimestamp(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 29, 33, 123456789, time.UTC)
	r := openSeeded(t,
		event(base, "p", "fractional"),
		event(base.Truncate(time.Second), "p", "whole-second"),
	)

	rows, err := r.ListRequests(context.Background(), RequestFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2", len(rows))
	}
	// The fractional row is the newer of the two within the same second.
	newest := rows[0]
	if newest.Model != "fractional" {
		t.Fatalf("newest = %q, want fractional", newest.Model)
	}
	if newest.ArrivalTsRaw == "" || newest.ArrivalTsRaw == rows[1].ArrivalTsRaw {
		t.Fatalf("stored text for the two rows is not distinct: %q vs %q", newest.ArrivalTsRaw, rows[1].ArrivalTsRaw)
	}

	rest, err := r.ListRequests(context.Background(), RequestFilter{
		BeforeTs: newest.ArrivalTsRaw, BeforeID: newest.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0].Model != "whole-second" {
		t.Errorf("continuation = %v, want just the whole-second row", rest)
	}
}

// TestSessionKeylessFilter proves the absent-key case is expressible and that
// it intersects with the other filters rather than replacing them.
func TestSessionKeylessFilter(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	withKey := event(base, "a", "keyed")
	withKey.SessionKey = "s1"
	noKey := event(base.Add(time.Second), "a", "unpinned")
	noKey.SessionKey = ""
	emptyKey := event(base.Add(2*time.Second), "b", "empty")
	emptyKey.SessionKey = ""
	r := openSeeded(t, withKey, noKey, emptyKey)

	cases := []struct {
		name   string
		filter RequestFilter
		want   int
	}{
		{"keyless", RequestFilter{SessionKeyless: true}, 2},
		{"keyed", RequestFilter{SessionKey: "s1"}, 1},
		{"keyless+provider", RequestFilter{SessionKeyless: true, Provider: "a"}, 1},
		{"keyless+provider with no rows", RequestFilter{SessionKeyless: true, Provider: "zz"}, 0},
	}
	for _, tc := range cases {
		got, err := r.ListRequests(context.Background(), tc.filter)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(got) != tc.want {
			t.Errorf("%s returned %d rows, want %d", tc.name, len(got), tc.want)
		}
	}
}

// A half-set cursor is ignored, not half-applied: silently paging from a
// nonsense position is worse than ignoring it.
func TestHalfCursorIsIgnored(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	r := openSeeded(t, event(base, "p", "a"), event(base.Add(time.Second), "p", "b"))

	rows, err := r.ListRequests(context.Background(), RequestFilter{BeforeID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Errorf("a before_id with no before_ts filtered the list to %d rows; want it ignored", len(rows))
	}
}
