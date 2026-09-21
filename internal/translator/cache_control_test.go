package translator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

// Anthropic prompt caching is opt-in per content block: a block with no
// `cache_control` marker is not part of any cacheable prefix, so the upstream
// re-reads it at full input price on every turn. Arbiter sets no marker at all
// before this work, which is why 483 of 483 Claude-format requests in the
// event store sat at 0% cache while OpenAI-format traffic cached normally
// (OpenAI caches a prompt prefix server-side without being asked).
//
// These tests assert the OUTBOUND WIRE SHAPE, from the real entry point, for
// one reason: a round trip through the local structs only proves Arbiter agrees
// with itself. `AnthropicSystem` carries a custom marshaller, so the whole
// marker policy is invisible to a test that reads the struct back — the field
// can be populated and absent from the emitted bytes at the same time.

// cachedSystemPrompt drives a client payload that is shaped like a real
// Anthropic request (block-form system, tools, a two-turn conversation) through
// the production parse and convert path, and returns the marshalled body the
// upstream would receive together with its decoded form.
func cachedSystemPrompt(t *testing.T) ([]byte, map[string]interface{}) {
	t.Helper()

	raw := `{
      "model": "claude/claude-sonnet-5",
      "max_tokens": 8192,
      "system": [
        {"type": "text", "text": "You are a helpful agent."},
        {"type": "text", "text": "Answer concisely."}
      ],
      "tools": [
        {"name": "read_file", "description": "read", "input_schema": {"type": "object"}},
        {"name": "write_file", "description": "write", "input_schema": {"type": "object"}}
      ],
      "messages": [
        {"role": "user", "content": "hello"},
        {"role": "assistant", "content": [{"type": "text", "text": "hi"}]},
        {"role": "user", "content": [{"type": "text", "text": "do the thing"}]}
      ]
    }`

	tr := NewDefaultTranslator()
	norm, err := tr.ToNormalized([]byte(raw), "anthropic")
	if err != nil {
		t.Fatalf("ToNormalized: %v", err)
	}
	out, err := tr.NormalizedToAnthropicRequest(norm)
	if err != nil {
		t.Fatalf("NormalizedToAnthropicRequest: %v", err)
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal outbound body: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("outbound body is not JSON: %v\n%s", err, body)
	}
	return body, decoded
}

// blocksWithMarker walks a decoded block array and counts how many entries
// carry a well-formed ephemeral `cache_control`.
func blocksWithMarker(t *testing.T, node interface{}, what string) int {
	t.Helper()

	blocks, ok := node.([]interface{})
	if !ok {
		t.Fatalf("%s: expected an array of blocks, got %T", what, node)
	}
	marked := 0
	for i, b := range blocks {
		block, ok := b.(map[string]interface{})
		if !ok {
			t.Fatalf("%s[%d]: block is %T, not an object", what, i, b)
		}
		cc, present := block["cache_control"]
		if !present {
			continue
		}
		m, ok := cc.(map[string]interface{})
		if !ok {
			t.Errorf("%s[%d]: cache_control is %T, want an object", what, i, cc)
			continue
		}
		if m["type"] != types.AnthropicCacheControlEphemeral {
			t.Errorf("%s[%d]: cache_control.type = %v, want %q", what, i, m["type"], types.AnthropicCacheControlEphemeral)
			continue
		}
		marked++
	}
	return marked
}

// The two breakpoints Arbiter emits on an ordinary request, asserted on the
// bytes: one on the system prompt (stable for the whole conversation) and one
// on the last content block of the newest turn (the rolling breakpoint that
// makes each completed turn cacheable input for the next request).
//
// Without the system marker the stable prefix is never cached at all; without
// the rolling marker a long agent conversation re-reads every turn it has
// already paid for.
func TestAnthropicOutboundMarksStablePrefixAndLastTurn(t *testing.T) {
	body, decoded := cachedSystemPrompt(t)

	// The system prompt is the one thing that cannot be carried as a bare
	// string any more: a string has nowhere to hang the marker. Assert the
	// shape changed deliberately, not by accident.
	if _, isString := decoded["system"].(string); isString {
		t.Fatalf("system serialized as a bare string, which cannot carry cache_control: %s", body)
	}
	if got := blocksWithMarker(t, decoded["system"], "system"); got != 1 {
		t.Errorf("system carries %d cache markers, want 1: %s", got, body)
	}

	messages, ok := decoded["messages"].([]interface{})
	if !ok || len(messages) == 0 {
		t.Fatalf("messages missing from outbound body: %s", body)
	}
	last, ok := messages[len(messages)-1].(map[string]interface{})
	if !ok {
		t.Fatalf("last message is not an object: %s", body)
	}
	if got := blocksWithMarker(t, last["content"], "last message"); got != 1 {
		t.Errorf("last message carries %d cache markers, want 1: %s", got, body)
	}

	// The whole point of the fix: the marker is on the wire, not just in the
	// struct. A regression that drops the field's tag or its marshalling would
	// still satisfy the counts above if the key vanished — so assert the key.
	if !strings.Contains(string(body), `"cache_control":{"type":"ephemeral"}`) {
		t.Errorf("no serialized cache_control marker in the outbound body: %s", body)
	}
}

