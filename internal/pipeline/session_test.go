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

func TestSessionKeyRejectsShortOpenerWithNoAssistantYet(t *testing.T) {
	// "hi" alone is below the distinctiveness threshold; two unrelated chats
	// opening this way must NOT be pinned together.
	if _, ok := SessionKey("", reqWith("", user("hi"))); ok {
		t.Fatal("expected no key for a bare short greeting")
	}
}

func TestSessionKeyIgnoresInjectedConstantSystemPrompt(t *testing.T) {
	// The system_prompt guardrail prepends a long constant to every request.
	// If that constant counted toward the length gate, every conversation
	// would pass it and short openers would collide. Confirm a short opener
	// stays unpinned even with a long system prompt present.
	longSys := strings.Repeat("You are a helpful assistant. ", 10)

	if _, ok := SessionKey("", reqWith(longSys, user("hi"))); ok {
		t.Fatal("injected system prompt must not satisfy the distinctiveness gate")
	}
	// ...but it IS mixed into the hash, so distinct client-supplied system
	// prompts still separate otherwise-identical conversations.
	a, _ := SessionKey("", reqWith("system A", user("a sufficiently long first message")))
	b, _ := SessionKey("", reqWith("system B", user("a sufficiently long first message")))
	if a == b {
		t.Fatal("different system prompts produced the same key")
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
