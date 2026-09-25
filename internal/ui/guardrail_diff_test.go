package ui

import (
	"strings"
	"testing"

	"github.com/cnf/arbiter/internal/store"
)

// A block with no guardrail touch renders no chip at all — the mechanism
// must not fire when the two variants are identical.
func TestTranscriptNoChipWhenNoGuardrailTouch(t *testing.T) {
	key := "untouched-session"
	h, _ := newSeededHandler(t, store.Event{
		TraceID: "t1", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1,
		SessionKey: key,
		Content: &store.CapturedContent{
			Request: []store.Block{{MsgIndex: 0, Position: 0, Role: "user", Kind: "text", Body: []byte("hi")}},
		},
	})
	body := serve(t, h, "GET", "/admin/ui/session?key="+key, false).Body.String()
	if strings.Contains(body, "modified by guardrail") {
		t.Error("a chip was rendered for a block with no guardrail touch")
	}
}