// Anthropic rejects a request carrying more than four `cache_control` markers,
// so a policy that marks "everything that looks stable" is a 400 rather than a
// saving. Count them on the wire, not in the struct.
func TestAnthropicOutboundStaysWithinBreakpointLimit(t *testing.T) {
	body, decoded := cachedSystemPrompt(t)

	total := blocksWithMarker(t, decoded["system"], "system")
	messages, _ := decoded["messages"].([]interface{})
	for i, m := range messages {
		msg, _ := m.(map[string]interface{})
		total += blocksWithMarker(t, msg["content"], "message")
		_ = i
	}
	if tools, ok := decoded["tools"].([]interface{}); ok {
		total += blocksWithMarker(t, tools, "tools")
	}

	if total > anthropicCacheBreakpointLimit {
		t.Errorf("%d cache breakpoints on the outbound body, Anthropic allows at most %d: %s",
			total, anthropicCacheBreakpointLimit, body)
	}
	// A request that carries none is the defect this work fixes, so the limit
	// test would also pass on a body with zero markers. Pin the floor too.
	if total == 0 {
		t.Errorf("no cache breakpoints at all on the outbound body: %s", body)
	}
}

// Arbiter owns the outbound breakpoint policy, and a client's own inbound
// markers are deliberately not carried across.
//
// The reasoning: a breakpoint caches the prefix ENDING at it, and Arbiter's
// rolling marker sits on the last block of the newest turn — so the prefix it
// defines is a superset of anything the client marked earlier in the same
// conversation. Relaying the client's markers as well would buy no extra cache
// read while spending breakpoints against Anthropic's limit of four, and the
// failure that matters is a rejected request: a client marking three of its own
// plus the two Arbiter adds is a 400, not a saving. The drop is safe rather
// than silent because everything the client marked is inside the prefix Arbiter
// still marks.
//
// Written from a RAW payload through the production parse path, so it also pins
// the inbound side: the blocks carrying the client's markers must survive
// parsing as ordinary text blocks, dropping only the attribute.
func TestClientCacheControlIsReplacedNotRelayed(t *testing.T) {
	// Four of the client's own breakpoints — one more than Anthropic allows on
	// the whole request, including Arbiter's.
	raw := `{
      "model": "claude-sonnet-5",
      "max_tokens": 1024,
      "system": [{"type": "text", "text": "You are Claude Code.", "cache_control": {"type": "ephemeral"}}],
      "messages": [
        {"role": "user", "content": [
          {"type": "text", "text": "prefix", "cache_control": {"type": "ephemeral"}}
        ]},
        {"role": "assistant", "content": [
          {"type": "text", "text": "ack", "cache_control": {"type": "ephemeral"}}
        ]},
        {"role": "user", "content": [
          {"type": "text", "text": "latest", "cache_control": {"type": "ephemeral"}}
        ]}
      ]
    }`

	tr := NewDefaultTranslator()
	norm, err := tr.ToNormalized([]byte(raw), "anthropic")
	if err != nil {
		t.Fatalf("ToNormalized: %v", err)
	}
	// The client's marked blocks must still carry their text through.
	if norm.SystemPrompt != "You are Claude Code." {
		t.Fatalf("system prompt lost: %q", norm.SystemPrompt)
	}

	out, err := tr.NormalizedToAnthropicRequest(norm)
	if err != nil {
		t.Fatalf("NormalizedToAnthropicRequest: %v", err)
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Exactly Arbiter's two, not the client's four plus two.
	if got := strings.Count(string(body), `"cache_control"`); got != 2 {
		t.Errorf("outbound body carries %d cache markers, want Arbiter's 2: %s", got, body)
	}
}

// The marker must be set on the LAST block of the last message, not the first:
// a breakpoint caches the prefix ending at it, so marking the opening block of
// the conversation caches nothing that the next turn does not already carry.
func TestAnthropicLastMessageMarkerIsOnItsLastBlock(t *testing.T) {
	norm := &types.NormalizedRequest{
		Model: "claude/claude-sonnet-5",
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{types.TextBlock("first")}},
			{Role: "assistant", Content: []types.ContentBlock{types.TextBlock("reply")}},
			{Role: "user", Content: []types.ContentBlock{
				types.TextBlock("second"),
				types.TextBlock("third"),
			}},
		},
	}

	out := normalizedToAnthropicRequest(norm)
	last := out.Messages[len(out.Messages)-1]
	for i, b := range last.Content {
		marked := b.CacheControl != nil
		want := i == len(last.Content)-1
		if marked != want {
			t.Errorf("last message block %d: marked=%v, want %v", i, marked, want)
		}
	}
	// Earlier turns stay unmarked: they are inside the cached prefix the
	// rolling breakpoint defines, and a marker on each would add nothing but
	// breakpoints against the limit.
	for _, m := range out.Messages[:len(out.Messages)-1] {
		for _, b := range m.Content {
			if b.CacheControl != nil {
				t.Errorf("an earlier turn carries a cache marker, want none: %+v", b)
			}
		}
	}
}

