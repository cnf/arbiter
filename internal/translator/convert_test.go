package translator

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

func TestAnthropicRequestRoundTrip(t *testing.T) {
	tr := NewDefaultTranslator()

	orig := &types.AnthropicRequest{
		Model:     "claude-3-opus-20250219",
		MaxTokens: 1024,
		System:    "be concise",
		Messages: []types.AnthropicMessage{
			{Role: "user", Content: []types.AnthropicContent{{Type: "text", Text: "hello"}}},
		},
	}

	norm := anthropicRequestToNormalized(orig)
	if norm.SystemPrompt != "be concise" {
		t.Fatalf("system prompt lost: %q", norm.SystemPrompt)
	}

	back, err := tr.NormalizedToAnthropicRequest(norm)
	if err != nil {
		t.Fatalf("NormalizedToAnthropicRequest: %v", err)
	}
	if back.Model != orig.Model || back.MaxTokens != orig.MaxTokens || back.System != orig.System {
		t.Fatalf("round trip mismatch: got %+v want %+v", back, orig)
	}
	if len(back.Messages) != 1 || back.Messages[0].Content[0].Text != "hello" {
		t.Fatalf("message content lost: %+v", back.Messages)
	}
}

// Real Anthropic clients send `system` as an array of content blocks, which a
// plain `string` field rejected at parse time — a 400 before any routing, so
// the client could not reach Arbiter at all. These two tests pin the shapes as
// raw wire JSON rather than as constructed structs: building an
// AnthropicRequest in Go and round-tripping it only ever proves Arbiter agrees
// with itself, which is exactly how the mismatch survived the suite.
func TestAnthropicSystemAcceptsBlockArray(t *testing.T) {
	const payload = `{
		"model": "claude-3-opus-20250219",
		"max_tokens": 100,
		"system": [{"type": "text", "text": "You are Claude."}],
		"messages": [{"role": "user", "content": "hi"}]
	}`

	norm, err := NewDefaultTranslator().ToNormalized([]byte(payload), "anthropic")
	if err != nil {
		t.Fatalf("ToNormalized rejected a block-array system prompt: %v", err)
	}
	if norm.SystemPrompt != "You are Claude." {
		t.Fatalf("SystemPrompt = %q, want %q", norm.SystemPrompt, "You are Claude.")
	}
}

func TestAnthropicSystemJoinsMultipleBlocks(t *testing.T) {
	const payload = `{
		"model": "m",
		"max_tokens": 1,
		"system": [
			{"type": "text", "text": "first"},
			{"type": "text", "text": "second"},
			{"type": "thinking", "thinking": "ignored"}
		],
		"messages": [{"role": "user", "content": "hi"}]
	}`

	norm, err := NewDefaultTranslator().ToNormalized([]byte(payload), "anthropic")
	if err != nil {
		t.Fatalf("ToNormalized: %v", err)
	}
	if norm.SystemPrompt != "first\n\nsecond" {
		t.Fatalf("SystemPrompt = %q, want %q", norm.SystemPrompt, "first\n\nsecond")
	}
}

// A bare-string message content is legal Anthropic and is what simple clients
// and plain curl send.
func TestAnthropicMessageAcceptsStringContent(t *testing.T) {
	const payload = `{
		"model": "m",
		"max_tokens": 1,
		"messages": [{"role": "user", "content": "hello there"}]
	}`

	norm, err := NewDefaultTranslator().ToNormalized([]byte(payload), "anthropic")
	if err != nil {
		t.Fatalf("ToNormalized rejected string message content: %v", err)
	}
	if len(norm.Messages) != 1 || len(norm.Messages[0].Content) != 1 {
		t.Fatalf("expected one message with one block, got %+v", norm.Messages)
	}
	if got := norm.Messages[0].Content[0].Text; got != "hello there" {
		t.Fatalf("text = %q, want %q", got, "hello there")
	}
}

