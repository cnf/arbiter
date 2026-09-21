package types

import (
	"encoding/json"
	"strings"
	"testing"
)

// AnthropicSystem carries a custom marshaller, so nothing about its wire shape
// can be asserted from the struct: the field can be right and the bytes wrong.
// These tests read the marshalled bytes.
//
// The shape is load-bearing for prompt caching. A bare string has nowhere to
// hang `cache_control`, so it can never be part of a cacheable prefix — and the
// system prompt is the one part of a conversation that is stable for its whole
// length, which makes it the most valuable thing to cache and the most
// expensive to re-read on every turn.

func TestAnthropicSystemMarshalsWithCacheMarker(t *testing.T) {
	req := AnthropicRequest{
		Model:     "claude-sonnet-5",
		MaxTokens: 1024,
		System:    "be concise",
		Messages: []AnthropicMessage{
			{Role: "user", Content: []AnthropicContent{{Type: "text", Text: "hello"}}},
		},
	}

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	want := `"system":[{"type":"text","text":"be concise","cache_control":{"type":"ephemeral"}}]`
	if !strings.Contains(string(body), want) {
		t.Errorf("system did not serialize as a marked text block:\n got %s\nwant it to contain %s", body, want)
	}
}

// An empty system prompt is omitted entirely (omitempty), and must not turn
// into a marked empty block — a breakpoint spent caching nothing.
func TestAnthropicEmptySystemIsOmittedUnmarked(t *testing.T) {
	body, err := json.Marshal(AnthropicRequest{
		Model:     "claude-sonnet-5",
		MaxTokens: 1024,
		Messages: []AnthropicMessage{
			{Role: "user", Content: []AnthropicContent{{Type: "text", Text: "hello"}}},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), `"system"`) {
		t.Errorf("an empty system prompt was serialized: %s", body)
	}
}

// The content block's marker is a pointer so absence is distinguishable from
// the zero value: an unmarked block must omit the key rather than emit
// `"cache_control":null`, which is a different body and not a marker Anthropic
// recognises.
func TestUnmarkedContentBlockOmitsCacheControl(t *testing.T) {
	body, err := json.Marshal(AnthropicContent{Type: "text", Text: "plain"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "cache_control") {
		t.Errorf("an unmarked block emitted a cache_control key: %s", body)
	}
	if strings.Contains(string(body), "null") {
		t.Errorf("an unmarked block emitted a null-valued field: %s", body)
	}
}

// Fresh markers, never a shared pointer: the marker is marshalled into outbound
// bodies, so one mutable instance handed to every block would let a later edit
// rewrite every request's marker at once.
func TestNewAnthropicCacheControlReturnsDistinctValues(t *testing.T) {
	a := NewAnthropicCacheControl()
	b := NewAnthropicCacheControl()
	if a == b {
		t.Fatal("NewAnthropicCacheControl returned the same pointer twice")
	}
	if a.Type != AnthropicCacheControlEphemeral {
		t.Errorf("type = %q, want %q", a.Type, AnthropicCacheControlEphemeral)
	}
	a.Type = "mutated"
	if b.Type != AnthropicCacheControlEphemeral {
		t.Errorf("mutating one marker changed another: %q", b.Type)
	}
}
