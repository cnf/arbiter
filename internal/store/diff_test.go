package store

import (
	"reflect"
	"testing"
)

// LineDiff must reconstruct exactly what changed, in original line order, so
// the transcript's diff view reads like a normal unified diff: unchanged
// lines as context, a del immediately followed by an add as a replace.
func TestLineDiffPureInsertionAndDeletion(t *testing.T) {
	got := LineDiff("a\nb\nc", "a\nb\nx\nc")
	want := []DiffOp{
		{Kind: "eq", Text: "a"},
		{Kind: "eq", Text: "b"},
		{Kind: "add", Text: "x"},
		{Kind: "eq", Text: "c"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LineDiff(pure insertion) = %+v, want %+v", got, want)
	}

	got = LineDiff("a\nb\nc", "a\nc")
	want = []DiffOp{
		{Kind: "eq", Text: "a"},
		{Kind: "del", Text: "b"},
		{Kind: "eq", Text: "c"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LineDiff(pure deletion) = %+v, want %+v", got, want)
	}
}

// A replace — the common guardrail case, a system line's content changed in
// place — must show as an adjacent del/add pair, not as a shuffle of the
// surrounding context.
func TestLineDiffReplaceIsAdjacentDelAdd(t *testing.T) {
	got := LineDiff("preamble: client text\nrest unchanged", "preamble: arbiter text\nrest unchanged")
	want := []DiffOp{
		{Kind: "del", Text: "preamble: client text"},
		{Kind: "add", Text: "preamble: arbiter text"},
		{Kind: "eq", Text: "rest unchanged"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LineDiff(replace) = %+v, want %+v", got, want)
	}
}

// Two texts that are byte-identical produce an all-"eq" diff — the case a
// caller should not even present a "modified by guardrail" chip for, but the
// diff function itself must still degrade to a no-op rather than a spurious
// del/add pair.
func TestLineDiffIdenticalTextsAreAllEqual(t *testing.T) {
	got := LineDiff("same\ntext", "same\ntext")
	for _, op := range got {
		if op.Kind != "eq" {
			t.Errorf("LineDiff(identical) produced a %q op; want only eq", op.Kind)
		}
	}
}

// Empty-string edge cases must not panic (splitLines returns nil for "",
// not [""], or a single added/removed blank line would show up as a
// phantom diff on every unmodified single-line block).
func TestLineDiffEmptyInputs(t *testing.T) {
	if got := LineDiff("", ""); len(got) != 0 {
		t.Errorf("LineDiff(\"\", \"\") = %+v, want empty", got)
	}
	if got := LineDiff("", "new"); len(got) != 1 || got[0].Kind != "add" {
		t.Errorf("LineDiff(\"\", \"new\") = %+v, want one add", got)
	}
	if got := LineDiff("old", ""); len(got) != 1 || got[0].Kind != "del" {
		t.Errorf("LineDiff(\"old\", \"\") = %+v, want one del", got)
	}
}
