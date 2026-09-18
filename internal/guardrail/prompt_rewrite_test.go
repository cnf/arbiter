package guardrail

import (
	"context"
	"strings"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

// Prompt rewrite guardrails — the §3 "client-injected hidden-prompt stripping"
// mechanism. These are the package's first tests; the two pre-existing guardrails
// (system_prompt, rate_limit) are covered indirectly through the pipeline.

func newGuard(t *testing.T, match, mode, action, replacement string, where ...string) *PromptRewriteGuardrail {
	t.Helper()
	g, err := NewPromptRewriteGuardrail("test", match, mode, action, replacement, "", 0, where)
	if err != nil {
		t.Fatalf("NewPromptRewriteGuardrail: %v", err)
	}
	return g
}

func reqWithSystem(system string) *types.NormalizedRequest {
	return &types.NormalizedRequest{
		SystemPrompt: system,
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{{Type: "text", Text: "hello"}}},
		},
	}
}

// --- matching modes ---

// TestPrefixMatchesAnInjectedPreamble is the common real case: a client prepends
// a fixed opening and then adds its own instructions after it. Prefix mode must
// match the opening without requiring the whole block to equal the pattern.
func TestPrefixMatchesAnInjectedPreamble(t *testing.T) {
	g := newGuard(t, "You are opencode, the best coding agent", "prefix", "strip", "")
	req := reqWithSystem("You are opencode, the best coding agent. Follow these rules:\n- be concise")

	got, err := g.ApplyPre(context.Background(), req)
	if err != nil {
		t.Fatalf("ApplyPre: %v", err)
	}
	if strings.Contains(got.SystemPrompt, "You are opencode") {
		t.Errorf("injected preamble survived: %q", got.SystemPrompt)
	}
	// The client's own instructions after the preamble must survive — this is a
	// strip of the matched span, not a wipe of the whole block.
	if !strings.Contains(got.SystemPrompt, "be concise") {
		t.Errorf("strip removed more than the match: %q", got.SystemPrompt)
	}
}

// TestPrefixIgnoresLeadingWhitespaceAndCase proves the match is robust to how a
// client re-serializes its preamble. A case or leading-newline difference is not
// a different prompt, and failing to match would leave the text in place silently.
func TestPrefixIgnoresLeadingWhitespaceAndCase(t *testing.T) {
	g := newGuard(t, "you are opencode", "prefix", "strip", "")
	req := reqWithSystem("\n\n   You Are OpenCode — instructions follow")

	got, _ := g.ApplyPre(context.Background(), req)
	if strings.Contains(strings.ToLower(got.SystemPrompt), "you are opencode") {
		t.Errorf("match was case/whitespace sensitive; text survived: %q", got.SystemPrompt)
	}
}

// TestExactRequiresTheWholeBlock pins the distinction from prefix: exact is for a
// client whose entire system prompt IS the injected text.
func TestExactRequiresTheWholeBlock(t *testing.T) {
	g := newGuard(t, "You are opencode", "exact", "strip", "")

	// Whole block matches (modulo whitespace) -> stripped to empty.
	got, _ := g.ApplyPre(context.Background(), reqWithSystem("  You are opencode  "))
	if strings.TrimSpace(got.SystemPrompt) != "" {
		t.Errorf("exact match on the whole block left %q, want empty", got.SystemPrompt)
	}

	// Same pattern, longer block -> no match, so nothing is removed.
	got, _ = g.ApplyPre(context.Background(), reqWithSystem("You are opencode. Extra."))
	if !strings.Contains(got.SystemPrompt, "You are opencode") {
		t.Errorf("exact mode matched a longer block; it must require the whole text: %q", got.SystemPrompt)
	}
}

