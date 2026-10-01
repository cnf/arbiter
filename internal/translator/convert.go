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
// Anthropic client-facing attachment parsing is intentionally minimal (small
// market; see issue #23): `source` is read for both its wire shapes — inline
// base64 ({"type":"base64","media_type":...,"data":...}) and a URL
// ({"type":"url","url":...}) — and `title` becomes the attachment's Name,
// mirroring OpenAI's `filename`. Anything else in `source` (an unrecognized
// source `type`, or one missing its payload) degrades to an attachment with
// no data rather than erroring, matching the OpenAI side's "skip, don't
// fail" behavior for content Arbiter doesn't fully understand.

func anthropicContentToBlock(c types.AnthropicContent) types.ContentBlock {
	switch c.Type {
	case "thinking":
		// A thinking block's text lives under Anthropic's "thinking" key, not
		// "text" — reading c.Text here yields an empty string and no error,
		// which is how the text was being lost.
		return types.ContentBlock{Type: c.Type, Text: c.Thinking, Signature: c.Signature}
	case "text":
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
	case "image", "document":
		mediaType, data, isURL := anthropicSourceToParts(c.Source)
		if c.Type == "image" {
			return types.AttachmentImageBlock(mediaType, data, "", isURL)
		}
		return types.AttachmentBlock(mediaType, data, c.Title, isURL)
	default:
		return types.ContentBlock{Type: "text", Text: c.Text}
	}
}

// anthropicSourceToParts reads an Anthropic content block's `source` map,
// which comes in exactly two shapes on the wire: base64 (media_type + data)
// or a url. Source is untyped (map[string]interface{}) because nothing
// inbound validated it before this — see AnthropicContent.Source — so every
// field read here is defensive against a missing or wrong-typed key.
func anthropicSourceToParts(source map[string]interface{}) (mediaType, data string, isURL bool) {
	if source == nil {
		return "", "", false
	}
	if t, _ := source["type"].(string); t == "url" {
		url, _ := source["url"].(string)
		return "", url, true
	}
	mediaType, _ = source["media_type"].(string)
	data, _ = source["data"].(string)
	return mediaType, data, false
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
	case "thinking":
		// Mirror of the inbound split: a thinking block must be written back
		// under its own key or the upstream sees a thinking block with no
		// text.
		return types.AnthropicContent{Type: cb.Type, Thinking: cb.Text, Signature: cb.Signature}
	case "text":
		return types.AnthropicContent{Type: cb.Type, Text: cb.Text}
	case "tool_use":
		// Input may be nil here (a no-argument tool call); that's fine —
		// AnthropicContent.MarshalJSON defaults a nil/empty Input to `{}`
		// on the wire for tool_use blocks specifically (issue #78).
		return types.AnthropicContent{Type: "tool_use", ID: cb.ToolUseID, Name: cb.ToolName, Input: cb.ToolInput}
	case "tool_result":
		return types.AnthropicContent{Type: "tool_result", ToolUseID: cb.ToolResultForID, Content: cb.ToolResult, IsError: cb.ToolIsError}
	case "attachment":
		// Outbound only for now: Anthropic's client-facing side is not a
		// priority (small market), but an attachment arriving from an OpenAI
		// client must still be able to reach an Anthropic-speaking upstream,
		// or routing it there would silently drop it.
		//
		// The block type is decided by media type, not by whether a filename
		// is present: `image` for image/*, `document` for everything else.
		// Source is either inline base64 or a URL the upstream fetches.
		source := map[string]interface{}{"type": "base64", "media_type": cb.MediaType, "data": cb.Data}
		if cb.IsURL {
			source = map[string]interface{}{"type": "url", "url": cb.Data}
		}
		blockType := "document"
		if cb.IsImage() {
			blockType = "image"
		}
		out := types.AnthropicContent{Type: blockType, Source: source}
		if cb.Name != "" {
			// Anthropic calls this a title; it is the closest thing to the
			// OpenAI filename and is what keeps a document recognisable.
			out.Title = cb.Name
		}
		return out
	default:
		return types.AnthropicContent{Type: "text", Text: cb.Text}
	}
}

// --- request conversion ---

