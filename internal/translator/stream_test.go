package translator

import (
	"encoding/json"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

func TestAnthropicStreamEventTranslation(t *testing.T) {
	evt := &AnthropicStreamEvent{
		Type:  "content_block_delta",
		Index: intPtr(0),
		Delta: &AnthropicStreamDelta{
			Type: "text_delta",
			Text: "Hello, world!",
		},
	}

	// Translate to normalized
	normalized := AnthropicStreamEventToNormalized(evt)
	if normalized.Type != "content_block_delta" {
		t.Fatalf("expected type content_block_delta, got %s", normalized.Type)
	}
	if normalized.TextDelta != "Hello, world!" {
		t.Fatalf("expected text delta, got %q", normalized.TextDelta)
	}

	// Translate back to Anthropic
	roundtrip := NormalizedToAnthropicStreamEvent(normalized)
	if roundtrip.Type != evt.Type {
		t.Fatalf("roundtrip type mismatch: %s != %s", roundtrip.Type, evt.Type)
	}
	if roundtrip.Delta.Text != evt.Delta.Text {
		t.Fatalf("roundtrip text mismatch: %q != %q", roundtrip.Delta.Text, evt.Delta.Text)
	}

	// Should be JSON-serializable
	b, err := json.Marshal(roundtrip)
	if err != nil {
		t.Fatalf("marshal roundtrip: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("marshaled to empty bytes")
	}
}

func TestOpenAIStreamEventTranslation(t *testing.T) {
	evt := &OpenAIStreamEvent{
		ID:      "chatcmpl-test",
		Model:   "gpt-4",
		Object:  "text_completion.chunk",
		Created: 1234567890,
		Choices: []OpenAIStreamChoice{{
			Index: 0,
			Delta: OpenAIStreamDelta{
				Content: "Hello there!",
			},
		}},
	}

	// Translate to normalized
	normalized := OpenAIStreamEventToNormalized(evt, false)
	if normalized.Type != "content_block_delta" {
		t.Fatalf("expected type content_block_delta, got %s", normalized.Type)
	}
	if normalized.TextDelta != "Hello there!" {
		t.Fatalf("expected text delta, got %q", normalized.TextDelta)
	}

	// Translate to Anthropic format
	messageID := "test-msg-id"
	anthEvt := NormalizedToAnthropicStreamEvent(normalized)
	if anthEvt.Type != "content_block_delta" {
		t.Fatalf("expected translated type content_block_delta, got %s", anthEvt.Type)
	}
	if anthEvt.Delta.Text != "Hello there!" {
		t.Fatalf("expected translated text, got %q", anthEvt.Delta.Text)
	}

	// Translate to OpenAI format
	oaiEvt := NormalizedToOpenAIStreamEvent(normalized, messageID, 1234567890)
	b, err := json.Marshal(oaiEvt)
	if err != nil {
		t.Fatalf("marshal openai event: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("marshaled to empty bytes")
	}
}

func TestNormalizedToOpenAIStreamEventFidelity(t *testing.T) {
	created := int64(1700000000)

	// message_start carries model + role, with the shared ID/created/model
	start := &types.NormalizedStreamEvent{
		Type:         "message_start",
		MessageID:    "msg_123",
		MessageModel: "gpt-4o",
	}
	oaiStart := NormalizedToOpenAIStreamEvent(start, "trace-1", created)
	if oaiStart.ID != "chatcmpl-trace-1" {
		t.Errorf("ID = %q, want chatcmpl-trace-1", oaiStart.ID)
	}
	if oaiStart.Created != created {
		t.Errorf("Created = %d, want %d", oaiStart.Created, created)
	}
	if oaiStart.Model != "gpt-4o" {
		t.Errorf("Model = %q, want gpt-4o", oaiStart.Model)
	}
	if oaiStart.Object != "chat.completion.chunk" {
		t.Errorf("Object = %q, want chat.completion.chunk", oaiStart.Object)
	}
	if oaiStart.Choices[0].Delta.Role != "assistant" {
		t.Errorf("Delta.Role = %q, want assistant", oaiStart.Choices[0].Delta.Role)
	}

	// a later content chunk also carries model/created (OpenAI sends model
	// and created on every chunk of one completion)
	delta := &types.NormalizedStreamEvent{
		Type:      "content_block_delta",
		DeltaType: "text_delta",
		TextDelta: "hello",
	}
	oaiDelta := NormalizedToOpenAIStreamEvent(delta, "trace-1", created)
	if oaiStart.ID != "chatcmpl-trace-1" {
		t.Errorf("ID = %q, want chatcmpl-trace-1", oaiStart.ID)
	}
	if oaiDelta.Created != created {
		t.Errorf("Created = %d, want %d", oaiDelta.Created, created)
	}

	// message_stop maps the normalized stop reason back to OpenAI's
	// finish_reason vocabulary ("end_turn" -> "stop")
	stop := &types.NormalizedStreamEvent{
		Type:              "message_stop",
		MessageStopReason: "end_turn",
	}
	oaiStop := NormalizedToOpenAIStreamEvent(stop, "trace-1", created)
	if oaiStop.Choices[0].FinishReason == nil || *oaiStop.Choices[0].FinishReason != "stop" {
		got := "<nil>"
		if oaiStop.Choices[0].FinishReason != nil {
			got = *oaiStop.Choices[0].FinishReason
		}
		t.Errorf("FinishReason = %q, want stop", got)
	}
}

func TestStreamMessageStartAndStop(t *testing.T) {
	// Anthropic message_start event
	startEvt := &AnthropicStreamEvent{
		Type: "message_start",
		Message: &AnthropicStreamMessage{
			ID:    "msg_123",
			Model: "claude-3-haiku",
			Usage: types.AnthropicUsage{InputTokens: 100},
		},
	}

	normalized := AnthropicStreamEventToNormalized(startEvt)
	if normalized.Type != "message_start" {
		t.Fatalf("expected message_start, got %s", normalized.Type)
	}
	if normalized.MessageID != "msg_123" {
		t.Fatalf("expected message ID msg_123, got %s", normalized.MessageID)
	}
	if normalized.InputTokens != 100 {
		t.Fatalf("expected 100 input tokens, got %d", normalized.InputTokens)
	}

	// Anthropic message_stop event
	stopEvt := &AnthropicStreamEvent{
		Type: "message_stop",
		Message: &AnthropicStreamMessage{
			ID:         "msg_123",
			StopReason: "end_turn",
		},
	}

	normalized = AnthropicStreamEventToNormalized(stopEvt)
	if normalized.Type != "message_stop" {
		t.Fatalf("expected message_stop, got %s", normalized.Type)
	}
	if normalized.MessageStopReason != "end_turn" {
		t.Fatalf("expected stop_reason end_turn, got %s", normalized.MessageStopReason)
	}
}

// --- fidelity: relay what the upstream sent, in the order it sent it ---

// The upstream streams a tool call as one fragment per chunk: id and function
// name on the first, a slice of the arguments JSON on every later one. The
// translator used to detect this delta type and then carry none of it, turning
// 19 real tool-call chunks into 19 empty deltas — the client receives a 200
// with no content and an agentic client cannot terminate its loop.
func TestOpenAIStreamCarriesToolCallFragments(t *testing.T) {
	// The real first fragment: id + name, empty arguments.
	first := &OpenAIStreamEvent{
		ID: "gen-1", Model: "openrouter/free",
		Choices: []OpenAIStreamChoice{{Index: 0, Delta: OpenAIStreamDelta{
			ToolCalls: []OpenAIStreamToolCall{{
				Index: 0, ID: "call_6bd99f1320a74af29d50bdea", Type: "function",
				Function: OpenAIStreamToolCallFunc{Name: "webfetch", Arguments: ""},
			}},
		}}},
	}
	norm := OpenAIStreamEventToNormalized(first, false)
	if norm.DeltaType != "tool_use_delta" {
		t.Fatalf("DeltaType = %q, want tool_use_delta", norm.DeltaType)
	}
	if norm.ToolCallID != "call_6bd99f1320a74af29d50bdea" {
		t.Errorf("ToolCallID = %q, want the upstream call id", norm.ToolCallID)
	}
	if norm.ToolCallName != "webfetch" {
		t.Errorf("ToolCallName = %q, want webfetch", norm.ToolCallName)
	}

	// The outbound side must put them back on the wire.
	out := NormalizedToOpenAIStreamEvent(norm, "trace-1", 123)
	if len(out.Choices[0].Delta.ToolCalls) != 1 {
		t.Fatalf("outbound tool_calls = %d, want 1", len(out.Choices[0].Delta.ToolCalls))
	}
	got := out.Choices[0].Delta.ToolCalls[0]
	if got.ID != "call_6bd99f1320a74af29d50bdea" || got.Function.Name != "webfetch" {
		t.Errorf("outbound call = %+v, want id and name preserved", got)
	}

	// A later fragment carries only an arguments slice, and must not synthesise
	// an id or name of its own.
	later := &OpenAIStreamEvent{
		ID: "gen-1", Model: "openrouter/free",
		Choices: []OpenAIStreamChoice{{Index: 0, Delta: OpenAIStreamDelta{
			ToolCalls: []OpenAIStreamToolCall{{
				Index: 0, Type: "function",
				Function: OpenAIStreamToolCallFunc{Arguments: "{\"url\": "},
			}},
		}}},
	}
	normLater := OpenAIStreamEventToNormalized(later, false)
	if normLater.ToolCallArgs != "{\"url\": " {
		t.Errorf("ToolCallArgs = %q, want the fragment verbatim", normLater.ToolCallArgs)
	}
	if normLater.ToolCallID != "" || normLater.ToolCallName != "" {
		t.Errorf("a mid-stream fragment must not invent id/name: %+v", normLater)
	}
	outLater := NormalizedToOpenAIStreamEvent(normLater, "trace-1", 123)
	if outLater.Choices[0].Delta.ToolCalls[0].Index != 0 {
		t.Errorf("Index lost on relay")
	}
}

// The upstream's terminal usage chunk carries role="assistant" too, which the
// translator mapped to message_start — emitting a second start AFTER the stop,
// which is invalid on the wire.
func TestOpenAIStreamNeverStartsAfterStop(t *testing.T) {
	// The finish chunk.
	finish := "stop"
	stopEvt := &OpenAIStreamEvent{
		Choices: []OpenAIStreamChoice{{Index: 0, FinishReason: &finish}},
	}
	stopNorm := OpenAIStreamEventToNormalized(stopEvt, false)
	if stopNorm.Type != "message_stop" {
		t.Fatalf("Type = %q, want message_stop", stopNorm.Type)
	}

	// The trailing chunk: role + usage, exactly as OpenRouter sends it.
	trailing := &OpenAIStreamEvent{
		Model: "openrouter/free",
		Choices: []OpenAIStreamChoice{{Index: 0, Delta: OpenAIStreamDelta{
			Role: "assistant", Content: "",
		}}},
		Usage: &types.OpenAIUsage{PromptTokens: 9223, CompletionTokens: 43, TotalTokens: 9266},
	}
	got := OpenAIStreamEventToNormalized(trailing, true)
	if got.Type == "message_start" {
		t.Fatalf("emitted message_start after message_stop — the trailing role/usage chunk was treated as a start")
	}
	// It is the usage carrier, so it must not be dropped either.
	if got.InputTokens != 9223 || got.OutputTokens != 43 {
		t.Errorf("usage lost: in=%d out=%d, want 9223/43", got.InputTokens, got.OutputTokens)
	}
}

// The terminal usage chunk arrives as a choice with an EMPTY delta plus a
// usage object — it does have a choices array, which is what made this easy to
// get wrong. Token counts were dropped because the empty delta won over the
// usage payload, so every streamed row recorded zero.
func TestOpenAIStreamCarriesUsageOnEmptyDeltaChunk(t *testing.T) {
	evt := &OpenAIStreamEvent{
		Model:   "openrouter/free",
		Choices: []OpenAIStreamChoice{{Index: 0, Delta: OpenAIStreamDelta{}}},
		Usage: &types.OpenAIUsage{
			PromptTokens:     9223,
			CompletionTokens: 43,
			TotalTokens:      9266,
			Cost:             0.0012,
			PromptDetails: map[string]interface{}{
				"cached_tokens":      float64(8000),
				"cache_write_tokens": float64(500),
			},
		},
	}
	norm := OpenAIStreamEventToNormalized(evt, false)
	if norm == nil {
		t.Fatal("usage chunk was dropped entirely")
	}
	if norm.Type != "usage" {
		t.Fatalf("Type = %q, want usage (an empty delta plus usage is the accounting event)", norm.Type)
	}
	if norm.InputTokens != 9223 || norm.OutputTokens != 43 {
		t.Errorf("tokens = %d/%d, want 9223/43", norm.InputTokens, norm.OutputTokens)
	}
	// The cache counters are the only evidence that affinity is working.
	if norm.CacheReadTokens != 8000 {
		t.Errorf("CacheReadTokens = %d, want 8000 (cached_tokens was not read)", norm.CacheReadTokens)
	}
	if norm.CacheWriteTokens != 500 {
		t.Errorf("CacheWriteTokens = %d, want 500", norm.CacheWriteTokens)
	}
	if norm.CostUSD != 0.0012 {
		t.Errorf("CostUSD = %v, want 0.0012", norm.CostUSD)
	}

	// Relayed to an OpenAI client in the shape the upstream uses: the choice is
	// PRESENT with an empty delta. Omitting `choices` entirely fails a strict
	// client's schema validation and aborts the whole stream.
	out := NormalizedToOpenAIStreamEvent(norm, "trace-1", 123)
	if out.Usage == nil || out.Usage.PromptTokens != 9223 {
		t.Errorf("outbound usage not relayed: %+v", out.Usage)
	}
	if len(out.Choices) != 1 {
		t.Fatalf("outbound usage chunk must carry one choice (empty delta), got %d", len(out.Choices))
	}
	if out.Choices[0].Delta.Content != "" || out.Choices[0].Delta.Role != "" {
		t.Errorf("the relayed choice must carry an empty delta, got %+v", out.Choices[0].Delta)
	}
}

// The relayed usage chunk must be schema-valid for a strict client: `choices`
// present with an empty delta, exactly as the upstream sends it. An earlier
// version emitted the chunk with no `choices` key at all, which opencode's
// zod union rejects ("expected array, received undefined") — aborting the
// stream and discarding the reply the user had already been shown.
func TestOpenAIUsageChunkKeepsChoicesPresent(t *testing.T) {
	norm := &types.NormalizedStreamEvent{
		Type: "usage", InputTokens: 9252, OutputTokens: 43, CostUSD: 0.001,
	}
	out := NormalizedToOpenAIStreamEvent(norm, "trace-1", 123)

	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := wire["choices"]; !ok {
		t.Fatalf("usage chunk omits `choices` entirely: %s", b)
	}
	if _, ok := wire["usage"]; !ok {
		t.Fatalf("usage chunk omits `usage`: %s", b)
	}

	var choices []map[string]interface{}
	if err := json.Unmarshal(wire["choices"], &choices); err != nil {
		t.Fatalf("choices is not an array: %v", err)
	}
	if len(choices) != 1 {
		t.Fatalf("choices length = %d, want 1", len(choices))
	}
	if _, ok := choices[0]["delta"]; !ok {
		t.Errorf("choice has no delta key: %+v", choices[0])
	}
}

// A chunk with no choices at all is also a usage carrier upstreams emit.
func TestOpenAIStreamCarriesChoicelessUsageChunk(t *testing.T) {
	evt := &OpenAIStreamEvent{
		Model: "openrouter/free",
		Usage: &types.OpenAIUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
	}
	norm := OpenAIStreamEventToNormalized(evt, false)
	if norm == nil || norm.Type != "usage" {
		t.Fatalf("choiceless usage chunk not carried: %+v", norm)
	}
	if norm.InputTokens != 10 || norm.OutputTokens != 2 {
		t.Errorf("tokens = %d/%d, want 10/2", norm.InputTokens, norm.OutputTokens)
	}
}

// Vendor reasoning is not in the OpenAI spec; it is relayed rather than dropped.
func TestOpenAIStreamRelaysReasoning(t *testing.T) {
	evt := &OpenAIStreamEvent{
		Model: "openrouter/free",
		Choices: []OpenAIStreamChoice{{Index: 0, Delta: OpenAIStreamDelta{
			ReasoningContent: "Let me think",
		}}},
	}
	norm := OpenAIStreamEventToNormalized(evt, false)
	if norm.DeltaType != "reasoning_delta" || norm.Reasoning != "Let me think" {
		t.Fatalf("reasoning not carried: type=%q text=%q", norm.DeltaType, norm.Reasoning)
	}
	out := NormalizedToOpenAIStreamEvent(norm, "trace-1", 123)
	if out.Choices[0].Delta.ReasoningContent != "Let me think" {
		t.Errorf("reasoning not relayed to the client: %+v", out.Choices[0].Delta)
	}
}

// A complete stream in the order a real upstream sends it: role, text, finish,
// then the usage chunk — the normalized sequence must be start, deltas, stop,
// usage, with no start after the stop.
func TestOpenAIStreamEventOrdering(t *testing.T) {
	finish := "stop"
	chunks := []*OpenAIStreamEvent{
		{Model: "m", Choices: []OpenAIStreamChoice{{Index: 0, Delta: OpenAIStreamDelta{Role: "assistant"}}}},
		{Model: "m", Choices: []OpenAIStreamChoice{{Index: 0, Delta: OpenAIStreamDelta{Content: "hi"}}}},
		{Model: "m", Choices: []OpenAIStreamChoice{{Index: 0, FinishReason: &finish}}},
		{Model: "m", Usage: &types.OpenAIUsage{PromptTokens: 10, CompletionTokens: 2}},
	}

	finished := false
	var seq []string
	for _, ch := range chunks {
		norm := OpenAIStreamEventToNormalized(ch, finished)
		if norm == nil {
			continue
		}
		if norm.Type == "message_stop" {
			finished = true
		}
		seq = append(seq, norm.Type)
	}

	want := []string{"message_start", "content_block_delta", "message_stop", "usage"}
	if len(seq) != len(want) {
		t.Fatalf("sequence = %v, want %v", seq, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("sequence = %v, want %v", seq, want)
		}
	}
}

// The Anthropic outbound path must not emit the normalized "usage" event name
// verbatim — an Anthropic client does not know it. It rides on message_delta.
func TestAnthropicOutboundMapsUsageEvent(t *testing.T) {
	norm := &types.NormalizedStreamEvent{
		Type: "usage", InputTokens: 100, OutputTokens: 20,
		CacheReadTokens: 80, CacheWriteTokens: 5,
	}
	out := NormalizedToAnthropicStreamEvent(norm)
	if out.Type != "message_delta" {
		t.Fatalf("Type = %q, want message_delta (Anthropic has no usage event)", out.Type)
	}
	if out.Usage == nil || out.Usage.InputTokens != 100 || out.Usage.OutputTokens != 20 {
		t.Fatalf("usage not relayed: %+v", out.Usage)
	}
	if out.Usage.CacheReadInputTokens != 80 || out.Usage.CacheCreationInputTokens != 5 {
		t.Errorf("cache counters not relayed: %+v", out.Usage)
	}
}

// Regression for #36: choices[].index means "which candidate completion"
// on the OpenAI wire, not "which content block". A reply whose first block
// is reasoning (block 0) followed by text (block 1) and a tool call (block
// 2) must still carry choices[0].index == 0 on every single chunk — Arbiter
// only ever produces one completion. Each block here uses a distinct
// BlockIndex/ToolCallIndex so a regression (stamping BlockIndex back onto
// the choice) would be caught.
func TestOpenAIStreamChoiceIndexAlwaysZeroAcrossBlocks(t *testing.T) {
	events := []*types.NormalizedStreamEvent{
		{Type: "message_start", MessageID: "msg_1", MessageModel: "m"},
		// Block 0: reasoning.
		{Type: "content_block_delta", DeltaType: "reasoning_delta", Reasoning: "thinking...", BlockIndex: 0},
		// Block 1: visible text.
		{Type: "content_block_delta", DeltaType: "text_delta", TextDelta: "Hello", BlockIndex: 1},
		// Block 2: a tool call, with its own (non-zero) ToolCallIndex.
		{Type: "content_block_start", BlockType: "tool_use", BlockIndex: 2, ToolCallIndex: 1, ToolCallID: "call_1", ToolCallName: "lookup"},
		{Type: "content_block_delta", DeltaType: "tool_use_delta", BlockIndex: 2, ToolCallIndex: 1, ToolCallArgs: "{}"},
		{Type: "message_stop", MessageStopReason: "end_turn"},
		{Type: "usage", InputTokens: 10, OutputTokens: 5},
	}

	for i, evt := range events {
		out := NormalizedToOpenAIStreamEvent(evt, "trace-1", 123)
		if out == nil {
			continue
		}
		if len(out.Choices) == 0 {
			t.Fatalf("event %d (%s): no choices emitted", i, evt.Type)
		}
		if out.Choices[0].Index != 0 {
			t.Errorf("event %d (%s): choices[0].Index = %d, want 0", i, evt.Type, out.Choices[0].Index)
		}
	}
}
