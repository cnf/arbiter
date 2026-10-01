package translator

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

// A Claude reply relayed to an OpenAI-format client, event for event.
//
// The upstream sends thinking and tool-call content under Anthropic's own
// delta vocabulary (thinking_delta, signature_delta, input_json_delta) and
// announces a tool call's identity on content_block_start. Before this was
// fixed the inbound parser read only delta.text, so thinking and arguments
// both reached the outbound switch as empty deltas, and the outbound switch had
// no arm for the Anthropic names at all — the client got a 200 stream of
// chunks carrying nothing, which is the "empty reply" the bug report describes.
//
// The sequence is written as the raw SSE payloads a real capture shows, in
// order, including the signature_delta that follows every thinking block.
// Parsing them through the same JSON decoder the upstream reader uses is the
// point: a hand-built struct would encode this test's assumption about the
// wire shape rather than the wire shape itself.
func TestClaudeThinkingAndToolCallsSurviveRelayToOpenAI(t *testing.T) {
	upstream := []string{
		`{"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"usage":{"input_tokens":120,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"The user wants a proxy. "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"I should check the config first."}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EqQBCgIYAhIM1gbcDa9GJwZA2b3hGgxBdjrkzLoky3dl1pki"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Quick check before I start:"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_01A","name":"read_file","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"main.go\"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"type":"message_delta","stop_reason":"tool_use"},"usage":{"output_tokens":391}}`,
		`{"type":"message_stop"}`,
	}

	var reasoning, text, args string
	var sawName, sawID bool
	var stops int

	for i, raw := range upstream {
		var evt AnthropicStreamEvent
		if err := json.Unmarshal([]byte(raw), &evt); err != nil {
			t.Fatalf("event %d is not valid upstream JSON: %v\n%s", i, err, raw)
		}

		norm := AnthropicStreamEventToNormalized(&evt)
		if norm == nil {
			// Only a deliberately-dropped event may vanish here.
			if evt.Delta == nil || evt.Delta.Type != "signature_delta" {
				t.Errorf("event %d (%s) vanished, and it is not the signature drop", i, evt.Type)
			}
			continue
		}

		out := NormalizedToOpenAIStreamEvent(norm, "trace-1", 42)
		if out == nil {
			// A dropped event is only acceptable when the upstream event
			// itself carried no content. Anything that carried content and
			// produced no chunk is content silently lost.
			if evt.Delta != nil && evt.Delta.Type == "signature_delta" {
				continue // the deliberate drop, asserted separately below
			}
			if upstreamEventCarriesContent(&evt) {
				t.Fatalf("event %d (%s) carried content but produced no outbound chunk",
					i, evt.Type)
			}
			continue
		}

		for _, ch := range out.Choices {
			if ch.Delta.ReasoningContent != "" {
				reasoning += ch.Delta.ReasoningContent
			}
			if ch.Delta.Content != "" {
				text += ch.Delta.Content
			}
			for _, tc := range ch.Delta.ToolCalls {
				if tc.Function.Name != "" {
					sawName = true
				}
				if tc.ID != "" {
					sawID = true
				}
				args += tc.Function.Arguments
			}
			if ch.FinishReason != nil {
				stops++
			}
		}

		// The invariant this test exists for: an event that carried content
		// upstream must not reach the client as a chunk carrying nothing. A
		// chunk whose delta is empty and which is not a lifecycle event is
		// exactly the empty-reply signature.
		d := out.Choices[0].Delta
		carries := d.Content != "" || d.ReasoningContent != "" || len(d.ToolCalls) > 0 ||
			d.Role != "" || out.Choices[0].FinishReason != nil || out.Usage != nil
		if !carries {
			t.Errorf("event %d (%s) reached the client as an empty chunk: %s",
				i, evt.Type, mustJSON(t, out))
		}
	}

	if reasoning != "The user wants a proxy. I should check the config first." {
		t.Errorf("thinking trace = %q, want the two thinking_delta texts concatenated", reasoning)
	}
	if text != "Quick check before I start:" {
		t.Errorf("text = %q, want the text_delta verbatim", text)
	}
	if !sawID {
		t.Error("tool call id never reached the client — it is only on content_block_start")
	}
	if !sawName {
		t.Error("tool call name never reached the client — it is only on content_block_start")
	}
	if args != `{"path":"main.go"}` {
		t.Errorf("tool arguments = %q, want the fragments concatenated by the client", args)
	}
	if stops != 1 {
		t.Errorf("finish_reason emitted %d times, want exactly 1", stops)
	}
}