// TestRegexModeMatchesAVaryingSignature is the escape hatch the user asked for:
// a signature that varies between clients or versions, which prefix/exact cannot
// express.
//
// The character class matters and is not incidental: `\w` excludes the hyphen, so
// `you are \w+` fails to match "claude-code". Worth knowing when writing real
// patterns — a too-narrow class silently matches nothing.
func TestRegexModeMatchesAVaryingSignature(t *testing.T) {
	g := newGuard(t, `(?i)you are [\w-]+, the best coding agent`, "regex", "strip", "")
	req := reqWithSystem("You are claude-code, the best coding agent. Then more.")

	got, _ := g.ApplyPre(context.Background(), req)
	if strings.Contains(got.SystemPrompt, "best coding agent") {
		t.Errorf("regex did not strip: %q", got.SystemPrompt)
	}
	if !strings.Contains(got.SystemPrompt, "Then more") {
		t.Errorf("regex strip removed beyond the match: %q", got.SystemPrompt)
	}
}

// --- actions ---

// TestReplaceSubstitutesTheMatch proves replace leaves the surrounding text and
// puts the configured string in place of the match.
func TestReplaceSubstitutesTheMatch(t *testing.T) {
	g := newGuard(t, "You are opencode", "prefix", "replace", "[removed by arbiter]")
	req := reqWithSystem("You are opencode. Keep this.")

	got, _ := g.ApplyPre(context.Background(), req)
	if !strings.Contains(got.SystemPrompt, "[removed by arbiter]") {
		t.Errorf("replacement not inserted: %q", got.SystemPrompt)
	}
	if strings.Contains(got.SystemPrompt, "You are opencode") {
		t.Errorf("matched text survived a replace: %q", got.SystemPrompt)
	}
	if !strings.Contains(got.SystemPrompt, "Keep this") {
		t.Errorf("replace removed surrounding text: %q", got.SystemPrompt)
	}
}

// TestBlockRefusesTheRequest proves block returns an error rather than mutating.
// Refusing before any rewrite means the request is untouched when the error
// propagates, and the upstream call never happens.
func TestBlockRefusesTheRequest(t *testing.T) {
	g := newGuard(t, "You are opencode", "prefix", "block", "")
	req := reqWithSystem("You are opencode. Something else.")
	before := req.SystemPrompt

	_, err := g.ApplyPre(context.Background(), req)
	if err == nil {
		t.Fatal("expected block to refuse the request")
	}
	if req.SystemPrompt != before {
		t.Errorf("block mutated the request: %q -> %q", before, req.SystemPrompt)
	}
}

// TestBlockAppliesEvenWhenAStripWouldMatch proves block is not short-circuited by
// a non-matching strip: the action is block, so a match blocks regardless.
func TestBlockDoesNotRewrite(t *testing.T) {
	g := newGuard(t, "nothing here", "prefix", "block", "")
	req := reqWithSystem("unrelated prompt")

	if _, err := g.ApplyPre(context.Background(), req); err != nil {
		t.Errorf("block refused a request that did not match: %v", err)
	}
}

// TestStripParagraphRemovesTheEnclosingParagraph is the reason this action exists:
// a pattern matching part of a sentence leaves a dangling fragment under plain
// strip, so the enclosing paragraph goes instead.
func TestStripParagraphRemovesTheEnclosingParagraph(t *testing.T) {
	g := newGuard(t, "INJECTED", "regex", "strip_paragraph", "")
	req := reqWithSystem("First paragraph stays.\n\nA paragraph with INJECTED text in the middle.\n\nThird paragraph stays.")

	got, _ := g.ApplyPre(context.Background(), req)
	if strings.Contains(got.SystemPrompt, "INJECTED") {
		t.Errorf("injected text survived: %q", got.SystemPrompt)
	}
	// The whole enclosing paragraph must go, not just the matched word.
	if strings.Contains(got.SystemPrompt, "A paragraph with") {
		t.Errorf("only the match was removed, leaving a fragment: %q", got.SystemPrompt)
	}
	if !strings.Contains(got.SystemPrompt, "First paragraph stays") ||
		!strings.Contains(got.SystemPrompt, "Third paragraph stays") {
		t.Errorf("strip_paragraph removed neighbouring paragraphs: %q", got.SystemPrompt)
	}
}

