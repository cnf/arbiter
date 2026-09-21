package translator

import (
	"encoding/json"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

// A cache-hitting Claude reply's usage counters survive to the normalized
// event, and back out onto the wire for an Anthropic-format client.
//
// Anthropic reports cache_read_input_tokens and cache_creation_input_tokens
// only on message_start's usage object — never on message_delta, whose usage
// carries just cumulative output_tokens (confirmed against the platform
// docs' streaming example and the prompt-caching page). Before this fix,
// AnthropicStreamEventToNormalized's message_start case read InputTokens and
// dropped the two cache fields on the floor: NormalizedStreamEvent had had
// CacheReadTokens/CacheWriteTokens fields since the #28 work, and
// pipeline.go's accumulator already copied them onto the stored Usage, but
// nothing upstream of that ever set them for the Anthropic streaming path.
// Every streamed Claude row recorded zero cache usage even when the upstream
// cache hit — indistinguishable, from the stored row alone, from the cache
// never engaging.
//
// The payload is written as the raw SSE data a real capture shows, parsed
// through the same json.Unmarshal + AnthropicStreamEventToNormalized path
// the upstream reader uses (see internal/upstream/stream.go's
// parseAnthropicSSEEvent) — a hand-built struct would only prove the Go
// field assignment works, not that the wire's JSON keys reach it.
func TestClaudeStreamingCacheUsageSurvivesToNormalized(t *testing.T) {
	// A near-total cache hit: input_tokens is deliberately small (the
	// uncached remainder after the last breakpoint), with the bulk of the
	// prompt reported as cache_read_input_tokens — the exact shape #39
	// flagged as ambiguous from the stored row alone.
	raw := `{"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"usage":{"input_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":48210,"output_tokens":1}}}`

	var evt AnthropicStreamEvent
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		t.Fatalf("unmarshal message_start: %v", err)
	}

	normalized := AnthropicStreamEventToNormalized(&evt)
	if normalized.InputTokens != 2 {
		t.Errorf("InputTokens = %d, want 2", normalized.InputTokens)
	}
	if normalized.CacheReadTokens != 48210 {
		t.Errorf("CacheReadTokens = %d, want 48210 — the fix this test pins", normalized.CacheReadTokens)
	}
	if normalized.CacheWriteTokens != 0 {
		t.Errorf("CacheWriteTokens = %d, want 0", normalized.CacheWriteTokens)
	}

	// A cache-write turn (first turn establishing a breakpoint) exercises
	// the other field, and that both fields are read independently rather
	// than one accidentally aliasing the other.
	rawWrite := `{"type":"message_start","message":{"id":"msg_02","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"usage":{"input_tokens":15,"cache_creation_input_tokens":48195,"cache_read_input_tokens":0,"output_tokens":1}}}`
	var evtWrite AnthropicStreamEvent
	if err := json.Unmarshal([]byte(rawWrite), &evtWrite); err != nil {
		t.Fatalf("unmarshal message_start (write): %v", err)
	}
	normalizedWrite := AnthropicStreamEventToNormalized(&evtWrite)
	if normalizedWrite.CacheWriteTokens != 48195 {
		t.Errorf("CacheWriteTokens = %d, want 48195", normalizedWrite.CacheWriteTokens)
	}
	if normalizedWrite.CacheReadTokens != 0 {
		t.Errorf("CacheReadTokens = %d, want 0 on a write-only turn", normalizedWrite.CacheReadTokens)
	}
}

// The same cache counters, relayed back out to an Anthropic-format client,
// land on message_start's usage object rather than being dropped a second
// time on the outbound leg (NormalizedToAnthropicStreamEvent had the same
// gap as the inbound side, for the pass-through relay case where both the
// upstream and the client speak Anthropic's wire format).
func TestClaudeStreamingCacheUsageSurvivesRelayToAnthropicClient(t *testing.T) {
	normalized := &types.NormalizedStreamEvent{
		Type:             "message_start",
		MessageID:        "msg_01",
		MessageModel:     "claude-sonnet-5",
		InputTokens:      2,
		CacheReadTokens:  48210,
		CacheWriteTokens: 0,
	}

	out := NormalizedToAnthropicStreamEvent(normalized)
	if out.Message == nil {
		t.Fatal("expected a message object on message_start")
	}
	if out.Message.Usage.CacheReadInputTokens != 48210 {
		t.Errorf("outbound CacheReadInputTokens = %d, want 48210", out.Message.Usage.CacheReadInputTokens)
	}
	if out.Message.Usage.InputTokens != 2 {
		t.Errorf("outbound InputTokens = %d, want 2", out.Message.Usage.InputTokens)
	}

	// Round-trip through the real marshaller: AnthropicUsage's cache fields
	// carry `omitempty`, so a zero write-count must be absent from the
	// wire (matching what Anthropic itself sends), while a non-zero
	// read-count must be present. Only marshaling the struct can tell the
	// two apart from a field that was never set.
	wire, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal outbound event: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal outbound wire: %v", err)
	}
	msg, _ := decoded["message"].(map[string]interface{})
	usage, _ := msg["usage"].(map[string]interface{})
	if usage == nil {
		t.Fatal("no usage object on the outbound wire")
	}
	if _, present := usage["cache_creation_input_tokens"]; present {
		t.Errorf("cache_creation_input_tokens present on the wire with value 0, want omitted")
	}
	if got, ok := usage["cache_read_input_tokens"].(float64); !ok || int(got) != 48210 {
		t.Errorf("wire cache_read_input_tokens = %v, want 48210", usage["cache_read_input_tokens"])
	}
}