// Both tolerant shapes at once, mirroring a real client request.
func TestAnthropicAcceptsBothShapesTogether(t *testing.T) {
	const payload = `{
		"model": "m",
		"max_tokens": 1,
		"system": [{"type": "text", "text": "sys"}],
		"messages": [
			{"role": "user", "content": "plain"},
			{"role": "assistant", "content": [{"type": "text", "text": "block form"}]}
		]
	}`

	norm, err := NewDefaultTranslator().ToNormalized([]byte(payload), "anthropic")
	if err != nil {
		t.Fatalf("ToNormalized: %v", err)
	}
	if norm.SystemPrompt != "sys" {
		t.Fatalf("SystemPrompt = %q", norm.SystemPrompt)
	}
	if len(norm.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(norm.Messages))
	}
	if norm.Messages[0].Content[0].Text != "plain" {
		t.Fatalf("string-content message lost: %+v", norm.Messages[0])
	}
	if norm.Messages[1].Content[0].Text != "block form" {
		t.Fatalf("block-content message lost: %+v", norm.Messages[1])
	}
}

func TestOpenAIRequestSystemMessageExtraction(t *testing.T) {
	req := &types.OpenAIRequest{
		Model: "gpt-4o",
		Messages: []types.OpenAIMessage{
			{Role: "system", Content: types.OpenAIMessageContent{types.TextBlock("be concise")}},
			{Role: "user", Content: types.OpenAIMessageContent{types.TextBlock("hello")}},
		},
	}
	norm := openAIRequestToNormalized(req)
	if norm.SystemPrompt != "be concise" {
		t.Fatalf("expected system prompt extracted, got %q", norm.SystemPrompt)
	}
	if len(norm.Messages) != 1 || norm.Messages[0].Role != "user" {
		t.Fatalf("expected only the user message to remain, got %+v", norm.Messages)
	}
}

func TestToolUseRoundTripAnthropicToOpenAI(t *testing.T) {
	norm := &types.NormalizedRequest{
		Model: "x",
		Messages: []types.Message{
			{Role: "assistant", Content: []types.ContentBlock{
				{Type: "tool_use", ToolUseID: "tu_1", ToolName: "get_weather", ToolInput: map[string]interface{}{"city": "NYC"}},
			}},
		},
	}
	openaiReq := normalizedToOpenAIRequest(norm)
	if len(openaiReq.Messages) != 1 || len(openaiReq.Messages[0].ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %+v", openaiReq.Messages)
	}
	tc := openaiReq.Messages[0].ToolCalls[0]
	if tc.Function.Name != "get_weather" || tc.ID != "tu_1" {
		t.Fatalf("tool call mismatch: %+v", tc)
	}
}

