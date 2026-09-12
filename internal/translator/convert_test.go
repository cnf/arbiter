package translator

import (
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

func TestOpenAIRequestSystemMessageExtraction(t *testing.T) {
	req := &types.OpenAIRequest{
		Model: "gpt-4o",
		Messages: []types.OpenAIMessage{
			{Role: "system", Content: "be concise"},
			{Role: "user", Content: "hello"},
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
			Message:      types.OpenAIMessage{Role: "assistant", Content: "hi"},
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
