package types

import "unicode/utf8"

// Ellipsize shortens s to at most max bytes on a rune boundary, marking that it
// was cut. Byte-truncating alone would split a multi-byte rune and put invalid
// UTF-8 into the store and onto the page; the cut is marked with "…" so a
// truncated value is never mistaken for the whole one.
//
// A max <= 0 means "no limit" and returns s unchanged. Shared by the routing
// rationale's preview and by a classifier's outbound input cap, which are the
// same operation on different budgets — keeping one implementation is what
// stops the two from disagreeing about rune boundaries.
func Ellipsize(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	// Walk back to the start of the rune straddling the boundary.
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
