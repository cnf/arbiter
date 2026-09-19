package types

import "testing"

// The live config writes the regex inside a YAML double-quoted scalar, where
// backslashes are escape-processed. This pins what actually reaches regexp
// after YAML decoding, because "\\\\s" and "\\s" produce very different regexes
// and only one of them means whitespace.
func TestYAMLEscapingOfMatchRegex(t *testing.T) {
	// What the live config contains (four backslashes between the quotes), as
	// YAML decodes it. yaml.Unmarshal would produce a two-backslash string.
	const asGoSeesIt = `(?i)^\\s*you (name|title|summari[sz]e) (chat )?sessions?\\b`

	m, err := NewTextMatcher(asGoSeesIt, MatchRegex)
	if err != nil {
		t.Fatalf("NewTextMatcher: %v", err)
	}

	prompt := "You name chat sessions. Given the user's opening message, write a title."
	if m.Matches(prompt) {
		t.Fatal("a double-backslash regex matched — expected it NOT to: \\\\s means a literal backslash, not whitespace")
	}

	// The single-backslash form is what the operator actually wants.
	good, err := NewTextMatcher(`(?i)^\s*you (name|title|summari[sz]e) (chat )?sessions?\b`, MatchRegex)
	if err != nil {
		t.Fatalf("NewTextMatcher: %v", err)
	}
	if !good.Matches(prompt) {
		t.Fatal("the single-backslash regex did not match the real prompt")
	}
}

// The prefix pattern is the one that should carry the load, and it is written
// without any escaping — so if only the regex is broken, a config with both
// still matches. This pins that the prefix half works, which isolates where to
// look when the whole block misses.
func TestPrefixPatternUnaffectedByRegexEscaping(t *testing.T) {
	m, err := NewTextMatcher("You name chat sessions.", MatchPrefix)
	if err != nil {
		t.Fatalf("NewTextMatcher: %v", err)
	}
	if !m.Matches("You name chat sessions. Given the user's opening message, write a title.") {
		t.Fatal("prefix pattern did not match the real title prompt")
	}
}
