package ui

import (
	"strings"
	"testing"

	"github.com/cnf/arbiter/internal/store"
)

// Tool call/result blocks in a transcript must render content-first — the
// summarized command, not the raw {id,name,input} envelope — per DESIGN.md,
// with the envelope available only behind a "raw JSON" toggle.
func TestTranscriptRendersToolCallsContentFirst(t *testing.T) {
	key := "tool-session"
	toolUse := store.Block{MsgIndex: 0, Position: 1, Role: "assistant", Kind: "tool_use",
		Body: []byte(`{"id":"tu_1","name":"terminal","input":{"command":"go test ./..."}}`)}
	toolResult := store.Block{MsgIndex: 0, Position: 2, Role: "user", Kind: "tool_result",
		Body: []byte(`{"for_id":"tu_1","content":"ok\n","is_error":false}`)}

	h, _ := newSeededHandler(t, store.Event{
		TraceID: "t1", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1,
		SessionKey: key,
		Content: &store.CapturedContent{
			Response: []store.Block{toolUse, toolResult},
		},
	})

	body := serve(t, h, "GET", "/admin/ui/session?key="+key, false).Body.String()

	if !strings.Contains(body, "go test ./...") {
		t.Errorf("the summarized command is not rendered; body = %s", firstLine(body))
	}
	// The raw envelope is not inlined by default — it sits behind a toggle.
	if strings.Contains(body, `&#34;id&#34;:&#34;tu_1&#34;`) || strings.Contains(body, `"id":"tu_1"`) {
		t.Error("the raw {id,name,input} envelope is inlined by default, not content-first")
	}
	if !strings.Contains(body, "raw JSON") {
		t.Error("no raw-JSON toggle is offered for the tool call")
	}
	if !strings.Contains(body, "tag tool") {
		t.Error("the tool call/result is not tagged with the tool color class")
	}
}
