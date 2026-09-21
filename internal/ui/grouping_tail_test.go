package ui

import (
	"strings"
	"testing"
	"time"
)

// TestTailRowsCarryTheirGroupWhenGrouped is the join between the two halves of the
// live grouped view, and it is the one defect in this feature that no gate would
// catch: the tail's rows must travel with the group key of the line they belong to,
// because the client uses it to place a row into an existing line rather than
// prepending a duplicate one. A missing key is not an error anywhere — it just
// renders a second line for a group already on screen, which reads as the tail
// double-reporting traffic.
//
// The group is established by the *first* poll (three rows already in the store),
// so the second poll's single arrival joins a group whose key the client already
// holds.
func TestTailRowsCarryTheirGroupWhenGrouped(t *testing.T) {
	h, w := newSeededHandlerLive(t,
		seededTurn("a", "sess-1"),
		seededTurn("b", "sess-1"),
	)

	first, _ := tailBody(t, h, "/admin/ui/requests/tail")
	if !first.Grouped {
		t.Error("the grouped list's tail did not report itself as grouped")
	}
	if len(first.Rows) != 2 {
		t.Fatalf("first poll returned %d rows, want 2", len(first.Rows))
	}
	for i, row := range first.Rows {
		if row.Key == "" {
			t.Errorf("row %d arrived with no group key, so the client cannot find its line", i)
		}
	}
	if first.Rows[0].Key != first.Rows[1].Key {
		t.Errorf("two rows of one group carry different keys (%q, %q); the page would render two lines",
			first.Rows[0].Key, first.Rows[1].Key)
	}
	wantKey := first.Rows[0].Key

	// The client finds a line through the row's *markup*, not through the JSON key
	// field: it queries `tr[data-key="..."]` on the table. A row whose JSON carries
	// a key but whose HTML does not is unplaceable, and the symptom is a duplicate
	// line — so the attribute is asserted here rather than trusted.
	if !strings.Contains(first.Rows[0].HTML, "<tr") {
		t.Fatalf("the polled row rendered no markup at all: %q", first.Rows[0].HTML)
	}
	if !strings.Contains(first.Rows[0].HTML, `data-key="`+wantKey+`"`) {
		t.Errorf("the row's markup does not carry data-key=%q, so the client cannot find its line; html=%s",
			wantKey, first.Rows[0].HTML)
	}

	// A third turn of the same conversation arrives, and must come back with the
	// *same* key — that is the property the page depends on across polls.
	w.Record(seededTurn("c", "sess-1"))

	second := pollUntilRows(t, h, first.Cursor)
	if len(second.Rows) != 1 {
		t.Fatalf("second poll returned %d rows, want 1", len(second.Rows))
	}
	if got := second.Rows[0].Key; got != wantKey {
		t.Errorf("the new turn arrived with key %q, want %q (the line it belongs to)", got, wantKey)
	}
	if second.NewestKey != wantKey {
		t.Errorf("NewestKey = %q, want %q", second.NewestKey, wantKey)
	}
}

// TestTailRowCarriesItsGroupEvenAlone is the row-level half of the contract: a
// single streamed, session-pinned request still travels with its group key, even
// though it is not yet a collapsed line.
//
// This is what makes the user's sequence work — `A`, then `A` again — because the
// first A's line is on screen carrying this key when the second A arrives, so the
// client can fold the second into it instead of prepending a duplicate line. A row
// that arrived with no key could never be joined, and the live grouped view would
// grow a new line per turn, which is the defect the grouped view exists to prevent.
func TestTailRowCarriesItsGroupEvenAlone(t *testing.T) {
	h, _ := newSeededHandler(t, seededTurn("a", "sess-1"))

	resp, _ := tailBody(t, h, "/admin/ui/requests/tail")
	if len(resp.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(resp.Rows))
	}
	if resp.Rows[0].Key == "" {
		t.Error("a lone streamed request arrived with no group key, so the next turn of its conversation cannot join its line")
	}
	// The row must actually render. An empty fragment would satisfy a "contains"
	// check only by accident, and a tail sending blank rows would look like a
	// quiet period rather than a broken view.
	if !strings.Contains(resp.Rows[0].HTML, "<tr") {
		t.Fatalf("the row's HTML fragment is not a rendered row: %q", resp.Rows[0].HTML)
	}
	if !strings.Contains(resp.Rows[0].HTML, `data-key="`+resp.Rows[0].Key+`"`) {
		t.Errorf("the lone row's markup does not carry data-key=%q; the client could not find its line; html=%s",
			resp.Rows[0].Key, resp.Rows[0].HTML)
	}
	if resp.NewestKey != resp.Rows[0].Key {
		t.Errorf("NewestKey = %q, want the row's own key %q", resp.NewestKey, resp.Rows[0].Key)
	}
}

// TestUnpinnedAndNonStreamedTailRowsCarryNoGroup keeps the boundary on the wire:
// a row that can never share a line must not advertise one.
func TestUnpinnedAndNonStreamedTailRowsCarryNoGroup(t *testing.T) {
	unpinned := seededTurn("a", "")
	unpinned.SessionKey = ""
	nonStreamed := seededTurn("b", "sess-1")
	nonStreamed.Stream = false

	h, _ := newSeededHandler(t, unpinned, nonStreamed)
	resp, _ := tailBody(t, h, "/admin/ui/requests/tail")
	if len(resp.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(resp.Rows))
	}
	for i, row := range resp.Rows {
		if row.Key != "" {
			t.Errorf("row %d (id %d) carries group key %q but can never share a line", i, row.ID, row.Key)
		}
	}
}

// TestFlatTailReportsItselfUngrouped keeps the mode on the wire: the tail follows
// the list it sits under, so a flat list's tail must tell the client to prepend
// rather than place by group.
func TestFlatTailReportsItselfUngrouped(t *testing.T) {
	h, _ := newSeededHandler(t,
		seededTurn("a", "sess-1"),
		seededTurn("b", "sess-1"),
	)

	flat, _ := tailBody(t, h, "/admin/ui/requests/tail?flat=1")
	if flat.Grouped {
		t.Error("a flat list's tail reported itself as grouped")
	}
	for i, row := range flat.Rows {
		if row.Key != "" {
			t.Errorf("flat tail row %d carries group key %q with no lines to place it in", i, row.Key)
		}
	}

	// And the mode is what the page hands its tail, so the two cannot disagree.
	body, code := getPage(t, h, "/admin/ui/requests?flat=1")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if got := attrFor(body, "data-tail-filter"); got == "" || !strings.Contains(got, "flat=1") {
		t.Errorf("the flat page's tail filter query %q does not carry the mode", got)
	}
}

// pollUntilRows polls the tail until it returns a row, bounded, because the store
// writes asynchronously and a single immediate poll races it.
func pollUntilRows(t *testing.T, h *Handler, cursor string) tailResponse {
	t.Helper()
	for i := 0; i < 300; i++ {
		resp, code := tailBody(t, h, "/admin/ui/requests/tail?cursor="+cursor)
		if code != 200 {
			t.Fatalf("status = %d", code)
		}
		if len(resp.Rows) > 0 {
			return resp
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the new request never appeared in the tail")
	return tailResponse{}
}

// attrFor reads one attribute's value out of a rendered page.
func attrFor(body, attr string) string {
	marker := attr + `="`
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i+len(marker):]
	if j := strings.Index(rest, `"`); j >= 0 {
		return rest[:j]
	}
	return ""
}