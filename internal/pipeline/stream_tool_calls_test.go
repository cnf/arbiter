package pipeline

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/store"
	"github.com/cnf/arbiter/internal/upstream"
	"github.com/cnf/arbiter/pkg/types"
)

// TestStreamedToolCallIsRecorded pins #34: a streamed response carrying a
// tool call must leave a row with the same evidence the non-streaming path
// already produces — tool_calls_json populated and a captured tool_use
// block with the reassembled arguments — not a plausible-looking gap.
//
// Drives the real pipeline goroutine (sequenceUpstream + Execute), not a
// hand-built event list — a hand-built Event proves the storage shape works,
// not that the streaming accumulator actually reassembles fragments (see #15,
// #29 for this trap landing here before). The fragment shape (identity on
// content_block_start, then two argument fragments) mirrors Anthropic's real
// wire format, where ToolCallIndex equals BlockIndex.
func TestStreamedToolCallIsRecorded(t *testing.T) {
	u := &sequenceUpstream{events: []*types.NormalizedStreamEvent{
		{Type: "message_start", InputTokens: 2},
		{Type: "content_block_start", BlockIndex: 0, BlockType: "text"},
		{Type: "content_block_delta", BlockIndex: 0, DeltaType: "text_delta", TextDelta: "let me check that"},
		{
			Type: "content_block_start", BlockIndex: 1, BlockType: "tool_use",
			ToolCallIndex: 1, ToolCallID: "call_abc", ToolCallName: "read_file",
		},
		{
			Type: "content_block_delta", BlockIndex: 1, DeltaType: "tool_use_delta",
			ToolCallIndex: 1, ToolCallArgs: `{"path":`,
		},
		{
			Type: "content_block_delta", BlockIndex: 1, DeltaType: "tool_use_delta",
			ToolCallIndex: 1, ToolCallArgs: `"main.go"}`,
		},
		{Type: "message_delta", OutputTokens: 12, MessageStopReason: "tool_use"},
		{Type: "message_stop"},
	}}
	w := &capturingWriter{}
	p := NewPipeline(
		nil, streamingNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, u, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)
	p.SetCaptureContent(true)

	out, err := p.Execute(context.Background(), []byte("please read main.go"), "openai", "t1", "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sr, ok := out.(*upstream.StreamResponse)
	if !ok {
		t.Fatalf("stream Execute returned %T, want *upstream.StreamResponse", out)
	}
	for range sr.EventChan {
	}

	ev, ok := waitForEvent(w)
	if !ok {
		t.Fatal("streamed request was not recorded")
	}

	if len(ev.ToolCalls) != 1 || ev.ToolCalls[0] != "read_file" {
		t.Errorf("ToolCalls = %v, want [read_file] — the tool call must be visible in tool_calls_json like the non-streaming path", ev.ToolCalls)
	}

	if ev.Content == nil {
		t.Fatal("no content captured on the streaming path")
	}
	var toolBlock *store.Block
	for i := range ev.Content.Response {
		if ev.Content.Response[i].Kind == "tool_use" {
			toolBlock = &ev.Content.Response[i]
		}
	}
	if toolBlock == nil {
		t.Fatalf("response blocks = %+v, want a tool_use block", ev.Content.Response)
	}
	var decoded struct {
		ID    string                 `json:"id"`
		Name  string                 `json:"name"`
		Input map[string]interface{} `json:"input"`
	}
	if err := json.Unmarshal(toolBlock.Body, &decoded); err != nil {
		t.Fatalf("tool_use block body did not decode: %v (%s)", err, toolBlock.Body)
	}
	if decoded.ID != "call_abc" || decoded.Name != "read_file" {
		t.Errorf("captured tool call = %+v, want id=call_abc name=read_file", decoded)
	}
	if decoded.Input["path"] != "main.go" {
		t.Errorf("captured tool call input = %+v, want path=main.go (fragments reassembled)", decoded.Input)
	}

	// The text block from the same message must still be there — recording
	// a tool call must not come at the expense of the text half.
	var sawText bool
	for _, b := range ev.Content.Response {
		if b.Kind == "text" && string(b.Body) == "let me check that" {
			sawText = true
		}
	}
	if !sawText {
		t.Errorf("response blocks = %+v, want the text block alongside the tool call", ev.Content.Response)
	}
}
