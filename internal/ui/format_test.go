package ui

import "testing"

// TestFmtBytes checks the SI-prefix boundaries (#63's tool-defs size
// display): decimal (1000-based) units, not binary (1024-based) ones, to
// match how fmtChars/fmtTokens already size things on this page.
func TestFmtBytes(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1000, "1.0 kB"},
		{1500, "1.5 kB"},
		{999_999, "1000.0 kB"},
		{1_000_000, "1.0 MB"},
		{2_500_000, "2.5 MB"},
		{1_000_000_000, "1.0 GB"},
	}
	for _, c := range cases {
		if got := fmtBytes(c.n); got != c.want {
			t.Errorf("fmtBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