// The mirror case: an OpenAI-format upstream's tool call must still reach an
// Anthropic-format client, now carrying the id and name that the block start
// announces. Before this, the outbound content_block_start dropped both, so an
// Anthropic client received argument fragments for a tool it could not name.
func TestToolCallIdentityReachesAnthropicClient(t *testing.T) {
	first := &OpenAIStreamEvent{
		ID: "gen-1", Model: "openrouter/free",
		Choices: []OpenAIStreamChoice{{Index: 0, Delta: OpenAIStreamDelta{
			ToolCalls: []OpenAIStreamToolCall{{
				Index: 0, ID: "call_abc", Type: "function",
				Function: OpenAIStreamToolCallFunc{Name: "read_file"},
			}},
		}}},
	}
	norm := OpenAIStreamEventToNormalized(first, false)
	if norm.ToolCallName != "read_file" || norm.ToolCallID != "call_abc" {
		t.Fatalf("inbound dropped identity: %+v", norm)
	}

	// The block start is emitted from a normalized content_block_start — the
	// event a vendor-tagged stream produces when it opens a tool_use block.
	start := NormalizedToAnthropicStreamEvent(&types.NormalizedStreamEvent{
		Type: "content_block_start", BlockType: "tool_use", BlockIndex: 1,
		ToolCallIndex: 1, ToolCallID: "call_abc", ToolCallName: "read_file",
	})
	if start.ContentBlock == nil || start.ContentBlock.ID != "call_abc" ||
		start.ContentBlock.Name != "read_file" {
		t.Fatalf("content_block_start = %+v, want id and name present", start.ContentBlock)
	}

	// And the arguments still arrive as input_json_delta fragments.
	args := NormalizedToAnthropicStreamEvent(&types.NormalizedStreamEvent{
		Type: "content_block_delta", DeltaType: "tool_use_delta",
		ToolCallIndex: 1, ToolCallArgs: `{"path":"main.go"}`,
	})
	if args.Delta == nil || args.Delta.Type != "input_json_delta" ||
		args.Delta.PartialJSON != `{"path":"main.go"}` {
		t.Fatalf("arguments = %+v, want an input_json_delta fragment", args.Delta)
	}
}

// A signature_delta is carried, and dropped only on the OpenAI wire where it
// has no meaning. The distinction matters: an Anthropic client must replay the
// signature with its thinking block on the next request, so dropping it on the
// anthropic-to-anthropic path breaks the conversation — while relaying it to an
// OpenAI client would put a field on the wire that client cannot use.
//
// Either way it must never become an EMPTY chunk, which is what falling through
// the switch used to produce.
func TestSignatureDeltaIsCarriedThenDroppedPerWireFormat(t *testing.T) {
	const sig = "EqQBCgIYAhIM"
	var evt AnthropicStreamEvent
	raw := `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + sig + `"}}`
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	norm := AnthropicStreamEventToNormalized(&evt)
	if norm == nil {
		t.Fatal("signature_delta was dropped inbound; an Anthropic client then cannot replay its thinking block")
	}
	if norm.Signature != sig {
		t.Errorf("normalized Signature = %q, want %q", norm.Signature, sig)
	}

	// Anthropic wire: re-emitted with the signature intact.
	back := NormalizedToAnthropicStreamEvent(norm)
	if back.Delta == nil || back.Delta.Type != "signature_delta" || back.Delta.Signature != sig {
		t.Errorf("anthropic outbound = %+v, want the signature preserved", back.Delta)
	}

	// OpenAI wire: no representation, so dropped rather than emptied.
	if out := NormalizedToOpenAIStreamEvent(norm, "t", 1); out != nil {
		d := out.Choices[0].Delta
		if d.Content == "" && d.ReasoningContent == "" && len(d.ToolCalls) == 0 {
			t.Errorf("openai outbound emitted an empty chunk: %+v", out)
		}
	}
}

