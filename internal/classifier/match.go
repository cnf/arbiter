package classifier

import (
	"fmt"

	"github.com/cnf/arbiter/pkg/types"
)

// RequestMatcher matches a request's own text — its system prompt or its
// messages — and reports the axis value to fill when it hits.
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

	// value is the axis value filled on a match.
	value string

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
func NewRequestMatcher(patterns []types.MatchPattern, value string, where []string, decisive bool) (*RequestMatcher, error) {
	if len(patterns) == 0 {
		return nil, fmt.Errorf("match needs at least one pattern")
	}
	if value == "" {
		return nil, fmt.Errorf("match needs a \"value\" — the axis value a hit fills")
	}
	m := &RequestMatcher{value: value, decisive: decisive}
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
	if types.Searches(m.where, types.TargetSystem) && req.SystemPrompt != "" {
		for _, p := range m.patterns {
			if p.Matches(req.SystemPrompt) {
				return true
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

// Value is the axis value a hit fills.
func (m *RequestMatcher) Value() string {
	if m == nil {
		return ""
	}
	return m.value
}

// Decisive reports whether a hit should end classification.
func (m *RequestMatcher) Decisive() bool {
	return m != nil && m.decisive
}
