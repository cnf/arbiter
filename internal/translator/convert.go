package translator

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/cnf/arbiter/pkg/types"
)

// --- stop reason / finish reason mapping ---
//
// NormalizedResponse.StopReason uses Anthropic's vocabulary as the
// canonical form ("end_turn", "max_tokens", "stop_sequence", "tool_use")
// since it's the richer of the two. OpenAI's finish_reason is mapped onto
// it and back.

func openAIFinishReasonToNormalized(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "end_turn" // no exact Anthropic equivalent; closest is a plain stop
	default:
		return reason
	}
}

func normalizedToOpenAIFinishReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "stop_sequence":
		return "stop"
	default:
		return "stop"
	}
}

// --- content block conversion ---
//
// NOTE: image/document blocks aren't translated yet (v1 scope is text +
// tools only, per the project's deferred-features list). Unknown Anthropic
// content types fall back to their raw text field, which is empty for pure
// image blocks — they're silently dropped rather than erroring, so a
// vision-capable request degrades to text-only instead of failing outright.

func anthropicContentToBlock(c types.AnthropicContent) types.ContentBlock {
	switch c.Type {
	case "text", "thinking":
		return types.ContentBlock{Type: c.Type, Text: c.Text}
	case "tool_use":
		return types.ContentBlock{Type: "tool_use", ToolUseID: c.ID, ToolName: c.Name, ToolInput: c.Input}
	case "tool_result":
		return types.ContentBlock{
			Type:            "tool_result",
			ToolResultForID: c.ToolUseID,
			ToolResult:      toolResultContentToText(c.Content),
			ToolIsError:     c.IsError,
		}
	default:
		return types.ContentBlock{Type: "text", Text: c.Text}
	}
}

// toolResultContentToText flattens a tool_result's Content field, which per
// the Anthropic API can be either a plain string or a list of content
// blocks (e.g. text blocks). Only text is preserved.
func toolResultContentToText(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if t, _ := m["type"].(string); t == "text" {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func blockToAnthropicContent(cb types.ContentBlock) types.AnthropicContent {
	switch cb.Type {
	case "text", "thinking":
		return types.AnthropicContent{Type: cb.Type, Text: cb.Text}
	case "tool_use":
		return types.AnthropicContent{Type: "tool_use", ID: cb.ToolUseID, Name: cb.ToolName, Input: cb.ToolInput}
	case "tool_result":
		return types.AnthropicContent{Type: "tool_result", ToolUseID: cb.ToolResultForID, Content: cb.ToolResult, IsError: cb.ToolIsError}
	default:
		return types.AnthropicContent{Type: "text", Text: cb.Text}
	}
}

// --- request conversion ---

func anthropicRequestToNormalized(req *types.AnthropicRequest) *types.NormalizedRequest {
	messages := make([]types.Message, len(req.Messages))
	for i, m := range req.Messages {
		blocks := make([]types.ContentBlock, len(m.Content))
		for j, c := range m.Content {
			blocks[j] = anthropicContentToBlock(c)
		}
		messages[i] = types.Message{Role: m.Role, Content: blocks}
	}
	tools := make([]types.Tool, len(req.Tools))
	for i, t := range req.Tools {
		tools[i] = types.Tool(t)
	}
	return &types.NormalizedRequest{
		Messages:     messages,
		Model:        req.Model,
		MaxTokens:    req.MaxTokens,
		Temperature:  req.Temperature,
		SystemPrompt: req.System,
		Tools:        tools,
	}
}

// anthropicDefaultMaxTokens is used when a NormalizedRequest carries no
// MaxTokens (e.g. it came in via OpenAI, where the field is optional) but
// needs to go out as Anthropic, which requires max_tokens > 0.
const anthropicDefaultMaxTokens = 4096

func normalizedToAnthropicRequest(req *types.NormalizedRequest) *types.AnthropicRequest {
	messages := make([]types.AnthropicMessage, len(req.Messages))
	for i, m := range req.Messages {
		content := make([]types.AnthropicContent, len(m.Content))
		for j, cb := range m.Content {
			content[j] = blockToAnthropicContent(cb)
		}
		messages[i] = types.AnthropicMessage{Role: m.Role, Content: content}
	}
	tools := make([]types.AnthropicTool, len(req.Tools))
	for i, t := range req.Tools {
		tools[i] = types.AnthropicTool(t)
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = anthropicDefaultMaxTokens
	}
	return &types.AnthropicRequest{
		Model:       req.Model,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
		System:      req.SystemPrompt,
		Messages:    messages,
		Tools:       tools,
	}
}

func openAIRequestToNormalized(req *types.OpenAIRequest) *types.NormalizedRequest {
	var systemPrompt string
	var messages []types.Message

	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			if systemPrompt != "" {
				systemPrompt += "\n\n"
			}
			systemPrompt += m.Content
		case "tool":
			messages = append(messages, types.Message{
				Role: "user", // Anthropic carries tool_result blocks in a user-role message
				Content: []types.ContentBlock{{
					Type:            "tool_result",
					ToolResultForID: m.ToolCallID,
					ToolResult:      m.Content,
				}},
			})
		default: // "user", "assistant"
			var blocks []types.ContentBlock
			if m.Content != "" {
				blocks = append(blocks, types.TextBlock(m.Content))
			}
			for _, tc := range m.ToolCalls {
				var input map[string]interface{}
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &input) // best-effort; malformed args become a nil input map
				blocks = append(blocks, types.ContentBlock{
					Type:      "tool_use",
					ToolUseID: tc.ID,
					ToolName:  tc.Function.Name,
					ToolInput: input,
				})
			}
			messages = append(messages, types.Message{Role: m.Role, Content: blocks})
		}
	}

	tools := make([]types.Tool, len(req.Tools))
	for i, t := range req.Tools {
		tools[i] = types.Tool{Name: t.Function.Name, Description: t.Function.Description, InputSchema: t.Function.Parameters}
	}

	return &types.NormalizedRequest{
		Messages:     messages,
		Model:        req.Model,
		MaxTokens:    req.MaxTokens,
		Temperature:  req.Temperature,
		SystemPrompt: systemPrompt,
		Tools:        tools,
	}
}

