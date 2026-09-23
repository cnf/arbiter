package ui

import (
	"strings"
	"testing"

	"github.com/cnf/arbiter/internal/store"
)

// A block whose guardrailed variant differs from what the client sent must
// render collapsed behind a "modified by guardrail" warn chip in the
// transcript, not an inline diff — the diff itself is fetched on demand from
// GuardrailDiffHandler when the chip's popover opens.
func TestTranscriptShowsGuardrailTouchChipNotInlineDiff(t *testing.T) {
	key := "diffed-session"
	sysAsSent := store.Block{MsgIndex: 0, Position: 0, Role: "system", Kind: "text",
		Body: []byte("client system prompt line one\nline two")}
	sysGuardrailed := store.Block{MsgIndex: 0, Position: 0, Role: "system", Kind: "text",
		Body: []byte("arbiter system prompt line one\nline two")}
	user := store.Block{MsgIndex: 1, Position: 0, Role: "user", Kind: "text", Body: []byte("hi")}

	h, _ := newSeededHandler(t, store.Event{
		TraceID: "t1", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1,
		SessionKey: key,
		Content: &store.CapturedContent{
			Request:            []store.Block{sysAsSent, user},
			RequestGuardrailed: []store.Block{sysGuardrailed, user},
		},
	})

	body := serve(t, h, "GET", "/admin/ui/session?key="+key, false).Body.String()

	if !strings.Contains(body, "modified by guardrail") {
		t.Errorf("transcript missing the guardrail-touch chip; body = %s", firstLine(body))
	}
	// Collapsed by default: the diff's own text must not be inline.
	if strings.Contains(body, "client system prompt") {
		t.Error("the pre-guardrail original leaked into the transcript instead of staying behind the chip")
	}
	if !strings.Contains(body, "/guardrail-diff?msg=0&amp;pos=0") {
		t.Errorf("the chip's popover source does not target the guardrail-diff endpoint; body = %s", firstLine(body))
	}

	// Fetching the diff on demand returns the line-level diff, del before add.
	diff := serve(t, h, "GET", "/admin/ui/requests/1/guardrail-diff?msg=0&pos=0", true).Body.String()
	if !strings.Contains(diff, "diff-del") || !strings.Contains(diff, "diff-add") {
		t.Errorf("guardrail-diff fragment does not render del/add ops; body = %s", diff)
	}
	if !strings.Contains(diff, "diff-eq") {
		t.Error("guardrail-diff fragment does not render the unchanged context line")
	}
}

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