// A message with no content blocks cannot carry a marker — there is no block to
// attach it to — so the scan has to reach past it to the last message that has
// one. Treating a blockless tail as the end of the conversation would silently
// emit no rolling breakpoint at all, which is indistinguishable from the bug
// this work exists to fix.
func TestAnthropicMarkerSkipsBlocklessTailMessage(t *testing.T) {
	norm := &types.NormalizedRequest{
		Model: "claude/claude-sonnet-5",
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{types.TextBlock("real content")}},
			{Role: "assistant", Content: nil},
		},
	}

	out := normalizedToAnthropicRequest(norm)
	if len(out.Messages) != 2 {
		t.Fatalf("expected both messages on the outbound body, got %d", len(out.Messages))
	}
	carrier := out.Messages[0].Content
	if len(carrier) == 0 || carrier[len(carrier)-1].CacheControl == nil {
		t.Fatalf("no rolling breakpoint: the blockless tail consumed it — %+v", out.Messages)
	}
	if len(out.Messages[1].Content) != 0 {
		t.Errorf("the blockless message gained content: %+v", out.Messages[1])
	}
}

// Tool definitions are frozen per conversation, so the last one carries the
// breakpoint that starts the cacheable prefix. Without it, a request whose
// system prompt is absent (every OpenAI client that sends none) has no
// breakpoint before the conversation at all.
func TestAnthropicMarksLastToolOnly(t *testing.T) {
	norm := &types.NormalizedRequest{
		Model: "claude/claude-sonnet-5",
		Tools: []types.Tool{
			{Name: "read_file", Description: "read", InputSchema: map[string]interface{}{"type": "object"}},
			{Name: "write_file", Description: "write", InputSchema: map[string]interface{}{"type": "object"}},
		},
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{types.TextBlock("hi")}},
		},
	}

	out := normalizedToAnthropicRequest(norm)
	for i, tool := range out.Tools {
		marked := tool.CacheControl != nil
		want := i == len(out.Tools)-1
		if marked != want {
			t.Errorf("tool %d (%s): marked=%v, want %v", i, tool.Name, marked, want)
		}
	}
}

// A request with nothing to mark must not fabricate a marker, and — the other
// half of the same rule — must not emit an empty system object or a spurious
// empty block. The empty-content array and the omitted absent prompt are both
// shapes Anthropic has an opinion about.
func TestAnthropicEmptyRequestEmitsNoMarkers(t *testing.T) {
	norm := &types.NormalizedRequest{Model: "claude/claude-sonnet-5"}

	out := normalizedToAnthropicRequest(norm)
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "cache_control") {
		t.Errorf("a request with nothing to cache emitted a marker: %s", body)
	}
}

// An empty system prompt must not grow a marker either: the marker would cache
// an empty prefix, which is a breakpoint spent on nothing.
func TestAnthropicEmptySystemEmitsEmptyString(t *testing.T) {
	body, err := json.Marshal(types.AnthropicRequest{
		Model:     "claude-sonnet-5",
		MaxTokens: 16,
		Messages:  []types.AnthropicMessage{{Role: "user", Content: []types.AnthropicContent{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), `"system"`) {
		t.Errorf("an absent system prompt was emitted anyway: %s", body)
	}

	body, err = json.Marshal(types.AnthropicRequest{
		Model:     "claude-sonnet-5",
		MaxTokens: 16,
		System:    "",
		Messages:  []types.AnthropicMessage{{Role: "user", Content: []types.AnthropicContent{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "cache_control") {
		t.Errorf("an empty system prompt was marked as cacheable: %s", body)
	}
}
