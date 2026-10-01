package translator

import (
	"encoding/json"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

// The OpenAI half of the reasoning-effort plumbing (#79). The Anthropic leg
// (`output_config.effort`) is covered by thinking_request_test.go; this file
// pins the OpenAI spelling, which is the one the real clients actually send.
//
// The gap this closes: only the Anthropic key was parsed, so on an
// OpenAI-format request (Hermes, opencode) the effort never entered the
// normalized request at all — the routing signal and the stored
// `client_effort` column were empty on every production row, even though the
// Anthropic path was green in tests.

// A real OpenAI-format client sends `reasoning_effort`. Asserting through the
// JSON decoder rather than a hand-built struct, so the json tag itself is
// pinned — a field with a typo'd tag parses to nothing and would pass a test
// that set the field directly.
func TestOpenAIClientReasoningEffortIsRead(t *testing.T) {
	raw := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`
	var req types.OpenAIRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}
	if req.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort did not survive parsing: %q", req.ReasoningEffort)
	}

	norm := openAIRequestToNormalized(&req)
	if norm.OutputEffort != "high" {
		t.Fatalf("normalized OutputEffort = %q, want high", norm.OutputEffort)
	}
}

// A client that asks for nothing must not acquire a dial — the empty state has
// to stay distinguishable from an explicit value all the way to the wire.
func TestOpenAIClientWithoutEffortStaysEmpty(t *testing.T) {
	raw := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	var req types.OpenAIRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	norm := openAIRequestToNormalized(&req)
	if norm.OutputEffort != "" {
		t.Fatalf("OutputEffort = %q, want empty when the client sent none", norm.OutputEffort)
	}
	out := normalizedToOpenAIRequest(norm)
	b, _ := json.Marshal(out)
	var wire map[string]interface{}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal outbound: %v", err)
	}
	if _, ok := wire["reasoning_effort"]; ok {
		t.Errorf("outbound body invented a reasoning_effort key: %s", b)
	}
}

// The outbound leg for an OpenAI-speaking upstream must carry the dial under
// this format's own key. The body is rebuilt from the normalized request, so a
// value the normalized type holds but this builder does not write is lost on
// the wire no matter how faithfully it was parsed.
func TestOpenAIDestinationGetsReasoningEffort(t *testing.T) {
	norm := &types.NormalizedRequest{Model: "gpt-4o", OutputEffort: "medium"}
	out := normalizedToOpenAIRequest(norm)
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal outbound: %v", err)
	}
	var wire map[string]interface{}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal outbound: %v", err)
	}
	if got := wire["reasoning_effort"]; got != "medium" {
		t.Errorf("outbound reasoning_effort = %v, want medium (body: %s)", got, b)
	}
	// The Anthropic key has no meaning on this wire shape and must not appear.
	if _, ok := wire["output_config"]; ok {
		t.Errorf("outbound OpenAI body carried an output_config key: %s", b)
	}
}

// A cross-format hop: an Anthropic-format client (whose effort lives in
// `output_config.effort`) routed to an OpenAI-speaking upstream. The wire key
// is chosen by the DESTINATION, not by where the value came from, so the knob
// must be re-spelled rather than dropped.
func TestAnthropicClientEffortReachesOpenAIUpstream(t *testing.T) {
	raw := `{"model":"m","max_tokens":100,"output_config":{"effort":"high"},
	         "messages":[{"role":"user","content":"hi"}]}`
	var req types.AnthropicRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	norm := anthropicRequestToNormalized(&req)
	if norm.OutputEffort != "high" {
		t.Fatalf("normalized OutputEffort = %q, want high", norm.OutputEffort)
	}

	out := normalizedToOpenAIRequest(norm)
	b, _ := json.Marshal(out)
	var wire map[string]interface{}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal outbound: %v", err)
	}
	if got := wire["reasoning_effort"]; got != "high" {
		t.Errorf("outbound reasoning_effort = %v, want high (body: %s)", got, b)
	}
}
