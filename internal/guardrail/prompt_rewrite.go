package guardrail

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// PromptRewriteGuardrail matches text a client injected into the system prompt
// and strips, replaces, or blocks it.
//
// This is the §3 "client-injected hidden-prompt stripping" mechanism. Client apps
// (opencode, Claude Code, …) prepend their own system prompts — prompts the
// operator did not write and often cannot see. Those prompts are re-sent on every
// request, so they inflate token counts, defeat prompt caching across clients,
// and can silently override the operator's own instructions.
//
// Matching is on the TEXT, not on a client identity. A per-client key would let
// an operator write "this client injects X"; matching X directly is both simpler
// and more robust, because it survives a client renaming itself. Per-client
// attribution is a separate concern (§4) and is not needed here.
type PromptRewriteGuardrail struct {
	name string

	// match is the pattern to look for.
	match string
	// matchRe is the compiled form, non-nil only for mode == regex.
	matchRe *regexp.Regexp

	mode   matchMode
	action rewriteAction

	// replacement is the text an `action: replace` substitutes in. Ignored by
	// every other action.
	replacement string

	// blockStatus is the HTTP status a `action: block` rejects with. Defaults to
	// 403 (a deliberate refusal, not a rate limit).
	blockStatus int

	// paragraphBoundary is the splitter used by `action: strip_paragraph`: the
	// smallest unit removed is one of these-delimited chunk, so a match in the
	// middle of a paragraph takes the whole paragraph rather than leaving a
	// half-sentence behind. Newline by default.
	paragraphBoundary string

	// where selects which parts of the request are searched.
	where []matchTarget
}

// matchMode says how a pattern is compared.
type matchMode string

const (
	// modeExact requires the searched text to equal the pattern, ignoring case
	// and surrounding whitespace. The whole block must be the injected prompt.
	modeExact matchMode = "exact"
	// modePrefix requires the searched text to START with the pattern. This is
	// the common case for an injected preamble: the client's prompt is a fixed
	// opening, possibly followed by more.
	modePrefix matchMode = "prefix"
	// modeRegex compiles the pattern as a Go regular expression and uses the
	// first match's byte offsets. The escape hatch for "smarter matching" — a
	// signature that varies (a version number, a path, a date).
	modeRegex matchMode = "regex"
)

// rewriteAction says what happens to a match.
type rewriteAction string

const (
	// actionStrip removes the matched text.
	actionStrip rewriteAction = "strip"
	// actionReplace substitutes Replacement for the matched text.
	actionReplace rewriteAction = "replace"
	// actionBlock refuses the request instead of rewriting it. The upstream call
	// never happens, so it costs nothing — which makes it a cheaper refusal than
	// any spend or rate guardrail, since those can only act after the fact.
	actionBlock rewriteAction = "block"
	// actionStripParagraph removes the whole paragraph containing the match. The
	// point of this over `strip` is that a pattern matching part of a sentence
	// leaves a dangling fragment; taking the enclosing paragraph leaves the rest
	// of the prompt coherent.
	actionStripParagraph rewriteAction = "strip_paragraph"
)

// matchTarget names a part of the request that can be searched.
type matchTarget string

const (
	// targetSystem searches req.SystemPrompt.
	targetSystem matchTarget = "system"
	// targetMessages searches the text blocks of every message.
	targetMessages matchTarget = "messages"
	// targetAll searches both.
	targetAll matchTarget = "all"
)

// validModes and validActions exist so an unknown value is a load-time error
// listing the alternatives, rather than a silent no-op. A guardrail that quietly
// does nothing is the worst outcome here: the operator believes injected text is
// being stripped while every request still carries it.
var validModes = []matchMode{modeExact, modePrefix, modeRegex}
var validActions = []rewriteAction{actionStrip, actionReplace, actionBlock, actionStripParagraph}
var validTargets = []matchTarget{targetSystem, targetMessages, targetAll}