// markLastBlock marks the final content block of the last message that has one,
// returning whether it marked anything.
//
// This is the rolling cache breakpoint. Anthropic caches a prefix up to a
// breakpoint, and a conversation only grows at its end, so marking the last
// block of the newest turn makes every completed turn a cacheable extension of
// the one before it: the next request reads everything but the new turn from
// cache. A marker on the system prompt alone would only ever cache that prompt,
// which is the smaller half of an agent conversation's input.
//
// The scan runs backwards so the index arithmetic stays on the value it
// dereferences: scanning forward needs `len(messages)-1-i` in the body, which is
// the shape where an off-by-one marks the wrong message.
//
// A blockless message is skipped rather than treated as the end of the
// conversation. Messages with no content carry no tokens, so marking the
// preceding message caches exactly the same prefix while keeping the marker on
// an object Anthropic can attach it to — an empty content array has no block to
// carry `cache_control`, so marking one would put the key nowhere and leave the
// request with no rolling breakpoint at all.
func markLastBlock(messages []types.AnthropicMessage) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		blocks := messages[i].Content
		if len(blocks) == 0 {
			continue
		}
		blocks[len(blocks)-1].CacheControl = types.NewAnthropicCacheControl()
		return true
	}
	return false
}

// anthropicCacheBreakpointLimit is Anthropic's documented maximum: four
// `cache_control` markers per request, beyond which the upstream rejects it.
// Arbiter emits at most two (the system prompt and the rolling last block),
// so this is a regression guard rather than a runtime constraint.
const anthropicCacheBreakpointLimit = 4

// markTools caches the frozen tool prefix. Tools are stable for a whole
// conversation and for every conversation sharing a route, and they sit before
// the system prompt in Anthropic's cache hierarchy, so marking the last one
// puts both of them — plus the messages — inside one cacheable prefix whenever
// the system prompt carries no marker of its own.
//
// The output's slice is fresh from the conversion above, so mutating an element
// cannot reach back into the caller's tools.
func markTools(tools []types.AnthropicTool) {
	if len(tools) == 0 {
		return
	}
	tools[len(tools)-1].CacheControl = types.NewAnthropicCacheControl()
}

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
		// Field-by-field rather than a conversion: AnthropicTool carries
		// outbound-only fields (the cache marker) that types.Tool has no home
		// for, so the two shapes are no longer identical and a conversion no
		// longer compiles. Copying only what is shared is what keeps an
		// outbound-only attribute from leaking into the normalized type.
		tools[i] = types.Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}
	}
	return &types.NormalizedRequest{
		Messages:     messages,
		Model:        req.Model,
		MaxTokens:    req.MaxTokens,
		Temperature:  req.Temperature,
		SystemPrompt: string(req.System),
		Tools:        tools,
		Stream:       req.Stream,
		Thinking:     req.Thinking,
		OutputEffort: anthropicOutputEffortOf(req),
	}
}

// anthropicOutputEffortOf reads the effort knob out of a request's
// output_config, tolerating an absent block so the caller need not nil-check.
func anthropicOutputEffortOf(req *types.AnthropicRequest) string {
	if req.OutputConfig == nil {
		return ""
	}
	return req.OutputConfig.Effort
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
	// The rolling breakpoint: the conversation only grows at its end, so
	// marking the newest turn is what makes each completed turn cacheable
	// input for the next one. This is the breakpoint a long agent
	// conversation is actually paid for.
	markLastBlock(messages)
	tools := make([]types.AnthropicTool, len(req.Tools))
	for i, t := range req.Tools {
		// Field-by-field, matching the inbound copy above: the shapes differ
		// now, and this direction deliberately leaves CacheControl nil — it is
		// set below, on the last tool only.
		tools[i] = types.AnthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}
	}
	// Tool definitions are frozen for a conversation, so the last one carries a
	// cache breakpoint: a prefix that never changes is the cheapest thing to
	// start a cache at, and it repeats one turn later whether or not the client
	// sends its tools again (an OpenAI client drops them once the model stops
	// calling them).
	markTools(tools)
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = anthropicDefaultMaxTokens
	}
	out := &types.AnthropicRequest{
		Model:       req.Model,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
		System:      types.AnthropicSystem(req.SystemPrompt),
		Messages:    messages,
		Tools:       tools,
		Stream:      req.Stream,
		Thinking:    req.Thinking,
	}
	// Only build output_config when there is an effort to send: an empty
	// block would put `"output_config": {}` on the wire, which is a different
	// request from omitting the field.
	if req.OutputEffort != "" {
		out.OutputConfig = &types.AnthropicOutputConfig{Effort: req.OutputEffort}
	}
	return out
}