// TestStripParagraphTidiesTheSeam proves removing a middle paragraph does not
// leave a crater of blank lines where it was.
func TestStripParagraphTidiesTheSeam(t *testing.T) {
	g := newGuard(t, "GONE", "regex", "strip_paragraph", "")
	req := reqWithSystem("A\n\nGONE\n\nB")

	got, _ := g.ApplyPre(context.Background(), req)
	if strings.Contains(got.SystemPrompt, "\n\n\n") {
		t.Errorf("removal left a blank-line crater: %q", got.SystemPrompt)
	}
	if got.SystemPrompt != "A\n\nB" {
		t.Errorf("SystemPrompt = %q, want %q", got.SystemPrompt, "A\n\nB")
	}
}

// TestStripParagraphOnTheWholeTextYieldsEmpty is the correct outcome when the
// request was nothing but injected text: there is nothing left to send.
func TestStripParagraphOnTheWholeTextYieldsEmpty(t *testing.T) {
	g := newGuard(t, "INJECTED", "regex", "strip_paragraph", "")
	req := reqWithSystem("INJECTED and nothing else")

	got, _ := g.ApplyPre(context.Background(), req)
	if strings.TrimSpace(got.SystemPrompt) != "" {
		t.Errorf("SystemPrompt = %q, want empty", got.SystemPrompt)
	}
}

// --- targets ---

// TestWhereSystemOnlyLeavesMessagesAlone proves the target selector is honoured:
// matching the system prompt must not touch message text, which is the client's
// actual conversation.
func TestWhereSystemOnlyLeavesMessagesAlone(t *testing.T) {
	g := newGuard(t, "remove me", "prefix", "strip", "", "system")
	req := &types.NormalizedRequest{
		SystemPrompt: "remove me and this",
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{{Type: "text", Text: "remove me from the user turn"}}},
		},
	}

	got, _ := g.ApplyPre(context.Background(), req)
	if strings.Contains(got.SystemPrompt, "remove me") {
		t.Errorf("system prompt not rewritten: %q", got.SystemPrompt)
	}
	if !strings.Contains(got.Messages[0].Content[0].Text, "remove me") {
		t.Errorf("messages were rewritten despite where=system: %q", got.Messages[0].Content[0].Text)
	}
}

// TestWhereMessagesRewritesTextBlocks proves the other direction, and that only
// text blocks are touched — a tool_use or attachment block is not free text and
// rewriting it would corrupt the request.
func TestWhereMessagesRewritesTextBlocks(t *testing.T) {
	g := newGuard(t, "drop this", "prefix", "strip", "", "messages")
	req := &types.NormalizedRequest{
		SystemPrompt: "drop this system text",
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{
				{Type: "text", Text: "drop this user text"},
				{Type: "tool_use", ToolName: "drop this tool", ToolUseID: "x"},
			}},
		},
	}

	got, _ := g.ApplyPre(context.Background(), req)
	if strings.Contains(got.Messages[0].Content[0].Text, "drop this") {
		t.Errorf("text block not rewritten: %q", got.Messages[0].Content[0].Text)
	}
	if !strings.Contains(got.SystemPrompt, "drop this") {
		t.Errorf("system prompt rewritten despite where=messages: %q", got.SystemPrompt)
	}
	if got.Messages[0].Content[1].ToolName != "drop this tool" {
		t.Errorf("a non-text block was rewritten: %+v", got.Messages[0].Content[1])
	}
}

// TestWhereAllCoversBoth proves the combined selector.
func TestWhereAllCoversBoth(t *testing.T) {
	g := newGuard(t, "zap", "prefix", "strip", "", "all")
	req := &types.NormalizedRequest{
		SystemPrompt: "zap system",
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{{Type: "text", Text: "zap message"}}},
		},
	}

	got, _ := g.ApplyPre(context.Background(), req)
	if strings.Contains(got.SystemPrompt, "zap") || strings.Contains(got.Messages[0].Content[0].Text, "zap") {
		t.Errorf("where=all did not cover both: system=%q msg=%q", got.SystemPrompt, got.Messages[0].Content[0].Text)
	}
}

