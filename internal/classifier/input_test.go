package classifier

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/pkg/types"
)

// agenticToolResultRequest is the request shape that produced the defect: a
// conversation whose LAST user turn carries only a tool_result block, which is
// what an agentic client sends on every turn following a tool call.
//
// types.ExtractText returns "" for such a turn, so any selector that reads "the
// last user message" hands the classifier an empty string.
func agenticToolResultRequest() *types.NormalizedRequest {
	return &types.NormalizedRequest{
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{types.TextBlock("what does this function do?")}},
			{Role: "assistant", Content: []types.ContentBlock{types.TextBlock("It parses the config.")}},
			{Role: "user", Content: []types.ContentBlock{
				{Type: "tool_result", ToolResultForID: "t1", ToolResult: "file contents here"},
			}},
		},
	}
}

// TestClassifierInputSkipsToolResultOnlyTurn is the regression test for the
// defect: the input is the first user turn with text, not the last user turn.
//
// Reverting to a last-turn selector fails this with got == "" — the empty input
// that made a decisions call answer "none of the options fit" at ~0.8
// confidence, twelve times, on one session.
func TestClassifierInputSkipsToolResultOnlyTurn(t *testing.T) {
	got := classifierInput(agenticToolResultRequest(), 0)
	if got == "" {
		t.Fatalf("classifierInput = %q, want the first user turn's text.\n"+
			"An empty input is not a no-op: the model answers anyway, and the verdict "+
			"is stored and rendered like any other.", got)
	}
	if got != "what does this function do?" {
		t.Errorf("classifierInput = %q, want %q", got, "what does this function do?")
	}
}

// TestClassifierInputIsEmptyOnlyWhenNothingHasText pins the other side: a
// request with no text anywhere still yields "", so the skip below has
// something to key on.
func TestClassifierInputIsEmptyOnlyWhenNothingHasText(t *testing.T) {
	req := &types.NormalizedRequest{
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{
				{Type: "tool_result", ToolResultForID: "t1", ToolResult: "output"},
			}},
		},
	}
	if got := classifierInput(req, 0); got != "" {
		t.Errorf("classifierInput = %q, want \"\" for a request with no text block", got)
	}
}

// TestClassifierInputTruncatesHeadAndMarksTheCut proves the cap keeps the head
// and marks the truncation, so a stored prompt shows that it was cut rather
// than looking like the whole message.
func TestClassifierInputTruncatesHeadAndMarksTheCut(t *testing.T) {
	long := strings.Repeat("a", 500) + "TAIL_MARKER"
	req := &types.NormalizedRequest{
		Messages: []types.Message{{Role: "user", Content: []types.ContentBlock{types.TextBlock(long)}}},
	}
	got := classifierInput(req, 100)
	if len(got) > 100+len("…") {
		t.Errorf("classifierInput returned %d bytes, want at most the cap plus the marker", len(got))
	}
	if !strings.HasPrefix(got, strings.Repeat("a", 100)) {
		t.Errorf("classifierInput = %q..., want the HEAD kept (truncation drops the tail)", got[:20])
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("classifierInput = %q, want a cut marker", got)
	}
	if strings.Contains(got, "TAIL_MARKER") {
		t.Error("classifierInput kept the tail, want it dropped")
	}
}

// TestEffectiveMaxInputCharsDefaultsAndSentinels pins the three-way rule the
// config relies on: unset takes the safe default (NOT unlimited — that was the
// defect), negative is the explicit opt-out, positive is honoured.
func TestEffectiveMaxInputCharsDefaultsAndSentinels(t *testing.T) {
	if got := effectiveMaxInputChars(0); got != defaultMaxInputChars {
		t.Errorf("effectiveMaxInputChars(0) = %d, want the default %d — unset must not mean unlimited", got, defaultMaxInputChars)
	}
	if got := effectiveMaxInputChars(-1); got != 0 {
		t.Errorf("effectiveMaxInputChars(-1) = %d, want 0 (unlimited, asked for explicitly)", got)
	}
	if got := effectiveMaxInputChars(1234); got != 1234 {
		t.Errorf("effectiveMaxInputChars(1234) = %d, want 1234", got)
	}
}

// TestDecisionsClassifierSkipsCallWhenNothingToClassify is the primary
// regression test, on the classifier that actually produced the bad rows.
//
// It asserts the CALL NEVER HAPPENED, not merely that the result was empty: the
// defect was a real upstream call returning a fabricated verdict, so a test
// that only checked the signals would pass even while the call went out.
func TestDecisionsClassifierSkipsCallWhenNothingToClassify(t *testing.T) {
	client := &fakeDecisionClient{responses: map[string]*types.DecisionResponse{
		testProvider: decisionReply(map[string]types.DecisionAnswer{
			"domain": choice("chat", 0.99, nil),
		}),
	}}
	c := newTestDecisionsClassifier(t, client)

	req := &types.NormalizedRequest{
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{
				{Type: "tool_result", ToolResultForID: "t1", ToolResult: "output"},
			}},
		},
	}
	sig, err := c.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(client.calls) != 0 {
		t.Errorf("the decision endpoint was called %d time(s) with nothing to classify (providers: %v); "+
			"an empty state makes the model answer anyway, and that verdict is stored as a real one",
			len(client.calls), client.calls)
	}
	if sig.Domain != "" {
		t.Errorf("Domain = %q, want empty", sig.Domain)
	}
	for _, call := range sig.ClassifierCalls {
		if call != nil && call.Input != "" {
			t.Errorf("recorded call has Input %q, want empty", call.Input)
		}
	}
}

