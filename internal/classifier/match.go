package classifier

import (
	"fmt"

	"github.com/cnf/arbiter/pkg/types"
)

// RequestMatcher matches a request's own text — its system prompt or its
// messages — and reports what a hit means: an axis value to fill, a request
// kind, or both.
//
// This exists because the text that identifies WHAT a request is can live
// somewhere no classifier read. A title generator's system prompt begins
// "You are a title generator.", and that signature is in req.SystemPrompt,
// while every classifier up to now matched types.LastUserText(req) — messages
// only. For a title-gen request the last user message is the conversation
// itself, which looks like ordinary chat, so the identifying text was invisible
// by construction rather than merely unmatched.
//
// It is a capability of the classifier, not a classifier type, because the
// signature is a property of the request rather than of any one classifier's
// job: a heuristic, an llm or a decisions classifier can all be told "if the
// request looks like this, it is this". And it is deliberately NOT a guardrail:
// a guardrail's ApplyPre returns a request and an error with no channel for
// routing signals, and pre-guardrails run before classify, so a guardrail
// emitting a signal would be emitting into a phase that already happened.
//
// Matching is on the TEXT, not on a client identity, for the reason the
// prompt_rewrite guardrail already gives: a per-client rule says "this client
// injects X", while matching X directly survives a client renaming itself.
type RequestMatcher struct {
	// patterns are OR-ed: any hit matches. A list rather than one pattern
	// because the same request kind is spelled slightly differently by
	// different clients — each variation is one config entry, so adding a new
	// client's signature is a one-line edit rather than a code change.
	patterns []*types.TextMatcher

	// value is the axis value filled on a match. May be empty when the
	// signature identifies a kind rather than a content axis (see kind).
	value string

	// kind is the request kind recorded on a match — "title", later
	// "subagent". Independent of value: a title request fills a kind and no
	// domain, because "what is this request about" has no meaningful answer
	// for a title generator. See types.Signals.RequestKind.
	kind string

	// where selects which parts of the request are searched. Defaults to
	// system-only, matching the guardrail's default and for the same reason:
	// the interesting signatures are preambles.
	where []types.MatchTarget

	// decisive, when true, marks a hit as an exact verdict: it ends
	// classification, so classifiers declared after this one do not run.
	// See MergedClassifier.
	decisive bool
}

// NewRequestMatcher compiles a matcher from already-parsed config values. An
// empty pattern list is an error rather than a no-op: a matcher that can never
// match is config the operator believes is doing something.
//
// value and kind are each optional but at least one is required — a matcher
// that hits and reports nothing is that same no-op by another route. A kind-only
// matcher is the normal shape for a signature that identifies what a request IS
// rather than what it is about.
func NewRequestMatcher(patterns []types.MatchPattern, value, kind string, where []string, decisive bool) (*RequestMatcher, error) {
	if len(patterns) == 0 {
		return nil, fmt.Errorf("match needs at least one pattern")
	}
	if value == "" && kind == "" {
		return nil, fmt.Errorf("match needs a %q (the axis value a hit fills) or a %q (the request kind a hit records)", "value", "kind")
	}
	m := &RequestMatcher{value: value, kind: kind, decisive: decisive}
	for _, p := range patterns {
		tm, err := types.NewTextMatcher(p.Pattern, types.MatchMode(p.Mode))
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", p.Pattern, err)
		}
		m.patterns = append(m.patterns, tm)
	}
	targets, err := types.ParseMatchTargets(where)
	if err != nil {
		return nil, err
	}
	m.where = targets
	return m, nil
}

// Match reports whether any pattern hits the request.
//
// The matched text is deliberately not returned. It would only be useful for a
// rationale, and the text that identified the request is already stored: with
// capture_content on, the system prompt is captured on the request's own row,
// and the value this fills is what the UI's axis column and the router's
// rationale show. Returning it would mean threading a string through two types
// for evidence that already exists.
func (m *RequestMatcher) Match(req *types.NormalizedRequest) bool {
	if m == nil || req == nil {
		return false
	}
	if types.Searches(m.where, types.TargetSystem) {
		// The prompt as the CLIENT sent it, not as the guardrails left it. A
		// system_prompt guardrail prepends its own text before classification
		// runs, so matching req.SystemPrompt would look for a signature behind
		// Arbiter's injected preamble — and fail, silently, in a way that looks
		// exactly like a wrong pattern. Falling back to SystemPrompt keeps this
		// correct for a caller that never set the field (a test, a direct
		// construction).
		system := req.ClientSystemPrompt
		if system == "" {
			system = req.SystemPrompt
		}
		if system != "" {
			for _, p := range m.patterns {
				if p.Matches(system) {
					return true
				}
			}
		}
	}
	if types.Searches(m.where, types.TargetMessages) {
		for _, msg := range req.Messages {
			for _, b := range msg.Content {
				if b.Type != "text" {
					continue
				}
				for _, p := range m.patterns {
					if p.Matches(b.Text) {
						return true
					}
				}
			}
		}
	}
	return false
}

// Value is the axis value a hit fills. Empty for a kind-only matcher, which
// fills no axis — see Kind.
func (m *RequestMatcher) Value() string {
	if m == nil {
		return ""
	}
	return m.value
}

// Kind is the request kind a hit records — "title", later "subagent". Empty
// for a matcher that only fills an axis. See types.Signals.RequestKind.
func (m *RequestMatcher) Kind() string {
	if m == nil {
		return ""
	}
	return m.kind
}

// Decisive reports whether a hit should end classification.
func (m *RequestMatcher) Decisive() bool {
	return m != nil && m.decisive
}
