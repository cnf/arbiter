package types

import "testing"

// #40: prefix/exact anchor to the WHOLE field, so they silently never match
// once anything precedes the target text — the normal shape of a multi-source
// system prompt (Arbiter's own framing followed by a client's injected
// preamble). These tests pin the line/paragraph-anchored modes added to fix
// that, and confirm the original whole-field modes still behave as before.

func TestPrefixStillAnchorsToTheWholeFieldNotAParagraph(t *testing.T) {
	m, err := NewTextMatcher("You are opencode", MatchPrefix)
	if err != nil {
		t.Fatalf("NewTextMatcher: %v", err)
	}
	text := "You are Hermes, a general agent.\n\nYou are opencode, the best coding agent."
	if m.Matches(text) {
		t.Fatal("MatchPrefix matched a paragraph that isn't at byte 0 — it should only match the whole field's start")
	}
}

func TestExactStillRequiresTheWholeField(t *testing.T) {
	m, err := NewTextMatcher("You are opencode, the best coding agent.", MatchExact)
	if err != nil {
		t.Fatalf("NewTextMatcher: %v", err)
	}
	text := "You are Hermes, a general agent.\n\nYou are opencode, the best coding agent."
	if m.Matches(text) {
		t.Fatal("MatchExact matched when the pattern was only part of the field")
	}
}

func TestParagraphPrefixFindsAPreambleBehindOtherText(t *testing.T) {
	m, err := NewTextMatcher("You are opencode", MatchParagraphPrefix)
	if err != nil {
		t.Fatalf("NewTextMatcher: %v", err)
	}
	text := "You are Hermes, a general agent.\n\nYou are opencode, the best coding agent."
	loc := m.Find(text)
	if loc == nil {
		t.Fatal("MatchParagraphPrefix did not find a preamble that opens the second paragraph")
	}
	want := "You are opencode"
	if got := text[loc[0]:loc[1]]; got != want {
		t.Fatalf("matched span = %q, want %q", got, want)
	}
}

func TestParagraphPrefixIgnoresCaseAndLeadingWhitespace(t *testing.T) {
	m, err := NewTextMatcher("you are opencode", MatchParagraphPrefix)
	if err != nil {
		t.Fatalf("NewTextMatcher: %v", err)
	}
	text := "First.\n\n   You Are OpenCode, the rest of it."
	if !m.Matches(text) {
		t.Fatal("MatchParagraphPrefix should ignore case and a paragraph's leading whitespace")
	}
}

func TestParagraphExactMatchesOneWholeParagraphAmongSeveral(t *testing.T) {
	m, err := NewTextMatcher("You are opencode, the best coding agent.", MatchParagraphExact)
	if err != nil {
		t.Fatalf("NewTextMatcher: %v", err)
	}
	text := "You are Hermes, a general agent.\n\nYou are opencode, the best coding agent.\n\nUser-specific instructions follow."
	loc := m.Find(text)
	if loc == nil {
		t.Fatal("MatchParagraphExact did not find the exact middle paragraph")
	}
	if got := text[loc[0]:loc[1]]; got != "You are opencode, the best coding agent." {
		t.Fatalf("matched span = %q", got)
	}
}

func TestParagraphExactDoesNotMatchAPartialParagraph(t *testing.T) {
	m, err := NewTextMatcher("You are opencode", MatchParagraphExact)
	if err != nil {
		t.Fatalf("NewTextMatcher: %v", err)
	}
	text := "You are Hermes.\n\nYou are opencode, the best coding agent."
	if m.Matches(text) {
		t.Fatal("MatchParagraphExact matched a paragraph that only starts with the pattern — exact must require the whole paragraph")
	}
}

func TestLinePrefixFindsAPreambleOnASingleLineAmongOthers(t *testing.T) {
	m, err := NewTextMatcher("You are opencode", MatchLinePrefix)
	if err != nil {
		t.Fatalf("NewTextMatcher: %v", err)
	}
	text := "You are Hermes, a general agent.\nYou are opencode, the best coding agent.\nMore text."
	loc := m.Find(text)
	if loc == nil {
		t.Fatal("MatchLinePrefix did not find a preamble on the second line")
	}
	if got := text[loc[0]:loc[1]]; got != "You are opencode" {
		t.Fatalf("matched span = %q", got)
	}
}

func TestLineExactMatchesOneWholeLineAmongOthers(t *testing.T) {
	m, err := NewTextMatcher("You are opencode.", MatchLineExact)
	if err != nil {
		t.Fatalf("NewTextMatcher: %v", err)
	}
	text := "You are Hermes.\nYou are opencode.\nMore text."
	loc := m.Find(text)
	if loc == nil {
		t.Fatal("MatchLineExact did not find the exact middle line")
	}
	if got := text[loc[0]:loc[1]]; got != "You are opencode." {
		t.Fatalf("matched span = %q", got)
	}
}

func TestLineExactDoesNotMatchAPartialLine(t *testing.T) {
	m, err := NewTextMatcher("You are opencode", MatchLineExact)
	if err != nil {
		t.Fatalf("NewTextMatcher: %v", err)
	}
	if m.Matches("You are opencode, the best coding agent.") {
		t.Fatal("MatchLineExact matched a line that only starts with the pattern")
	}
}

func TestParagraphPrefixNoMatchReturnsNil(t *testing.T) {
	m, err := NewTextMatcher("not present anywhere", MatchParagraphPrefix)
	if err != nil {
		t.Fatalf("NewTextMatcher: %v", err)
	}
	if m.Find("First paragraph.\n\nSecond paragraph.") != nil {
		t.Fatal("expected no match")
	}
}

func TestNewModesAreAcceptedByNewTextMatcher(t *testing.T) {
	for _, mode := range []MatchMode{MatchLineExact, MatchLinePrefix, MatchParagraphExact, MatchParagraphPrefix} {
		if _, err := NewTextMatcher("x", mode); err != nil {
			t.Errorf("mode %q: NewTextMatcher: %v", mode, err)
		}
	}
}
