package store

import "strings"

// DiffOp is one line of a unified-style line diff: unchanged (kept as muted
// context), removed, or added. Line-level rather than word-level — a replace
// is expressed as an adjacent removed line followed by an added line, which
// also covers pure insertion and pure deletion without a separate mechanism
// for either (see DESIGN.md's "Session transcript" section).
type DiffOp struct {
	Kind string // "eq" | "del" | "add"
	Text string
}

// LineDiff computes a line-level diff between two texts, in original line
// order. It is a small hand-rolled LCS (longest common subsequence), not a
// dependency: this repo stays deliberately light on third-party packages
// (see go.mod), and a guardrail-rewritten prompt is at most a few hundred
// lines — well within an O(n*m) table.
//
// The result reconstructs the standard unified-diff read: unchanged lines
// give context, a run of "del" immediately followed by "add" reads as a
// replace, a "del" alone is a pure removal, an "add" alone is a pure
// insertion.
func LineDiff(before, after string) []DiffOp {
	a := splitLines(before)
	b := splitLines(after)
	n, m := len(a), len(b)

	// lcs[i][j] = length of the LCS of a[i:] and b[j:]. Built backward so the
	// walk below reads forward without reversing the result at the end.
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	out := make([]DiffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, DiffOp{Kind: "eq", Text: a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, DiffOp{Kind: "del", Text: a[i]})
			i++
		default:
			out = append(out, DiffOp{Kind: "add", Text: b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, DiffOp{Kind: "del", Text: a[i]})
	}
	for ; j < m; j++ {
		out = append(out, DiffOp{Kind: "add", Text: b[j]})
	}
	return out
}

// splitLines splits on "\n" without discarding empty lines — a blank line is
// real content in a diff (an added or removed paragraph break), not noise to
// collapse. strings.Split (not SplitAfter) is used so the "\n" itself never
// shows up doubled when a line is rendered.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