// Every content-bearing delta type the Anthropic wire documents must have an
// explicit arm that puts its payload on the client's wire. A type that falls
// through to the default produces a well-formed chunk carrying nothing, which
// a strict client accepts — the stream ends "successfully" and the empty reply
// gets blamed on the model rather than on the translator.
func TestEveryContentBearingAnthropicDeltaReachesTheClient(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		check func(*OpenAIStreamEvent) bool
	}{
		{
			name: "text_delta",
			raw:  `{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"hi"}}`,
			check: func(o *OpenAIStreamEvent) bool {
				return o.Choices[0].Delta.Content == "hi"
			},
		},
		{
			name: "thinking_delta",
			raw:  `{"type":"content_block_delta","index":3,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
			check: func(o *OpenAIStreamEvent) bool {
				return o.Choices[0].Delta.ReasoningContent == "hmm"
			},
		},
		{
			name: "input_json_delta",
			raw:  `{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"a\":1}"}}`,
			check: func(o *OpenAIStreamEvent) bool {
				return len(o.Choices[0].Delta.ToolCalls) == 1 &&
					o.Choices[0].Delta.ToolCalls[0].Function.Arguments == `{"a":1}`
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var evt AnthropicStreamEvent
			if err := json.Unmarshal([]byte(tc.raw), &evt); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			norm := AnthropicStreamEventToNormalized(&evt)
			if norm == nil {
				t.Fatalf("%s: dropped, want relayed", tc.name)
			}
			out := NormalizedToOpenAIStreamEvent(norm, "t", 1)
			d := out.Choices[0].Delta
			if d.Content == "" && d.ReasoningContent == "" && len(d.ToolCalls) == 0 {
				t.Fatalf("%s: relayed as an empty chunk — the client sees nothing", tc.name)
			}
			if !tc.check(out) {
				t.Fatalf("%s: chunk = %s", tc.name, mustJSON(t, out))
			}
		})
	}
}

func mustJSON(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// upstreamEventCarriesContent reports whether an Anthropic SSE event put any
// content on the wire. A block whose opening chunk is empty carries none, so a
// translator is free to drop it; a block opening with text, a tool call's
// identity, or any delta payload does not get that licence.
func upstreamEventCarriesContent(evt *AnthropicStreamEvent) bool {
	if evt.ContentBlock != nil {
		if evt.ContentBlock.Type == "tool_use" {
			return true // id and name are content the client needs
		}
		if evt.ContentBlock.Text != "" {
			return true
		}
	}
	if evt.Delta != nil {
		if evt.Delta.Text != "" || evt.Delta.Thinking != "" || evt.Delta.PartialJSON != "" {
			return true
		}
	}
	if evt.Message != nil {
		// message_start / message_stop carry lifecycle, not content.
		return false
	}
	return evt.Usage != nil
}

// The streaming mirror of TestNoArgToolUseEmitsEmptyInputObjectOnWire: a
// no-argument tool call's content_block_start must open with `"input":{}`
// on the Anthropic client wire, not a missing key or `"input":null`. A real
// Anthropic stream always opens a tool_use block this way, before any
// input_json_delta fragments arrive — a client (or Arbiter itself, replaying
// the reassembled block later) that saw the key missing here would hit the
// same issue #78 400 on the next turn.
func TestContentBlockStartForNoArgToolUseEmitsEmptyInputObjectOnWire(t *testing.T) {
	start := NormalizedToAnthropicStreamEvent(&types.NormalizedStreamEvent{
		Type: "content_block_start", BlockType: "tool_use", BlockIndex: 0,
		ToolCallIndex: 0, ToolCallID: "toolu_01", ToolCallName: "kanban_show",
	})

	raw, err := json.Marshal(start)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"input":{}`)) {
		t.Fatalf("content_block_start wire JSON = %s, want it to contain the literal %q", raw, `"input":{}`)
	}
}