// TestDecisionsClassifierSendsFirstTurnNotToolResult is the other half: when
// there IS text to classify, the state sent is the first user turn — so the
// request is classified on what it is about, and a tool_result tail cannot
// starve it.
func TestDecisionsClassifierSendsFirstTurnNotToolResult(t *testing.T) {
	client := &fakeDecisionClient{responses: map[string]*types.DecisionResponse{
		testProvider: decisionReply(map[string]types.DecisionAnswer{
			"domain": choice("code_generation", 0.9, nil),
		}),
	}}
	c := newTestDecisionsClassifier(t, client)

	if _, err := c.Classify(context.Background(), agenticToolResultRequest()); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("Decide called %d times, want 1", len(client.requests))
	}
	if got := client.requests[0].State; got != "what does this function do?" {
		t.Errorf("state sent = %q, want the first user turn's text", got)
	}
}

// TestDecisionsClassifierCapsSentState proves the cap reaches the wire, not
// just the recorded copy: an uncapped input is what the upstream rejected with
// max_tokens_exceeded at 111KB.
func TestDecisionsClassifierCapsSentState(t *testing.T) {
	client := &fakeDecisionClient{responses: map[string]*types.DecisionResponse{
		testProvider: decisionReply(map[string]types.DecisionAnswer{"domain": choice("chat", 0.9, nil)}),
	}}
	c := NewDecisionsClassifierFull(
		"domain-decisions", decisionsResolver("jev", testProvider), "jev",
		client, map[string]types.ProviderConfig{testProvider: decisionsProvider(testProvider)},
		[]DecisionQuestionConfig{{
			Name: "domain", Axis: AxisDomain, Type: types.DecisionChoice,
			Labels: domainLabels(), Escape: "none",
		}},
		NewHeuristicClassifier("fb", AxisDomain, nil), 5*time.Second, 64,
	)

	huge := strings.Repeat("x", 50000)
	req := &types.NormalizedRequest{
		Messages: []types.Message{{Role: "user", Content: []types.ContentBlock{types.TextBlock(huge)}}},
	}
	if _, err := c.Classify(context.Background(), req); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("Decide called %d times, want 1", len(client.requests))
	}
	state, _ := client.requests[0].State.(string)
	if len(state) > 64+len("…") {
		t.Errorf("state sent to the endpoint is %d bytes, want at most the cap plus the marker", len(state))
	}
	if state == "" {
		t.Error("state sent to the endpoint is empty, want the (capped) first user turn")
	}
}

// TestLLMClassifierSkipsCallWhenNothingToClassify is the same skip for the
// other model-backed type — both must be fixed, since both read the same
// selector.
func TestLLMClassifierSkipsCallWhenNothingToClassify(t *testing.T) {
	u := &fakeUpstream{responses: map[string]*types.NormalizedResponse{
		"primary": reply("chat"),
	}}
	c := NewLLMClassifier("t", AxisDomain, pinnedResolver(), "classify", u, testProviders(), bareLabels, "", "", fakeHeuristic{domain: "chat"}, time.Second)

	req := &types.NormalizedRequest{
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{
				{Type: "tool_result", ToolResultForID: "t1", ToolResult: "output"},
			}},
		},
	}
	sig, err := c.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(u.calls) != 0 {
		t.Errorf("upstream called %d time(s) with nothing to classify (providers: %v)", len(u.calls), u.calls)
	}
	// The fallback still runs — that is the configured behaviour for a
	// classifier that produced no verdict of its own.
	if sig.Domain != "chat" {
		t.Errorf("Domain = %q, want the fallback's %q", sig.Domain, "chat")
	}
}

// TestHeuristicClassifierMatchesFirstTurnNotToolResult covers the cheapest
// classifier, which had the same empty-selection bug one layer down: a
// tool_result-only last turn matched no keyword at all.
func TestHeuristicClassifierMatchesFirstTurnNotToolResult(t *testing.T) {
	hc := NewHeuristicClassifier("kw", AxisDomain, map[string][]string{
		"code_generation": {"function"},
	})

	sig, err := hc.Classify(context.Background(), agenticToolResultRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "code_generation" {
		t.Errorf("Domain = %q, want code_generation — the keyword is in the FIRST user turn, "+
			"and reading only the last (tool_result-only) turn matches nothing", sig.Domain)
	}
}
