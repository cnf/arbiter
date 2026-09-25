package ui

import (
	"testing"
)

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