func normalizedToOpenAIRequest(req *types.NormalizedRequest) *types.OpenAIRequest {
	var messages []types.OpenAIMessage
	if req.SystemPrompt != "" {
		messages = append(messages, types.OpenAIMessage{Role: "system", Content: req.SystemPrompt})
	}

	for _, m := range req.Messages {
		var textParts []string
		var toolCalls []types.OpenAIToolCall

		for _, cb := range m.Content {
			switch cb.Type {
			case "text", "thinking":
				textParts = append(textParts, cb.Text)
			case "tool_use":
				argsJSON, _ := json.Marshal(cb.ToolInput)
				toolCalls = append(toolCalls, types.OpenAIToolCall{
					ID:   cb.ToolUseID,
					Type: "function",
					Function: types.OpenAIFunctionCall{
						Name:      cb.ToolName,
						Arguments: string(argsJSON),
					},
				})
			case "tool_result":
				// OpenAI represents tool results as their own role:"tool"
				// message rather than a block inside a user message.
				// NOTE: emitted immediately, so a message that mixes
				// tool_result blocks with text/tool_use blocks (rare in
				// practice — Anthropic tool_result messages are normally
				// pure) can come out reordered relative to the original.
				messages = append(messages, types.OpenAIMessage{
					Role:       "tool",
					Content:    cb.ToolResult,
					ToolCallID: cb.ToolResultForID,
				})
			default:
				textParts = append(textParts, cb.Text)
			}
		}

		if len(textParts) > 0 || len(toolCalls) > 0 {
			messages = append(messages, types.OpenAIMessage{
				Role:      m.Role,
				Content:   strings.Join(textParts, "\n"),
				ToolCalls: toolCalls,
			})
		}
	}

	tools := make([]types.OpenAITool, len(req.Tools))
	for i, t := range req.Tools {
		tools[i] = types.OpenAITool{
			Type: "function",
			Function: types.OpenAIFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		}
	}

	return &types.OpenAIRequest{
		Model:       req.Model,
		Messages:    messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Tools:       tools,
	}
}

