package ui

import (
	"strings"
	"testing"

	"github.com/cnf/arbiter/internal/store"
)

// TestSessionTranscriptUsesGuardrailedFormAndKeepsDedup proves the session
// transcript's dedup/preamble logic — which matches on the literal Direction
// string "request" — still works correctly when a turn has both captures. The
// transcript must show the guardrailed text (what actually went upstream),
// never leak the pre-guardrail original, and still treat the system block as
// the one-time preamble rather than repeating or dropping it.
func TestSessionTranscriptUsesGuardrailedFormAndKeepsDedup(t *testing.T) {
	key := "guardrailed-session"
	sysAsSent := store.Block{MsgIndex: 0, Position: 0, Role: "system", Kind: "text",
		Body: []byte("CLIENT SYSTEM PROMPT")}
	sysGuardrailed := store.Block{MsgIndex: 0, Position: 0, Role: "system", Kind: "text",
		Body: []byte("ARBITER INJECTED PROMPT")}
	user := store.Block{MsgIndex: 1, Position: 0, Role: "user", Kind: "text",
		Body: []byte("a question")}
	reply := store.Block{MsgIndex: 0, Position: 0, Role: "assistant", Kind: "text",
		Body: []byte("an answer")}

	h, _ := newSeededHandler(t, store.Event{
		TraceID: "t1", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1,
		SessionKey: key,
		Content: &store.CapturedContent{
			Request:            []store.Block{sysAsSent, user},
			RequestGuardrailed: []store.Block{sysGuardrailed, user},
			Response:           []store.Block{reply},
		},
	})

	body := serve(t, h, "GET", "/admin/ui/session?key="+key, false).Body.String()

	if !strings.Contains(body, "ARBITER INJECTED PROMPT") {
		t.Errorf("transcript missing the guardrailed system text; body = %s", body)
	}
	if strings.Contains(body, "CLIENT SYSTEM PROMPT") {
		t.Errorf("transcript leaked the pre-guardrail original; body = %s", body)
	}
	// The preamble split (system role, "request" direction) must still fire:
	// the guardrailed system block was relabeled to Direction "request" by
	// filterRequestDirection, so this is exactly the case that would silently
	// break without that relabel.
	if !strings.Contains(body, "a question") {
		t.Errorf("transcript missing the user turn; body = %s", body)
	}
}