// --- no-match and config validation ---

// TestNoMatchLeavesTextUntouched is the safety property: a guardrail that does not
// match must change nothing at all.
func TestNoMatchLeavesTextUntouched(t *testing.T) {
	g := newGuard(t, "not present", "prefix", "strip", "")
	req := reqWithSystem("an ordinary system prompt")
	before := req.SystemPrompt

	got, err := g.ApplyPre(context.Background(), req)
	if err != nil {
		t.Fatalf("ApplyPre: %v", err)
	}
	if got.SystemPrompt != before {
		t.Errorf("text changed on a non-match: %q -> %q", before, got.SystemPrompt)
	}
}

// TestUnknownModeIsALoadError is the failure mode that matters most here: a
// silently no-op guardrail would leave the operator believing injected text is
// stripped while every request still carries it.
func TestUnknownModeIsALoadError(t *testing.T) {
	if _, err := NewPromptRewriteGuardrail("t", "x", "substring", "strip", "", "", 0, nil); err == nil {
		t.Error("expected an error for an unknown mode")
	}
	if _, err := NewPromptRewriteGuardrail("t", "x", "prefix", "delete", "", "", 0, nil); err == nil {
		t.Error("expected an error for an unknown action")
	}
	if _, err := NewPromptRewriteGuardrail("t", "x", "prefix", "strip", "", "", 0, []string{"headers"}); err == nil {
		t.Error("expected an error for an unknown where target")
	}
}

// TestEmptyMatchIsRefused proves a pattern that would match everything is
// rejected at load rather than silently wiping every prompt.
func TestEmptyMatchIsRefused(t *testing.T) {
	if _, err := NewPromptRewriteGuardrail("t", "", "prefix", "strip", "", "", 0, nil); err == nil {
		t.Error("expected an error for an empty match")
	}
}

// TestInvalidRegexIsALoadError proves a bad pattern fails at load, not per request.
func TestInvalidRegexIsALoadError(t *testing.T) {
	if _, err := NewPromptRewriteGuardrail("t", "([unclosed", "regex", "strip", "", "", 0, nil); err == nil {
		t.Error("expected an error for an invalid regex")
	}
}

// TestReplaceWithoutReplacementIsRefused proves the config error is caught rather
// than silently stripping instead of replacing.
func TestReplaceWithoutReplacementIsRefused(t *testing.T) {
	if _, err := NewPromptRewriteGuardrail("t", "x", "prefix", "replace", "", "", 0, nil); err == nil {
		t.Error("expected an error for action replace with no replacement")
	}
}

// TestDefaultsAreApplied proves the documented defaults: mode prefix (an injected
// preamble is almost always a fixed opening), action strip, and system-only
// targeting.
func TestDefaultsAreApplied(t *testing.T) {
	g, err := NewPromptRewriteGuardrail("t", "x", "", "", "", "", 0, nil)
	if err != nil {
		t.Fatalf("NewPromptRewriteGuardrail: %v", err)
	}
	if g.mode != modePrefix {
		t.Errorf("mode = %q, want prefix", g.mode)
	}
	if g.action != actionStrip {
		t.Errorf("action = %q, want strip", g.action)
	}
	if !g.searches(targetSystem) || g.searches(targetMessages) {
		t.Errorf("where = %v, want system only", g.where)
	}
}

// TestShouldRunSkipsAnEmptyRequest proves the guardrail reports itself skipped for
// a request with nothing to search, so an "applied" log line stays meaningful.
func TestShouldRunSkipsAnEmptyRequest(t *testing.T) {
	g := newGuard(t, "x", "prefix", "strip", "")
	if g.ShouldRun(&types.NormalizedRequest{}) {
		t.Error("ShouldRun = true for a request with no system prompt and no messages")
	}
	if !g.ShouldRun(reqWithSystem("something")) {
		t.Error("ShouldRun = false for a request with content")
	}
}