// NewPromptRewriteGuardrail builds the guardrail from config values already
// extracted by the caller.
func NewPromptRewriteGuardrail(
	name, match, mode, action, replacement, paragraphBoundary string,
	blockStatus int,
	where []string,
) (*PromptRewriteGuardrail, error) {
	if name == "" {
		return nil, fmt.Errorf("prompt_rewrite guardrail needs a name")
	}
	if match == "" {
		return nil, fmt.Errorf("prompt_rewrite guardrail %q: match is required (an empty pattern would match everything)", name)
	}

	g := &PromptRewriteGuardrail{
		name:              name,
		match:             match,
		mode:              matchMode(mode),
		action:            rewriteAction(action),
		replacement:       replacement,
		paragraphBoundary: paragraphBoundary,
		blockStatus:       blockStatus,
	}

	if g.mode == "" {
		// Prefix is the default because it is what an injected preamble almost
		// always is: a fixed opening followed by the client's own instructions.
		g.mode = modePrefix
	}
	if !contains(validModes, g.mode) {
		return nil, fmt.Errorf("prompt_rewrite guardrail %q: unknown mode %q (want one of %s)", name, mode, join(validModes))
	}
	if g.action == "" {
		g.action = actionStrip
	}
	if !contains(validActions, g.action) {
		return nil, fmt.Errorf("prompt_rewrite guardrail %q: unknown action %q (want one of %s)", name, action, join(validActions))
	}
	if g.mode == modeRegex {
		re, err := regexp.Compile(match)
		if err != nil {
			return nil, fmt.Errorf("prompt_rewrite guardrail %q: invalid regex %q: %w", name, match, err)
		}
		g.matchRe = re
	}
	if g.action == actionReplace && g.replacement == "" {
		return nil, fmt.Errorf("prompt_rewrite guardrail %q: action replace needs a replacement (use strip to remove instead)", name)
	}
	if g.paragraphBoundary == "" {
		g.paragraphBoundary = "\n"
	}
	if g.blockStatus == 0 {
		g.blockStatus = 403
	}

	if len(where) == 0 {
		g.where = []matchTarget{targetSystem}
	} else {
		for _, w := range where {
			t := matchTarget(w)
			if !contains(validTargets, t) {
				return nil, fmt.Errorf("prompt_rewrite guardrail %q: unknown where %q (want one of %s)", name, w, join(validTargets))
			}
			g.where = append(g.where, t)
		}
	}
	return g, nil
}

func (g *PromptRewriteGuardrail) Name() string { return g.name }

// ShouldRun reports whether there is anything to search. A request with no
// system prompt and no messages has nothing to match against, so the guardrail
// is skipped rather than reported as applied — an "applied" log line for a
// no-op would make the log useless for telling whether stripping is working.
func (g *PromptRewriteGuardrail) ShouldRun(req *types.NormalizedRequest) bool {
	if len(req.Messages) > 0 {
		return true
	}
	return req.SystemPrompt != ""
}

func (g *PromptRewriteGuardrail) ApplyPre(ctx context.Context, req *types.NormalizedRequest) (*types.NormalizedRequest, error) {
	searchesSystem := g.searches(targetSystem)
	searchesMessages := g.searches(targetMessages)

	// Block is checked first and independently of rewriting: a request that
	// matches a block rule is refused whether or not it also matches a strip
	// rule, and refusing before mutating means the request is untouched when the
	// error propagates.
	if g.action == actionBlock {
		if searchesSystem && g.find(req.SystemPrompt) != nil {
			return nil, arbitererrors.NewGuardrailError(
				fmt.Sprintf("guardrail %q: request blocked by prompt match", g.name), g.blockStatus, nil)
		}
		if searchesMessages {
			for _, m := range req.Messages {
				for _, b := range m.Content {
					if b.Type == "text" && g.find(b.Text) != nil {
						return nil, arbitererrors.NewGuardrailError(
							fmt.Sprintf("guardrail %q: request blocked by prompt match", g.name), g.blockStatus, nil)
					}
				}
			}
		}
		return req, nil
	}

	if searchesSystem {
		req.SystemPrompt = g.rewrite(req.SystemPrompt)
	}
	if searchesMessages {
		for i := range req.Messages {
			for j := range req.Messages[i].Content {
				b := &req.Messages[i].Content[j]
				if b.Type == "text" {
					b.Text = g.rewrite(b.Text)
				}
			}
		}
	}
	return req, nil
}

