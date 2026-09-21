package translator

import (
	"encoding/json"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

// The request half of the Claude-via-Arbiter fix. A real Anthropic client asks
// for extended reasoning and the upstream never hears about it, because
// AnthropicRequest did not declare the fields and normalizedToAnthropicRequest
// had nothing to copy. The body below is the shape a captured LiteLLM request
// from claude-cli/2.1.260 (claude-desktop-3p) actually carries:
//
//	"thinking": {"type": "adaptive"},
//	"output_config": {"effort": "high"}
//
// parsed through the same JSON decoder the inbound path uses, so this test
// pins the wire shape rather than a hand-built struct.
func TestClientThinkingRequestSurvivesToAnthropicUpstream(t *testing.T) {
	raw := `{
		"model": "claude-sonnet",
		"max_tokens": 32000,
		"stream": true,
		"system": "You are Claude Code.",
		"thinking": {"type": "adaptive"},
		"output_config": {"effort": "high"},
		"messages": [{"role": "user", "content": "hi"}]
	}`

	var req types.AnthropicRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}
	if req.Thinking == nil || req.Thinking.Type != "adaptive" {
		t.Fatalf("thinking did not survive parsing: %+v", req.Thinking)
	}
	if req.OutputConfig == nil || req.OutputConfig.Effort != "high" {
		t.Fatalf("output_config did not survive parsing: %+v", req.OutputConfig)
	}

	// Client -> normalized.
	norm := anthropicRequestToNormalized(&req)
	if norm.Thinking == nil || norm.Thinking.Type != "adaptive" {
		t.Fatalf("normalized dropped thinking: %+v", norm.Thinking)
	}
	if norm.OutputEffort != "high" {
		t.Fatalf("normalized OutputEffort = %q, want high", norm.OutputEffort)
	}

	// Normalized -> upstream body. This is the assertion that matters: the
	// outbound request is rebuilt from scratch, so a field the normalized type
	// carries but this builder does not write is lost on the wire no matter
	// how faithfully it was parsed.
	out := normalizedToAnthropicRequest(norm)
	if out.Thinking == nil || out.Thinking.Type != "adaptive" {
		t.Fatalf("outbound thinking = %+v, want the client's adaptive block", out.Thinking)
	}
	if out.OutputConfig == nil || out.OutputConfig.Effort != "high" {
		t.Fatalf("outbound output_config = %+v, want effort high", out.OutputConfig)
	}

	// And it must actually serialize: a field present on the struct but
	// omitted by a json tag is not on the wire.
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal outbound: %v", err)
	}
	var wire map[string]interface{}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal outbound: %v", err)
	}
	if _, ok := wire["thinking"]; !ok {
		t.Errorf("outbound body has no thinking key: %s", b)
	}
	if _, ok := wire["output_config"]; !ok {
		t.Errorf("outbound body has no output_config key: %s", b)
	}
}

// An explicit budget form must survive too — "enabled" with budget_tokens is
// the other documented shape, and a struct that only knew `type` would drop
// the budget.
func TestExplicitThinkingBudgetSurvives(t *testing.T) {
	raw := `{"model":"m","max_tokens":100,"thinking":{"type":"enabled","budget_tokens":8000},
	         "messages":[{"role":"user","content":"hi"}]}`
	var req types.AnthropicRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out := normalizedToAnthropicRequest(anthropicRequestToNormalized(&req))
	if out.Thinking == nil || out.Thinking.BudgetTokens != 8000 {
		t.Fatalf("budget_tokens = %+v, want 8000 preserved", out.Thinking)
	}
}

// A client that sends NO thinking request must not acquire one. The failure
// here is the opposite of the one above: inventing a thinking request for
// ordinary traffic would slow every reply down and change what the model does,
// and an absent field is not the same request as a null one.
func TestNoThinkingRequestStaysAbsent(t *testing.T) {
	raw := `{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	var req types.AnthropicRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out := normalizedToAnthropicRequest(anthropicRequestToNormalized(&req))
	if out.Thinking != nil {
		t.Errorf("thinking = %+v, want nil for a client that sent none", out.Thinking)
	}
	if out.OutputConfig != nil {
		t.Errorf("output_config = %+v, want nil (not an empty block)", out.OutputConfig)
	}
	b, _ := json.Marshal(out)
	var wire map[string]interface{}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal outbound: %v", err)
	}
	if _, ok := wire["thinking"]; ok {
		t.Errorf("outbound body invented a thinking key: %s", b)
	}
	if _, ok := wire["output_config"]; ok {
		t.Errorf("outbound body invented an output_config key: %s", b)
	}
}

// An OpenAI-format client has no thinking field to send, so the normalized
// request must come out with none either — the field is Anthropic-only and
// must not be fabricated for the other ingress format.
func TestOpenAIIngressGetsNoThinkingRequest(t *testing.T) {
	raw := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	var req types.OpenAIRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	norm := openAIRequestToNormalized(&req)
	if norm.Thinking != nil || norm.OutputEffort != "" {
		t.Fatalf("OpenAI ingress obtained thinking: %+v effort=%q", norm.Thinking, norm.OutputEffort)
	}
}
