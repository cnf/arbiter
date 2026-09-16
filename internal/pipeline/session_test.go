package pipeline

import (
	"strings"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

func reqWith(system string, msgs ...types.Message) *types.NormalizedRequest {
	return &types.NormalizedRequest{SystemPrompt: system, Messages: msgs}
}

func user(text string) types.Message {
	return types.Message{Role: "user", Content: []types.ContentBlock{types.TextBlock(text)}}
}

func assistant(text string) types.Message {
	return types.Message{Role: "assistant", Content: []types.ContentBlock{types.TextBlock(text)}}
}

func toolResult(id string) types.Message {
	return types.Message{Role: "user", Content: []types.ContentBlock{{Type: "tool_result", ToolResultForID: id, ToolResult: "result"}}}
}

func TestSessionKeyHeaderWins(t *testing.T) {
	req := reqWith("", user("a reasonably long first user message"))

	key, ok := SessionKey("explicit-session", req)
	if !ok || key != "explicit-session" {
		t.Fatalf("SessionKey = (%q, %v), want (explicit-session, true)", key, ok)
	}
}

func TestSessionKeyStableAsConversationGrows(t *testing.T) {
	// A conversation that has already had one exchange, then keeps growing.
	base := reqWith("sys", user("write me a parser for this config format"), assistant("sure, here is a parser"))
	key1, ok1 := SessionKey("", base)
	if !ok1 {
		t.Fatal("expected a key for a distinctive conversation")
	}

	grown := reqWith("sys",
		user("write me a parser for this config format"),
		assistant("sure, here is a parser"),
		user("now make it handle nested blocks"),
		assistant("done"),
	)
	key2, _ := SessionKey("", grown)
	if key1 != key2 {
		t.Fatalf("key changed as conversation grew: %q != %q", key1, key2)
	}
}

func TestSessionKeyDiffersForDifferentFirstMessages(t *testing.T) {
	a, _ := SessionKey("", reqWith("", user("write me a parser for this config format")))
	b, _ := SessionKey("", reqWith("", user("explain how garbage collection works here")))
	if a == b {
		t.Fatal("two different first user messages produced the same key")
	}
}

func TestSessionKeyRejectsShortOpenerWithNoSystemPrompt(t *testing.T) {
	// "hi" with no system prompt has nothing at all to be distinctive about;
	// two unrelated chats opening this way must NOT be pinned together.
	if _, ok := SessionKey("", reqWith("", user("hi"))); ok {
		t.Fatal("expected no key for a bare short greeting")
	}
}

func TestSessionKeyShortOpenerWithRealSystemPromptPins(t *testing.T) {
	// A real client sends a long house prompt and a terse opening turn — an
	// agent CLI opening with "stream", "hi", or a single word is the normal
	// shape, not an edge case. Gating on the user turn alone left such a
	// conversation with no key on turn 1 AND no key on turn 40 (the first user
	// turn never changes), so it was never pinned for its entire life.
	housePrompt := "You are opencode, an interactive CLI tool. " + strings.Repeat("More instructions. ", 40)

	key, ok := SessionKey("", reqWith(housePrompt, user("hi")))
	if !ok {
		t.Fatal("a terse opener backed by a real system prompt must still pin")
	}
	if key == "" {
		t.Fatal("empty key with ok=true")
	}

	// The system prompt must count toward distinctiveness, not just length:
	// two clients whose house prompts differ must not share a key even when
	// their opening turns are identical.
	other, _ := SessionKey("", reqWith("You are a different CLI tool.", user("hi")))
	if key == other {
		t.Fatal("different system prompts produced the same key")
	}
}

func TestSessionKeyRejectsBareShortOpenerWithNothingElse(t *testing.T) {
	// The gate still refuses a genuinely uninformative request: a bare "hi"
	// with no system prompt at all has nothing to distinguish it from any
	// other bare "hi".
	if _, ok := SessionKey("", reqWith("", user("hi"))); ok {
		t.Fatal("expected no key for a bare short greeting with no system prompt")
	}
}

func TestSessionKeySkipsToolOnlyUserTurns(t *testing.T) {
	// An agentic client may send a turn whose only content is a tool_result.
	// That turn carries no text and must not become the "first user turn".
	req := reqWith("sys",
		toolResult("call_1"),
		user("please summarize the document I attached earlier"),
	)
	key, ok := SessionKey("", req)
	if !ok {
		t.Fatal("expected a key derived from the first text-bearing user turn")
	}

	// Same conversation, but the tool-only turn is absent — the first
	// text-bearing turn is identical, so the key must match.
	key2, _ := SessionKey("", reqWith("sys", user("please summarize the document I attached earlier")))
	if key != key2 {
		t.Fatalf("tool-only leading turn changed the key: %q != %q", key, key2)
	}
}

func TestSessionKeyNoTextAtAll(t *testing.T) {
	if _, ok := SessionKey("", reqWith("", toolResult("call_1"))); ok {
		t.Fatal("expected no key when the conversation has no text at all")
	}
}