func (g *PromptRewriteGuardrail) ApplyPost(ctx context.Context, resp *types.NormalizedResponse, route types.Route) (*types.NormalizedResponse, error) {
	// No-op for post — this guardrail only shapes outbound requests. A response
	// guardrail is a different concern.
	return resp, nil
}

func (g *PromptRewriteGuardrail) searches(t matchTarget) bool {
	for _, w := range g.where {
		if w == t || w == targetAll {
			return true
		}
	}
	return false
}

// rewrite applies the configured action to one piece of text, returning it
// unchanged when nothing matched.
func (g *PromptRewriteGuardrail) rewrite(text string) string {
	if text == "" {
		return text
	}
	switch g.action {
	case actionStrip:
		return g.strip(text)
	case actionReplace:
		return g.replace(text)
	case actionStripParagraph:
		return g.stripParagraph(text)
	default:
		return text
	}
}

// strip removes the matched span, leaving the surrounding text.
func (g *PromptRewriteGuardrail) strip(text string) string {
	loc := g.find(text)
	if loc == nil {
		return text
	}
	return text[:loc[0]] + text[loc[1]:]
}

// replace substitutes the configured replacement for the matched span.
func (g *PromptRewriteGuardrail) replace(text string) string {
	loc := g.find(text)
	if loc == nil {
		return text
	}
	return text[:loc[0]] + g.replacement + text[loc[1]:]
}

// stripParagraph removes the whole paragraph containing the match, then tidies
// the seam.
//
// The smallest unit removed is one paragraph-delimited chunk, so a pattern that
// matches part of a sentence takes the enclosing paragraph with it rather than
// leaving a dangling fragment — which is the whole reason to prefer this over
// strip. When the match is the entire text (one paragraph), the result is empty,
// which is the correct outcome: there was nothing but injected text.
func (g *PromptRewriteGuardrail) stripParagraph(text string) string {
	loc := g.find(text)
	if loc == nil {
		return text
	}

	start := strings.LastIndex(text[:loc[0]], g.paragraphBoundary)
	if start < 0 {
		start = 0
	} else {
		start += len(g.paragraphBoundary)
	}
	end := strings.Index(text[loc[1]:], g.paragraphBoundary)
	if end < 0 {
		end = len(text)
	} else {
		end += loc[1]
	}

	return tidySeam(text[:start], text[end:])
}

// tidySeam joins the text on either side of a removed paragraph without leaving
// a blank-line crater or a leading newline. Removing a paragraph from the middle
// would otherwise leave three consecutive newlines where one belonged.
func tidySeam(before, after string) string {
	before = strings.TrimRight(before, "\n")
	after = strings.TrimLeft(after, "\n")
	switch {
	case before == "":
		return after
	case after == "":
		return before
	default:
		return before + "\n\n" + after
	}
}

// find locates the configured pattern in text and returns its byte offsets, or
// nil when it does not match.
//
// exact and prefix are case-insensitive and ignore surrounding whitespace,
// because a client's preamble is re-serialized differently by different clients
// and by different wire formats — a case or trailing-space difference is not a
// different prompt. regex is used verbatim, since the operator wrote the pattern
// precisely and Go's regexp already offers (?i) when case-insensitivity is
// wanted there.
func (g *PromptRewriteGuardrail) find(text string) []int {
	if text == "" {
		return nil
	}
	switch g.mode {
	case modeRegex:
		return g.matchRe.FindStringIndex(text)
	case modeExact:
		if strings.EqualFold(strings.TrimSpace(text), strings.TrimSpace(g.match)) {
			return []int{0, len(text)}
		}
		return nil
	case modePrefix:
		trimmed := strings.TrimLeft(text, " \t\r\n")
		offset := len(text) - len(trimmed)
		if len(trimmed) >= len(g.match) && strings.EqualFold(trimmed[:len(g.match)], g.match) {
			return []int{offset, offset + len(g.match)}
		}
		return nil
	default:
		return nil
	}
}

func contains[T comparable](xs []T, want T) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func join[T comparable](xs []T) string {
	parts := make([]string, 0, len(xs))
	for _, x := range xs {
		parts = append(parts, fmt.Sprint(x))
	}
	return strings.Join(parts, ", ")
}