func TestFinishReasonMapping(t *testing.T) {
	cases := map[string]string{
		"stop":           "end_turn",
		"length":         "max_tokens",
		"tool_calls":     "tool_use",
		"content_filter": "end_turn",
	}
	for in, want := range cases {
		if got := openAIFinishReasonToNormalized(in); got != want {
			t.Errorf("openAIFinishReasonToNormalized(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOpenAIResponseToNormalizedUsage(t *testing.T) {
	resp := &types.OpenAIResponse{
		Model: "gpt-4o",
		Choices: []types.OpenAIChoice{{
			Message:      types.OpenAIMessage{Role: "assistant", Content: types.OpenAIMessageContent{types.TextBlock("hi")}},
			FinishReason: "stop",
		}},
		Usage: types.OpenAIUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			Cost:             0.002,
		},
	}
	norm := openAIResponseToNormalized(resp)
	if norm.Usage.InputTokens != 10 || norm.Usage.OutputTokens != 5 || norm.Usage.CostUSD != 0.002 {
		t.Fatalf("usage mismatch: %+v", norm.Usage)
	}
	if norm.StopReason != "end_turn" {
		t.Fatalf("expected mapped stop reason, got %q", norm.StopReason)
	}
}

// Anthropic's API requires `input` on every tool_use block, even {} for a
// no-argument call — a block replayed as conversation history with the key
// missing 400s the whole request (issue #78). This asserts the raw outbound
// wire bytes, not a struct round-trip: a struct-shaped assertion can't see
// an omitempty (or a type-dependent MarshalJSON) drop the key, since the Go
// struct still "has" the field either way.
func TestNoArgToolUseEmitsEmptyInputObjectOnWire(t *testing.T) {
	tr := NewDefaultTranslator()
	norm := &types.NormalizedRequest{
		Model:     "claude-3-opus-20250219",
		MaxTokens: 100,
		Messages: []types.Message{
			{Role: "assistant", Content: []types.ContentBlock{
				{Type: "tool_use", ToolUseID: "toolu_01", ToolName: "kanban_show", ToolInput: nil},
			}},
		},
	}

	req, err := tr.NormalizedToAnthropicRequest(norm)
	if err != nil {
		t.Fatalf("NormalizedToAnthropicRequest: %v", err)
	}

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	messages := decoded["messages"].([]interface{})
	content := messages[0].(map[string]interface{})["content"].([]interface{})
	block := content[0].(map[string]interface{})
	input, present := block["input"]
	if !present {
		t.Fatalf("wire JSON = %s, want an \"input\" key present on the tool_use block", raw)
	}
	inputMap, ok := input.(map[string]interface{})
	if !ok || len(inputMap) != 0 {
		t.Fatalf("wire JSON = %s, want \"input\":{} exactly", raw)
	}

	if !bytes.Contains(raw, []byte(`"input":{}`)) {
		t.Fatalf("wire JSON = %s, want it to contain the literal %q", raw, `"input":{}`)
	}
}

// A tool_use block with real arguments must round-trip unchanged — this fix
// only touches the nil/empty case (removing an always-false omitempty check
// can't change behavior when the map is already non-empty), but the whole
// point of asserting raw bytes above is that a regression here would be easy
// to introduce by over-correcting (e.g. by forcing input non-nil elsewhere).
func TestToolUseWithArgumentsStillEmitsThemOnWire(t *testing.T) {
	tr := NewDefaultTranslator()
	norm := &types.NormalizedRequest{
		Model:     "claude-3-opus-20250219",
		MaxTokens: 100,
		Messages: []types.Message{
			{Role: "assistant", Content: []types.ContentBlock{
				{Type: "tool_use", ToolUseID: "toolu_02", ToolName: "get_weather", ToolInput: map[string]interface{}{"city": "NYC"}},
			}},
		},
	}

	req, err := tr.NormalizedToAnthropicRequest(norm)
	if err != nil {
		t.Fatalf("NormalizedToAnthropicRequest: %v", err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"input":{"city":"NYC"}`)) {
		t.Fatalf("wire JSON = %s, want the populated input preserved verbatim", raw)
	}
}

// A non-tool_use block (text here) must never pick up an `input` key — the
// MarshalJSON override that forces `input` onto a tool_use block is keyed on
// block type specifically so it does not leak onto text/thinking/tool_result
// blocks, which never carry one on the real Anthropic wire.
func TestTextBlockNeverGetsAnInputKeyOnWire(t *testing.T) {
	tr := NewDefaultTranslator()
	norm := &types.NormalizedRequest{
		Model:     "claude-3-opus-20250219",
		MaxTokens: 100,
		Messages: []types.Message{
			{Role: "assistant", Content: []types.ContentBlock{
				{Type: "text", Text: "hello"},
			}},
		},
	}

	req, err := tr.NormalizedToAnthropicRequest(norm)
	if err != nil {
		t.Fatalf("NormalizedToAnthropicRequest: %v", err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(raw, []byte(`"input"`)) {
		t.Fatalf("wire JSON = %s, a text block must never carry an \"input\" key", raw)
	}
}
