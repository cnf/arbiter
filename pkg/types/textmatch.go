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
	// case and surrounding whitespace. The whole block must be the pattern.
	MatchExact MatchMode = "exact"
	// MatchPrefix requires the searched text to START with the pattern,
	// ignoring case and leading whitespace. The common case for a client's
	// injected preamble: a fixed opening, possibly followed by more.
	MatchPrefix MatchMode = "prefix"
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
var ValidMatchModes = []MatchMode{MatchExact, MatchPrefix, MatchRegex}
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
		trimmed := strings.TrimLeft(text, " \t\r\n")
		offset := len(text) - len(trimmed)
		if len(trimmed) >= len(m.Pattern) && strings.EqualFold(trimmed[:len(m.Pattern)], m.Pattern) {
			return []int{offset, offset + len(m.Pattern)}
		}
		return nil
	default:
		return nil
	}
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
