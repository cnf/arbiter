package classifier

import (
	"context"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

// hermesTitlePrompt is Hermes' real title-generation system prompt, verbatim as
// captured from a live request.
//
// It is here rather than invented because the earlier design note claimed the
// title generator's prompt begins "You are a title generator." — it does not.
// A pattern written against that claim matches nothing, and the failure is
// invisible: a match block that never hits looks exactly like a signature that
// was wrong, so the operator tunes the pattern instead of the assumption. Real
// strings, not paraphrases.
const hermesTitlePrompt = `You name chat sessions. Given the user's opening message, write a title that lets them find this conversation again in a list.

Rules:
- 3 to 7 words, sentence case (capitalize only the first word and proper nouns).
- Name what the user wants DONE, not that they asked a question.
- Keep technical terms, filenames, numbers, and error codes exact.
- Drop filler words: the, this, my, a, an.
- No trailing punctuation, no quotes, no tool names, no 'Title:' prefix.
- Never answer the message. Name it.
- Always produce something, even for a bare greeting.
- Write the title in the same language as the user's message.
Good: {"title": "Fix login button on mobile"}
Good: {"title": "Postgres connection pool exhaustion"}
Good: {"title": "Friendly greeting"}
Too vague: {"title": "Code changes"}
Too long: {"title": "Investigate and fix the issue where the login button does not respond on mobile devices"}`

// A title-gen request embeds the conversation it is titling as its user
// message, so the last user message looks like ordinary chat — which is why
// nothing matched before. The signature is the system prompt.
func hermesTitleRequest() *types.NormalizedRequest {
	return &types.NormalizedRequest{
		SystemPrompt: hermesTitlePrompt,
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{types.TextBlock("whats the deepest point of the thames river?")}},
		},
	}
}

// titleKindMatcher is the shape the deployment config uses: a signature that
// records the request KIND and fills no content axis.
//
// It fills no domain deliberately. "What is this request about" has no
// meaningful answer for a title generator — the conversation being titled is
// the payload, not the request's subject — and letting the signature claim
// `domain` would put a value in the axis column that no router rule should
// ever match on.
func titleKindMatcher(t *testing.T) *RequestMatcher {
	t.Helper()
	return mustKindMatcher(t, []types.MatchPattern{{Pattern: "You name chat sessions."}}, "title", nil, true)
}

// The real Hermes title prompt, matched by its actual opening. This is the
// pattern to deploy; the test exists so a future edit to the prompt is caught
// here rather than in production traffic.
func TestMatcherOnRealHermesTitlePrompt(t *testing.T) {
	hc := NewHeuristicClassifierWithMatch("request-kind", AxisDomain, nil, titleKindMatcher(t))

	sig, err := hc.Classify(context.Background(), hermesTitleRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.RequestKind != "title" {
		t.Fatalf("RequestKind = %q, want title — the real prompt did not match", sig.RequestKind)
	}
	// The other half of the shape: a kind-only signature must not also fill
	// the axis it is declared on. A value there would show in the axes column
	// as though the request had a content domain.
	if sig.Domain != "" {
		t.Fatalf("Domain = %q, want empty — a kind-only signature must fill no axis", sig.Domain)
	}
}

// The claim that was wrong, pinned so it cannot come back as an assumption: a
// prefix pattern for "You are a title generator." does NOT match Hermes' prompt.
// If a client with that signature appears later it needs its own pattern — the
// OR-list is exactly the mechanism for that.
func TestUnrelatedSignatureDoesNotMatchHermesPrompt(t *testing.T) {
	hc := NewHeuristicClassifierWithMatch("request-kind", AxisDomain, nil,
		mustKindMatcher(t, []types.MatchPattern{{Pattern: "You are a title generator."}}, "title", nil, true))

	sig, err := hc.Classify(context.Background(), hermesTitleRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.RequestKind == "title" {
		t.Fatal("a pattern for a different client's prompt matched Hermes' prompt — the test fixture is not the real string")
	}
}

// A broadening regex, for clients whose wording drifts around the same idea.
// Tested against the real prompt so it is known to cover it, not assumed to.
func TestBroadenedRegexCoversTheRealPrompt(t *testing.T) {
	hc := NewHeuristicClassifierWithMatch("request-kind", AxisDomain, nil,
		mustKindMatcher(t, []types.MatchPattern{
			{Pattern: "(?i)^\\s*you (name|title|summari[sz]e) (chat )?sessions?\\b", Mode: "regex"},
		}, "title", nil, true))

	sig, err := hc.Classify(context.Background(), hermesTitleRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.RequestKind != "title" {
		t.Fatalf("RequestKind = %q, want the broadened regex to cover the real prompt", sig.RequestKind)
	}

	// And it must not fire on an ordinary assistant preamble.
	ordinary := &types.NormalizedRequest{
		SystemPrompt: "You are a helpful assistant. Answer the user's question.",
		Messages:     []types.Message{{Role: "user", Content: []types.ContentBlock{types.TextBlock("hi")}}},
	}
	sig, err = hc.Classify(context.Background(), ordinary)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.RequestKind == "title" {
		t.Fatal("the broadened regex matched an ordinary assistant preamble")
	}
}

// A decisive match on the real prompt must stop the merge, so a title-gen
// request never pays for a domain classification it already knows the answer to.
//
// This is what keeps classification affordable on the literal-model path: the
// signature is free, and everything behind it — the decisions call, the LLM
// classifiers — is skipped for every request it identifies.
func TestDecisiveMatchOnRealPromptSkipsTheRest(t *testing.T) {
	counter := &countingClassifier{}
	probe := NewHeuristicClassifierWithMatch("request-kind", AxisDomain, nil, titleKindMatcher(t))
	merged := NewMergedClassifier("merged", []Classifier{probe, counter})

	sig, err := merged.Classify(context.Background(), hermesTitleRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if counter.calls != 0 {
		t.Fatalf("downstream classifier ran %d times, want 0 for a real title-gen request", counter.calls)
	}
	// The kind has to survive the merge, or the row still cannot say what it
	// was — which is the entire point of recording it.
	if sig.RequestKind != "title" {
		t.Fatalf("merged RequestKind = %q, want title", sig.RequestKind)
	}
}