// --- response conversion ---

func anthropicResponseToNormalized(resp *types.AnthropicResponse) *types.NormalizedResponse {
	blocks := make([]types.ContentBlock, len(resp.Content))
	for i, c := range resp.Content {
		blocks[i] = anthropicContentToBlock(c)
	}
	return &types.NormalizedResponse{
		Content:    blocks,
		StopReason: resp.StopReason,
		Model:      resp.Model,
		Usage: types.Usage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			CacheRead:    resp.Usage.CacheReadInputTokens,
			CacheWrite:   resp.Usage.CacheCreationInputTokens,
		},
	}
}

func openAIResponseToNormalized(resp *types.OpenAIResponse) *types.NormalizedResponse {
	choice := resp.Choices[0]

	var blocks []types.ContentBlock
	if choice.Message.Content != "" {
		blocks = append(blocks, types.TextBlock(choice.Message.Content))
	}
	for _, tc := range choice.Message.ToolCalls {
		var input map[string]interface{}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
		blocks = append(blocks, types.ContentBlock{
			Type:      "tool_use",
			ToolUseID: tc.ID,
			ToolName:  tc.Function.Name,
			ToolInput: input,
		})
	}

	usage := types.Usage{
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
		CostUSD:      resp.Usage.Cost, // OpenRouter; 0 for plain OpenAI
	}
	if cached, ok := resp.Usage.PromptDetails["cached_tokens"]; ok {
		if f, ok := cached.(float64); ok {
			usage.CacheRead = int(f)
		}
	}
	if upstreamCost, ok := resp.Usage.CostDetails["upstream_inference_cost"]; ok && upstreamCost > usage.CostUSD {
		usage.CostUSD = upstreamCost // OpenRouter sometimes reports the raw upstream cost separately from its markup-inclusive Cost field
	}

	return &types.NormalizedResponse{
		Content:    blocks,
		StopReason: openAIFinishReasonToNormalized(choice.FinishReason),
		Model:      resp.Model,
		Usage:      usage,
	}
}

func normalizedToAnthropicResponse(resp *types.NormalizedResponse) *types.AnthropicResponse {
	content := make([]types.AnthropicContent, len(resp.Content))
	for i, cb := range resp.Content {
		content[i] = blockToAnthropicContent(cb)
	}
	return &types.AnthropicResponse{
		ID:         "msg_" + resp.TraceID,
		Type:       "message",
		Role:       "assistant",
		Content:    content,
		Model:      resp.Model,
		StopReason: resp.StopReason,
		Usage: types.AnthropicUsage{
			InputTokens:              resp.Usage.InputTokens,
			OutputTokens:             resp.Usage.OutputTokens,
			CacheReadInputTokens:     resp.Usage.CacheRead,
			CacheCreationInputTokens: resp.Usage.CacheWrite,
		},
	}
}

func normalizedToOpenAIResponse(resp *types.NormalizedResponse) *types.OpenAIResponse {
	var content strings.Builder
	var toolCalls []types.OpenAIToolCall

	for _, cb := range resp.Content {
		switch cb.Type {
		case "text", "thinking":
			content.WriteString(cb.Text)
		case "tool_use":
			argsJSON, _ := json.Marshal(cb.ToolInput)
			toolCalls = append(toolCalls, types.OpenAIToolCall{
				ID:   cb.ToolUseID,
				Type: "function",
				Function: types.OpenAIFunctionCall{
					Name:      cb.ToolName,
					Arguments: string(argsJSON),
				},
			})
		}
	}

	return &types.OpenAIResponse{
		ID:      "chatcmpl-" + resp.TraceID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   resp.Model,
		Choices: []types.OpenAIChoice{{
			Index: 0,
			Message: types.OpenAIMessage{
				Role:      "assistant",
				Content:   content.String(),
				ToolCalls: toolCalls,
			},
			FinishReason: normalizedToOpenAIFinishReason(resp.StopReason),
		}},
		Usage: types.OpenAIUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
			Cost:             resp.Usage.CostUSD,
		},
	}
}
