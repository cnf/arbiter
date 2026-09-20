package types

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestEllipsizeCutsOnRuneBoundary is the reason the helper exists rather than a
// plain slice: cutting at a byte offset would split a multi-byte rune and put
// invalid UTF-8 into the store and onto the page.
func TestEllipsizeCutsOnRuneBoundary(t *testing.T) {
	// Each "é" is 2 bytes, so a 5-byte budget lands mid-rune without the fix.
	s := strings.Repeat("é", 10)
	got := Ellipsize(s, 5)
	if !utf8.ValidString(got) {
		t.Errorf("Ellipsize produced invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("Ellipsize = %q, want a cut marker so a truncated value isn't mistaken for the whole one", got)
	}
	if len(got) > 5+len("…") {
		t.Errorf("Ellipsize = %q (%d bytes), want at most the budget plus the marker", got, len(got))
	}

	// A string under the budget is returned untouched — no marker, no change.
	if got := Ellipsize("short", 120); got != "short" {
		t.Errorf("Ellipsize = %q, want the input unchanged", got)
	}
}

// TestEllipsizeZeroMeansNoLimit pins the sentinel a classifier's input cap
// relies on: an unset cap must not truncate to nothing.
func TestEllipsizeZeroMeansNoLimit(t *testing.T) {
	for _, max := range []int{0, -1} {
		if got := Ellipsize("unchanged", max); got != "unchanged" {
			t.Errorf("Ellipsize(%q, %d) = %q, want the input unchanged", "unchanged", max, got)
		}
	}
}
