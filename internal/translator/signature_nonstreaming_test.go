package translator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

// The NON-STREAMING half of thinking-signature relay (#61).
//
// The streaming half has always worked (see signature_relay_test.go). The
// non-streaming half could not work: there was no field to carry the signature
// into, so every multi-turn non-streaming Anthropic thinking conversation
// through Arbiter lost it on turn two and onward.
//
// Both directions are asserted with raw wire bodies parsed through the same
// JSON decoder the real paths use — the discipline the sibling tests in this
// package follow. A hand-built struct here would encode this test's assumption
// about the wire shape instead of the wire shape itself, which is exactly how
// a test can pass with the fix reverted.

// sigFixture is a real-shaped Anthropic thinking-block signature: base64, the
// length the API actually issues. Its value is opaque to Arbiter and must
// survive byte-for-byte.
const sigFixture = "EqQBCgIYAhIM1gbcDa9GJwZA2b3hGgxBdjrkzLoky3dl1pkiYhb3NoYW5kcmE="

// Turn two of a multi-turn non-streaming conversation: the client replays the
// assistant's thinking block, signature and all, as Anthropic requires.
//
// The signature is not content. It is a handle Anthropic issued with the
// thinking block, and the upstream rejects or silently degrades the
// conversation if it comes back missing or altered. Arbiter rebuilds the
// outbound request from the normalized struct, so a signature the normalized
// type cannot hold is dropped on the way out even though the client sent it.
func TestNonStreamingThinkingSignatureSurvivesRequestToUpstream(t *testing.T) {
	raw := `{
		"model": "claude-sonnet-5",
		"max_tokens": 32000,
		"thinking": {"type": "adaptive"},
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "what is a proxy"}]},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "The user wants a definition.", "signature": "` + sigFixture + `"},
				{"type": "text", "text": "A proxy sits between client and server."}
			]},
			{"role": "user", "content": [{"type": "text", "text": "and a reverse proxy?"}]}
		]
	}`

	var req types.AnthropicRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}

	// Client -> normalized. The thinking block must arrive with its signature.
	norm := anthropicRequestToNormalized(&req)
	thinking := findBlock(norm, "thinking")
	if thinking == nil {
		t.Fatal("normalized dropped the thinking block entirely")
	}
	if thinking.Text != "The user wants a definition." {
		t.Errorf("thinking text = %q, want the client's text", thinking.Text)
	}
	if thinking.Signature != sigFixture {
		t.Fatalf("normalized lost the signature (got %q) — the conversation cannot be continued",
			thinking.Signature)
	}

	// Normalized -> upstream body. This is the assertion that matters: the
	// outbound request is rebuilt from scratch, so a field the normalized type
	// carries but this builder does not write never reaches the upstream no
	// matter how faithfully it was parsed.
	out := normalizedToAnthropicRequest(norm)
	outThinking := findAnthropicContent(out, "thinking")
	if outThinking == nil {
		t.Fatal("outbound request has no thinking block")
	}
	if outThinking.Signature != sigFixture {
		t.Errorf("outbound thinking signature = %q, want %q", outThinking.Signature, sigFixture)
	}
	if outThinking.Thinking != "The user wants a definition." {
		t.Errorf("outbound thinking text = %q, want the client's text", outThinking.Thinking)
	}

	// And on the actual wire, since an omitempty misapplied is invisible in
	// the struct, and the key name is what the upstream actually reads.
	wire := mustJSON(t, out)
	if !strings.Contains(wire, `"signature":"`+sigFixture+`"`) {
		t.Errorf("signature absent from the marshalled upstream request body:\n%s", wire)
	}
	if !strings.Contains(wire, `"type":"thinking","thinking":"The user wants a definition."`) {
		t.Errorf("thinking text is not under Anthropic's \"thinking\" key on the outbound wire:\n%s", wire)
	}
	// A text block must not acquire a stray signature field.
	if strings.Contains(wire, `"text":"A proxy sits between client and server.","signature"`) {
		t.Error("a text block was emitted with a signature field it cannot have")
	}
}

// The response direction of the same rule: the upstream issues a thinking block
// with a signature, and it must still be there when the reply leaves Arbiter.
//
// If the inbound parse drops it, no client can replay the block on its next
// turn — the failure is silent and surfaces one turn later, in a different
// process, as degraded reasoning rather than as an error.
func TestNonStreamingThinkingSignatureSurvivesUpstreamResponseToClient(t *testing.T) {
	raw := `{
		"id": "msg_01XFDUDYJgAACzvnptvVoYEL",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-5",
		"content": [
			{"type": "thinking", "thinking": "Consider the two hops.", "signature": "` + sigFixture + `"},
			{"type": "text", "text": "A reverse proxy terminates the client."}
		],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 12, "output_tokens": 34}
	}`

	var resp types.AnthropicResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal captured response: %v", err)
	}

	// Upstream -> normalized.
	norm := anthropicResponseToNormalized(&resp)
	thinking := findBlockResp(norm, "thinking")
	if thinking == nil {
		t.Fatal("normalized dropped the thinking block from the response")
	}
	if thinking.Signature != sigFixture {
		t.Fatalf("inbound response signature = %q, want %q", thinking.Signature, sigFixture)
	}

	// Normalized -> client body.
	back := normalizedToAnthropicResponse(norm)
	wire := mustJSON(t, back)
	if !strings.Contains(wire, `"signature":"`+sigFixture+`"`) {
		t.Errorf("signature absent from the reply sent to the client:\n%s", wire)
	}
	if !strings.Contains(wire, `"type":"thinking","thinking":"Consider the two hops."`) {
		t.Errorf("thinking text is not under Anthropic's \"thinking\" key in the reply:\n%s", wire)
	}
}

// OpenAI has no representation for an Anthropic thinking signature, and the
// streaming path already drops it there deliberately. The non-streaming path
// must do the same rather than inventing a field no OpenAI client can read —
// and must not drop the thinking TEXT along with the signature, which is the
// other way this can go wrong.
func TestNonStreamingSignatureIsDroppedOnOpenAIWireOnly(t *testing.T) {
	norm := &types.NormalizedResponse{
		TraceID: "t-1",
		Content: []types.ContentBlock{
			{Type: "thinking", Text: "Consider the two hops.", Signature: sigFixture},
			{Type: "text", Text: "A reverse proxy terminates the client."},
		},
	}

	wire := mustJSON(t, normalizedToOpenAIResponse(norm))
	if strings.Contains(wire, "signature") {
		t.Errorf("signature leaked onto the OpenAI wire, where no client can use it:\n%s", wire)
	}
	if !strings.Contains(wire, "Consider the two hops.") {
		t.Errorf("thinking text was dropped along with the signature:\n%s", wire)
	}
}

func findBlock(norm *types.NormalizedRequest, blockType string) *types.ContentBlock {
	for i := range norm.Messages {
		for j := range norm.Messages[i].Content {
			if norm.Messages[i].Content[j].Type == blockType {
				return &norm.Messages[i].Content[j]
			}
		}
	}
	return nil
}

func findBlockResp(norm *types.NormalizedResponse, blockType string) *types.ContentBlock {
	for i := range norm.Content {
		if norm.Content[i].Type == blockType {
			return &norm.Content[i]
		}
	}
	return nil
}

func findAnthropicContent(req *types.AnthropicRequest, blockType string) *types.AnthropicContent {
	for i := range req.Messages {
		for j := range req.Messages[i].Content {
			if req.Messages[i].Content[j].Type == blockType {
				return &req.Messages[i].Content[j]
			}
		}
	}
	return nil
}
