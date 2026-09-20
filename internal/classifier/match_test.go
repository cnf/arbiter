package classifier

import (
	"context"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

// titleGenRequest is the real shape of a title-generation request: the
// signature lives in the SYSTEM PROMPT, and the last user message is the
// conversation being titled — which looks like ordinary chat.
func titleGenRequest() *types.NormalizedRequest {
	return &types.NormalizedRequest{
		SystemPrompt: "You are a title generator. You will be given a conversation and must produce a short title for it.",
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{types.TextBlock("how do I reverse a linked list in go?")}},
		},
	}
}

// mustMatcher builds a matcher that fills an axis value, the pre-kind shape.
// kindMatcher below is its kind-only counterpart.
func mustMatcher(t *testing.T, patterns []types.MatchPattern, value string, where []string, decisive bool) *RequestMatcher {
	t.Helper()
	m, err := NewRequestMatcher(patterns, value, "", where, decisive)
	if err != nil {
		t.Fatalf("NewRequestMatcher: %v", err)
	}
	return m
}

// mustKindMatcher builds a matcher that records a request kind and fills no
// axis — the shape a title-gen signature uses.
func mustKindMatcher(t *testing.T, patterns []types.MatchPattern, kind string, where []string, decisive bool) *RequestMatcher {
	t.Helper()
	m, err := NewRequestMatcher(patterns, "", kind, where, decisive)
	if err != nil {
		t.Fatalf("NewRequestMatcher: %v", err)
	}
	return m
}

// The point of the whole feature: the identifying text is in the system prompt,
// which no classifier read before — the user-message text is messages-only, and
// the identifying text is not in a message at all. This is
// the case where the two disagree by construction, so a matcher that silently
// searched the last user message instead would pass every other test here and
// fail only on real traffic.
func TestMatcherFindsSignatureInSystemPromptNotLastUserMessage(t *testing.T) {
	hc := NewHeuristicClassifierWithMatch("kind", AxisDomain, nil,
		mustMatcher(t, []types.MatchPattern{{Pattern: "You are a title generator."}}, "title_generation", nil, false))

	sig, err := hc.Classify(context.Background(), titleGenRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "title_generation" {
		t.Fatalf("Domain = %q, want title_generation (matched via the system prompt)", sig.Domain)
	}
	// A structural hit is certain, unlike a keyword guess.
	if sig.Confidence != 1.0 {
		t.Fatalf("Confidence = %v, want 1.0 for an exact match", sig.Confidence)
	}
	if sig.AxisConfidence[AxisDomain] != 1.0 {
		t.Fatalf("AxisConfidence = %v, want domain 1.0", sig.AxisConfidence)
	}
}

// A matcher that does not hit must fall through to the keywords, not report
// nothing: otherwise adding a title-gen rule would switch off classification
// for every request that is not a title generation.
func TestMatcherMissFallsThroughToKeywords(t *testing.T) {
	hc := NewHeuristicClassifierWithMatch("kind", AxisDomain,
		map[string][]string{"code_generation": {"refactor"}},
		mustMatcher(t, []types.MatchPattern{{Pattern: "You are a title generator."}}, "title_generation", nil, false))

	sig, err := hc.Classify(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "code_generation" {
		t.Fatalf("Domain = %q, want the keyword result (code_generation) on a matcher miss", sig.Domain)
	}
}

// Several clients spell the same preamble differently, so patterns are OR-ed.
func TestMatcherPatternsAreOred(t *testing.T) {
	m := mustMatcher(t, []types.MatchPattern{
		{Pattern: "You are a title generator."},
		{Pattern: "You are an expert title writer."},
	}, "title_generation", nil, false)

	req := &types.NormalizedRequest{SystemPrompt: "You are an expert title writer. Summarise the following."}
	if !m.Match(req) {
		t.Fatal("the second pattern did not match — patterns must be OR-ed, not AND-ed")
	}
}

// regex is the escape hatch for a signature that varies, and it is matched
// verbatim (case-sensitively unless the pattern says otherwise).
func TestMatcherRegexMode(t *testing.T) {
	m := mustMatcher(t, []types.MatchPattern{
		{Pattern: "(?i)you are (a|the) .{0,20}title (generator|writer)", Mode: "regex"},
	}, "title_generation", nil, false)

	if !m.Match(&types.NormalizedRequest{SystemPrompt: "You are a concise title writer v2."}) {
		t.Fatal("regex pattern did not match a variant the exact/prefix modes could not")
	}
	if m.Match(&types.NormalizedRequest{SystemPrompt: "You are a helpful assistant."}) {
		t.Fatal("regex matched an unrelated system prompt")
	}
}

// exact and prefix ignore case and surrounding whitespace, because the same
// text is re-serialized differently by different clients and wire formats —
// the shared matcher's rule, and this asserts the classifier inherits it.
func TestMatcherPrefixIgnoresCaseAndLeadingSpace(t *testing.T) {
	m := mustMatcher(t, []types.MatchPattern{{Pattern: "You are a title generator.", Mode: "prefix"}}, "title_generation", nil, false)

	for _, sp := range []string{
		"You are a title generator.",
		"YOU ARE A TITLE GENERATOR. Then more text.",
		"\n\n  you are a title generator.",
	} {
		if !m.Match(&types.NormalizedRequest{SystemPrompt: sp}) {
			t.Errorf("prefix did not match %q", sp)
		}
	}
}

// where: messages searches the message text instead of the system prompt.
func TestMatcherWhereMessages(t *testing.T) {
	m := mustMatcher(t, []types.MatchPattern{{Pattern: "summarise this conversation"}}, "subagent", []string{"messages"}, false)

	req := &types.NormalizedRequest{
		SystemPrompt: "You are a helpful assistant.",
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{types.TextBlock("summarise this conversation for me")}},
		},
	}
	if !m.Match(req) {
		t.Fatal("where: messages did not search the message text")
	}
	// And a system-prompt-only request must not match when messages is the only
	// target — the two targets are not interchangeable.
	if m.Match(&types.NormalizedRequest{SystemPrompt: "summarise this conversation"}) {
		t.Fatal("where: messages also matched the system prompt")
	}
}

