package translator

import (
	"encoding/json"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

// #27: prompt_tokens_details.cached_tokens is parsed on every inbound path
// (applyOpenAIUsage, openAIResponseToNormalized) but was never written back
// out on any outbound OpenAI-format construction site, so a client could
// never compute a cache-hit percentage regardless of which upstream served
// the request. These pin the three sites: non-streaming, the Anthropic
// message_delta usage chunk, and the OpenAI-compatible terminal usage chunk.

// Non-streaming: a normalized response with CacheRead set must carry
// prompt_tokens_details.cached_tokens on the wire, and must NOT carry it —
// not even as an empty object — when there was nothing to report.
func TestNonStreamingResponseCarriesPromptTokensDetails(t *testing.T) {
	resp := &types.NormalizedResponse{
		Usage: types.Usage{InputTokens: 50113, OutputTokens: 437, CacheRead: 48210},
	}
	out := normalizedToOpenAIResponse(resp)

	wire, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	usage, _ := decoded["usage"].(map[string]interface{})
	if usage == nil {
		t.Fatal("no usage object on the wire")
	}
	details, _ := usage["prompt_tokens_details"].(map[string]interface{})
	if details == nil {
		t.Fatal("prompt_tokens_details missing from the wire despite a cache read")
	}
	if got, ok := details["cached_tokens"].(float64); !ok || int(got) != 48210 {
		t.Errorf("cached_tokens = %v, want 48210", details["cached_tokens"])
	}

	// No cache read at all: the key must be absent, not {} or {cached_tokens:0}.
	noCache := &types.NormalizedResponse{Usage: types.Usage{InputTokens: 100, OutputTokens: 20}}
	outNoCache := normalizedToOpenAIResponse(noCache)
	wireNoCache, err := json.Marshal(outNoCache)
	if err != nil {
		t.Fatalf("marshal (no cache): %v", err)
	}
	var decodedNoCache map[string]interface{}
	if err := json.Unmarshal(wireNoCache, &decodedNoCache); err != nil {
		t.Fatalf("unmarshal wire (no cache): %v", err)
	}
	usageNoCache, _ := decodedNoCache["usage"].(map[string]interface{})
	if _, present := usageNoCache["prompt_tokens_details"]; present {
		t.Errorf("prompt_tokens_details present with no cache read, want omitted entirely")
	}
}

// Streaming, Anthropic-sourced: the message_delta case is where this stream
// type's Usage is actually attached on the OpenAI wire (see the pipeline
// backfill that makes evt.InputTokens/CacheReadTokens non-zero here in the
// first place). This test only owns the translator's own half: given a
// populated event, does the wire carry the field.
func TestAnthropicMessageDeltaCarriesPromptTokensDetailsOnOpenAIWire(t *testing.T) {
	evt := &types.NormalizedStreamEvent{
		Type: "message_delta", InputTokens: 2, OutputTokens: 264, CacheReadTokens: 143223,
	}
	out := NormalizedToOpenAIStreamEvent(evt, "trace-1", 1)
	if out.Usage == nil {
		t.Fatal("no usage on the message_delta wire chunk")
	}
	wire, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	usage, _ := decoded["usage"].(map[string]interface{})
	details, _ := usage["prompt_tokens_details"].(map[string]interface{})
	if details == nil {
		t.Fatal("prompt_tokens_details missing from message_delta's wire usage")
	}
	if got, ok := details["cached_tokens"].(float64); !ok || int(got) != 143223 {
		t.Errorf("cached_tokens = %v, want 143223", details["cached_tokens"])
	}
}

// Streaming, OpenAI-compatible-sourced (OpenRouter etc): the synthetic
// "usage" event type.
func TestOpenAICompatibleUsageEventCarriesPromptTokensDetailsOnWire(t *testing.T) {
	evt := &types.NormalizedStreamEvent{
		Type: "usage", InputTokens: 263885, OutputTokens: 512, CacheReadTokens: 262784, CostUSD: 0.01,
	}
	out := NormalizedToOpenAIStreamEvent(evt, "trace-1", 1)
	if out.Usage == nil {
		t.Fatal("no usage on the usage wire chunk")
	}
	wire, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	usage, _ := decoded["usage"].(map[string]interface{})
	details, _ := usage["prompt_tokens_details"].(map[string]interface{})
	if details == nil {
		t.Fatal("prompt_tokens_details missing from the usage event's wire usage")
	}
	if got, ok := details["cached_tokens"].(float64); !ok || int(got) != 262784 {
		t.Errorf("cached_tokens = %v, want 262784", details["cached_tokens"])
	}
}

// buildOpenAIPromptDetails itself: nil (not {}) when there is nothing to
// report, and a real map otherwise. Pinned directly since all three call
// sites depend on this exact contract.
func TestBuildOpenAIPromptDetailsOmitsWhenZero(t *testing.T) {
	if got := buildOpenAIPromptDetails(0); got != nil {
		t.Errorf("buildOpenAIPromptDetails(0) = %v, want nil", got)
	}
	if got := buildOpenAIPromptDetails(-5); got != nil {
		t.Errorf("buildOpenAIPromptDetails(-5) = %v, want nil", got)
	}
	got := buildOpenAIPromptDetails(100)
	if got == nil || got["cached_tokens"] != 100 {
		t.Errorf("buildOpenAIPromptDetails(100) = %v, want {cached_tokens: 100}", got)
	}
}