func openAIRequestToNormalized(req *types.OpenAIRequest) *types.NormalizedRequest {
	var systemPrompt string
	var messages []types.Message

	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			// Only text reaches the system prompt: a system message carrying an
			// attachment has no sensible place to put it, and silently dropping
			// it is better than folding base64 into the prompt string.
			if text := m.Content.String(); text != "" {
				if systemPrompt != "" {
					systemPrompt += "\n\n"
				}
				systemPrompt += text
			}
		case "tool":
			messages = append(messages, types.Message{
				Role: "user", // Anthropic carries tool_result blocks in a user-role message
				Content: []types.ContentBlock{{
					Type:            "tool_result",
					ToolResultForID: m.ToolCallID,
					ToolResult:      m.Content.String(),
				}},
			})
		default: // "user", "assistant"
			// The content blocks carry attachments straight through — they are
			// already in the internal form, so there is nothing to convert.
			blocks := make([]types.ContentBlock, 0, len(m.Content)+len(m.ToolCalls))
			blocks = append(blocks, m.Content...)
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
		Stream:       req.Stream,
		// The OpenAI spelling of the reasoning dial. Read as a request fact
		// like the Anthropic leg above, so it reaches the routing signals
		// (#79) and the recorded row instead of being dropped at the door.
		// Note this is the ONLY effort source for OpenAI-format traffic:
		// Anthropic's `output_config` has no home on this wire shape, so a
		// client sending the Anthropic knob here stays invisible.
		OutputEffort: req.ReasoningEffort,
	}
}

func normalizedToOpenAIRequest(req *types.NormalizedRequest) *types.OpenAIRequest {
	var messages []types.OpenAIMessage
	if req.SystemPrompt != "" {
		messages = append(messages, types.OpenAIMessage{
			Role:    "system",
			Content: types.OpenAIMessageContent{types.TextBlock(req.SystemPrompt)},
		})
	}

	for _, m := range req.Messages {
		var parts []types.ContentBlock
		var toolCalls []types.OpenAIToolCall

		for _, cb := range m.Content {
			switch cb.Type {
			case "text", "thinking":
				parts = append(parts, types.TextBlock(cb.Text))
			case "attachment":
				// Carried through as its own part. Dropping it here is what
				// would make a vision or document request silently answer a
				// different question, so it is appended even though the
				// message may then serialize as the array form.
				parts = append(parts, cb)
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
					Content:    types.OpenAIMessageContent{types.TextBlock(cb.ToolResult)},
					ToolCallID: cb.ToolResultForID,
				})
			}
		}

		if len(parts) > 0 || len(toolCalls) > 0 {
			messages = append(messages, types.OpenAIMessage{
				Role:      m.Role,
				Content:   parts,
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

	out := &types.OpenAIRequest{
		Model:       req.Model,
		Messages:    messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Tools:       tools,
		Stream:      req.Stream,
	}
	// Ask for the terminal usage chunk on every stream. Arbiter needs it
	// regardless of whether the client asked: token counts, cost and cache-read
	// figures are what the event store records, and without this flag a stream
	// reports none of them. The outbound body is rebuilt from the normalized
	// request, so a client's own stream_options would otherwise be dropped here
	// and the upstream would never be asked.
	if req.Stream {
		out.StreamOptions = &types.OpenAIStreamOptions{IncludeUsage: true}
	}
	// The reasoning dial travels under this format's own spelling. The
	// normalized request may have been filled from EITHER ingress (an
	// Anthropic client routed to an OpenAI-speaking upstream), and the wire
	// field is chosen by the destination, not by where the value came from —
	// so `output_config.effort` from an Anthropic client becomes
	// `reasoning_effort` here rather than being dropped. Omitted when empty,
	// so a client that asked for nothing does not gain a dial.
	out.ReasoningEffort = req.OutputEffort
	return out
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
	// A response is text and tool calls; the response side never carries
	// attachments, so its content string is the whole story.
	if text := choice.Message.Content.String(); text != "" {
		blocks = append(blocks, types.TextBlock(text))
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
				Content:   types.OpenAIMessageContent{types.TextBlock(content.String())},
				ToolCalls: toolCalls,
			},
			FinishReason: normalizedToOpenAIFinishReason(resp.StopReason),
		}},
		Usage: types.OpenAIUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
			Cost:             resp.Usage.CostUSD,
			PromptDetails:    buildOpenAIPromptDetails(resp.Usage.CacheRead),
		},
	}
}
