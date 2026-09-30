package types

import (
	"fmt"
	"regexp"
	"strings"
)

// MatchMode says how a pattern is compared against text.
type MatchMode string

const (
	// MatchExact requires the searched text to equal the pattern, ignoring
	// case and surrounding whitespace. This anchors to the WHOLE field, not to
	// a paragraph or line within it — a multi-source system prompt (Arbiter's
	// own framing followed by a client's preamble, say) will never satisfy
	// this unless the pattern is the entire field. Use MatchParagraphExact or
	// MatchLineExact when the pattern is one paragraph/line among several.
	MatchExact MatchMode = "exact"
	// MatchPrefix requires the searched text to START with the pattern,
	// ignoring case and leading whitespace. This anchors to byte 0 of the
	// WHOLE field: it is the common case for an injected preamble only when
	// that preamble is the first thing in the field. The moment anything
	// precedes it (another source concatenated first), this silently never
	// matches. Use MatchLinePrefix or MatchParagraphPrefix when the target
	// text opens a line/paragraph but not the whole field.
	MatchPrefix MatchMode = "prefix"
	// MatchLineExact is like MatchExact but anchors to each line of the text
	// (split on "\n") rather than the whole field: it matches when any one
	// line, trimmed, equals the pattern.
	MatchLineExact MatchMode = "line_exact"
	// MatchLinePrefix is like MatchPrefix but anchors to each line of the
	// text rather than the whole field: it matches when any one line, after
	// trimming leading whitespace, starts with the pattern.
	MatchLinePrefix MatchMode = "line_prefix"
	// MatchParagraphExact is like MatchExact but anchors to each paragraph —
	// a chunk delimited by one or more blank lines — rather than the whole
	// field: it matches when any one paragraph, trimmed, equals the pattern.
	MatchParagraphExact MatchMode = "paragraph_exact"
	// MatchParagraphPrefix is like MatchPrefix but anchors to each paragraph
	// rather than the whole field: it matches when any one paragraph, after
	// trimming leading whitespace, starts with the pattern. This is the mode
	// #40 was filed for: a multi-source system prompt where the target text
	// is a whole paragraph but not the first thing in the field.
	MatchParagraphPrefix MatchMode = "paragraph_prefix"
	// MatchRegex compiles the pattern as a Go regular expression and uses the
	// first match's byte offsets. The escape hatch for a signature that varies
	// (a version number, a path, a date).
	MatchRegex MatchMode = "regex"
)

// MatchTarget names a part of a request that can be searched.
type MatchTarget string

const (
	// TargetSystem searches NormalizedRequest.SystemPrompt.
	TargetSystem MatchTarget = "system"
	// TargetMessages searches the text blocks of every message.
	TargetMessages MatchTarget = "messages"
	// TargetAll searches both.
	TargetAll MatchTarget = "all"
)

// ValidMatchModes and ValidMatchTargets exist so an unknown value is a
// load-time error listing the alternatives rather than a silent no-op.
var ValidMatchModes = []MatchMode{
	MatchExact, MatchPrefix,
	MatchLineExact, MatchLinePrefix,
	MatchParagraphExact, MatchParagraphPrefix,
	MatchRegex,
}
var ValidMatchTargets = []MatchTarget{TargetSystem, TargetMessages, TargetAll}

// TextMatcher is a compiled text pattern with its comparison mode.
//
// It lives here rather than in the package that first needed it (guardrail's
// prompt_rewrite) because a second consumer arrived: a classifier matching a
// request's own text — a title generator's system prompt, say — wants the same
// three modes with the same case and whitespace rules. Two implementations of
// "does this text start with this pattern" would drift, and the drift would be
// invisible: both would keep working, just on slightly different definitions of
// a match. This is the same reasoning that put ParseLabels here.
type TextMatcher struct {
	// Pattern is the configured text, kept for error messages and for
	// re-serializing a config.
	Pattern string
	// Mode is how Pattern is compared.
	Mode MatchMode

	// re is the compiled form, non-nil only for MatchRegex.
	re *regexp.Regexp
}

// NewTextMatcher compiles a pattern. An empty pattern is an error: it would
// match everything, which is never what an operator means and is a footgun in
// both directions (a stripper would delete the whole prompt; a classifier would
// label every request).
func NewTextMatcher(pattern string, mode MatchMode) (*TextMatcher, error) {
	if pattern == "" {
		return nil, fmt.Errorf("match pattern is required (an empty pattern would match everything)")
	}
	if mode == "" {
		// Prefix is the default because it is what an injected preamble almost
		// always is: a fixed opening followed by the client's own text.
		mode = MatchPrefix
	}
	if !containsMode(ValidMatchModes, mode) {
		return nil, fmt.Errorf("unknown match mode %q (want one of %s)", mode, joinModes(ValidMatchModes))
	}
	m := &TextMatcher{Pattern: pattern, Mode: mode}
	if mode == MatchRegex {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regex %q: %w", pattern, err)
		}
		m.re = re
	}
	return m, nil
}