// countingClassifier counts how many times it was consulted, and fills nothing.
type countingClassifier struct{ calls int }

func (c *countingClassifier) Classify(context.Context, *types.NormalizedRequest) (types.Signals, error) {
	c.calls++
	return types.Signals{}, nil
}

// A decisive matcher hit ends classification: the classifiers behind it do not
// run, which is the whole reason to mark a signature decisive — an exact match
// is not a candidate for keyword voting or a paid model call.
func TestMergedClassifierDecisiveMatchSkipsTheRest(t *testing.T) {
	counter := &countingClassifier{}
	probe := NewHeuristicClassifierWithMatch("kind", AxisDomain, nil,
		mustMatcher(t, []types.MatchPattern{{Pattern: "You are a title generator."}}, "title_generation", nil, true))
	merged := NewMergedClassifier("merged", []Classifier{probe, counter})

	if _, err := merged.Classify(context.Background(), titleGenRequest()); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if counter.calls != 0 {
		t.Fatalf("downstream classifier ran %d times, want 0 after a decisive hit", counter.calls)
	}
}

// The critical counterpart: a decisive matcher that did NOT match must fall
// through. Getting this wrong would let one title-gen rule switch off
// classification for every request that is not a title generation.
func TestMergedClassifierDecisiveMissStillRunsTheRest(t *testing.T) {
	counter := &countingClassifier{}
	probe := NewHeuristicClassifierWithMatch("kind", AxisDomain, nil,
		mustMatcher(t, []types.MatchPattern{{Pattern: "You are a title generator."}}, "title_generation", nil, true))
	merged := NewMergedClassifier("merged", []Classifier{probe, counter})

	sig, err := merged.Classify(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if counter.calls != 1 {
		t.Fatalf("downstream classifier ran %d times, want 1 after a decisive MISS", counter.calls)
	}
	if sig.Domain == "title_generation" {
		t.Fatal("a decisive pattern filled its value on a request it did not match")
	}
}

// A non-decisive matcher must NOT short-circuit, so the default is the safe
// behaviour: adding a match block does not change what else runs.
func TestMergedClassifierNonDecisiveMatchDoesNotSkip(t *testing.T) {
	counter := &countingClassifier{}
	probe := NewHeuristicClassifierWithMatch("kind", AxisDomain, nil,
		mustMatcher(t, []types.MatchPattern{{Pattern: "You are a title generator."}}, "title_generation", nil, false))
	merged := NewMergedClassifier("merged", []Classifier{probe, counter})

	if _, err := merged.Classify(context.Background(), titleGenRequest()); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if counter.calls != 1 {
		t.Fatalf("downstream classifier ran %d times, want 1 (non-decisive must not skip)", counter.calls)
	}
}