// Find locates the pattern in text and returns its byte offsets, or nil when it
// does not match.
//
// exact and prefix are case-insensitive and ignore surrounding whitespace,
// because the same text is re-serialized differently by different clients and by
// different wire formats — a case or trailing-space difference is not a
// different prompt. regex is used verbatim, since the operator wrote the pattern
// precisely and Go's regexp already offers (?i) when case-insensitivity is
// wanted there.
func (m *TextMatcher) Find(text string) []int {
	if text == "" || m == nil {
		return nil
	}
	switch m.Mode {
	case MatchRegex:
		return m.re.FindStringIndex(text)
	case MatchExact:
		if strings.EqualFold(strings.TrimSpace(text), strings.TrimSpace(m.Pattern)) {
			return []int{0, len(text)}
		}
		return nil
	case MatchPrefix:
		trimmed := strings.TrimLeft(text, " 	\r\n")
		offset := len(text) - len(trimmed)
		if len(trimmed) >= len(m.Pattern) && strings.EqualFold(trimmed[:len(m.Pattern)], m.Pattern) {
			return []int{offset, offset + len(m.Pattern)}
		}
		return nil
	case MatchLineExact:
		for _, ln := range lineSpans(text) {
			seg := text[ln.start:ln.end]
			if strings.EqualFold(strings.TrimSpace(seg), strings.TrimSpace(m.Pattern)) {
				return []int{ln.start, ln.end}
			}
		}
		return nil
	case MatchLinePrefix:
		for _, ln := range lineSpans(text) {
			seg := text[ln.start:ln.end]
			trimmed := strings.TrimLeft(seg, " 	\r")
			offset := ln.start + (len(seg) - len(trimmed))
			if len(trimmed) >= len(m.Pattern) && strings.EqualFold(trimmed[:len(m.Pattern)], m.Pattern) {
				return []int{offset, offset + len(m.Pattern)}
			}
		}
		return nil
	case MatchParagraphExact:
		for _, p := range paragraphSpans(text) {
			seg := text[p.start:p.end]
			if strings.EqualFold(strings.TrimSpace(seg), strings.TrimSpace(m.Pattern)) {
				return []int{p.start, p.end}
			}
		}
		return nil
	case MatchParagraphPrefix:
		for _, p := range paragraphSpans(text) {
			seg := text[p.start:p.end]
			trimmed := strings.TrimLeft(seg, " 	\r\n")
			offset := p.start + (len(seg) - len(trimmed))
			if len(trimmed) >= len(m.Pattern) && strings.EqualFold(trimmed[:len(m.Pattern)], m.Pattern) {
				return []int{offset, offset + len(m.Pattern)}
			}
		}
		return nil
	default:
		return nil
	}
}

// textSpan is a half-open byte range [start, end) into a piece of text.
type textSpan struct{ start, end int }

// lineSpans splits text on "\n" and returns each line's byte range, excluding
// the newline itself. A text with no trailing newline still gets a final span
// for its last (possibly empty) line, so every byte of text belongs to exactly
// one line.
func lineSpans(text string) []textSpan {
	var spans []textSpan
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			spans = append(spans, textSpan{start, i})
			start = i + 1
		}
	}
	spans = append(spans, textSpan{start, len(text)})
	return spans
}

// paragraphSpans groups lineSpans into paragraphs: maximal runs of consecutive
// non-blank lines, separated by one or more blank (whitespace-only) lines. A
// paragraph's span covers from its first line's start to its last line's end,
// so it may itself contain internal newlines.
//
// This is deliberately a different unit than prompt_rewrite's
// action:strip_paragraph, whose "paragraph" is whatever paragraph_boundary
// (default a single "\n") delimits — effectively a line. Here "paragraph"
// means the ordinary sense: text separated by a blank line, which is the unit
// a multi-source system prompt is actually built from (§#40).
func paragraphSpans(text string) []textSpan {
	lines := lineSpans(text)
	var spans []textSpan
	i := 0
	for i < len(lines) {
		if strings.TrimSpace(text[lines[i].start:lines[i].end]) == "" {
			i++
			continue
		}
		start := lines[i].start
		end := lines[i].end
		j := i + 1
		for j < len(lines) && strings.TrimSpace(text[lines[j].start:lines[j].end]) != "" {
			end = lines[j].end
			j++
		}
		spans = append(spans, textSpan{start, end})
		i = j
	}
	return spans
}

// Matches reports whether the pattern occurs in text at all.
func (m *TextMatcher) Matches(text string) bool {
	return m.Find(text) != nil
}

// ParseMatchTargets reads a `where:` list, defaulting to system-only when
// absent — the same default the prompt_rewrite guardrail established, so the
// two consumers cannot disagree about what an unspecified target means.
func ParseMatchTargets(raw []string) ([]MatchTarget, error) {
	if len(raw) == 0 {
		return []MatchTarget{TargetSystem}, nil
	}
	out := make([]MatchTarget, 0, len(raw))
	for _, w := range raw {
		t := MatchTarget(w)
		if !containsTarget(ValidMatchTargets, t) {
			return nil, fmt.Errorf("unknown where %q (want one of %s)", w, joinTargets(ValidMatchTargets))
		}
		out = append(out, t)
	}
	return out, nil
}

// Searches reports whether the target list includes t (or TargetAll).
func Searches(targets []MatchTarget, t MatchTarget) bool {
	for _, w := range targets {
		if w == t || w == TargetAll {
			return true
		}
	}
	return false
}

func containsMode(xs []MatchMode, want MatchMode) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func containsTarget(xs []MatchTarget, want MatchTarget) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func joinModes(xs []MatchMode) string {
	parts := make([]string, 0, len(xs))
	for _, x := range xs {
		parts = append(parts, string(x))
	}
	return strings.Join(parts, ", ")
}

func joinTargets(xs []MatchTarget) string {
	parts := make([]string, 0, len(xs))
	for _, x := range xs {
		parts = append(parts, string(x))
	}
	return strings.Join(parts, ", ")
}
