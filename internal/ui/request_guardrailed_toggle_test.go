package ui

import (
	"strings"
	"testing"

	"github.com/cnf/arbiter/internal/store"
)

// TestRequestContentDefaultsToGuardrailedFormAndOffersToggle is #13's UI
// half: with both captures present, the default fragment shows the
// guardrailed text, hides the original, and offers a link to switch — the
// link's presence is exactly what tells an operator there IS an original to
// see, on requests where there isn't the link must not appear at all.
func TestRequestContentDefaultsToGuardrailedFormAndOffersToggle(t *testing.T) {
	h, _ := newSeededHandler(t, store.Event{
		TraceID: "t1", Provider: "p", Model: "m", LatencyMs: 1, StatusCode: 200,
		Content: &store.CapturedContent{
			Request:            []store.Block{{Role: "system", Kind: "text", Body: []byte("CLIENT SYSTEM PROMPT")}},
			RequestGuardrailed: []store.Block{{Role: "system", Kind: "text", Body: []byte("ARBITER INJECTED PROMPT")}},
		},
	})

	body := serve(t, h, "GET", "/admin/ui/requests/1/content", true).Body.String()
	if !strings.Contains(body, "ARBITER INJECTED PROMPT") {
		t.Errorf("default view missing the guardrailed text; body = %s", body)
	}
	if strings.Contains(body, "CLIENT SYSTEM PROMPT") {
		t.Errorf("default view leaked the pre-guardrail text; body = %s", body)
	}
	if !strings.Contains(body, "show as sent") {
		t.Errorf("toggle link to the as-sent view is missing; body = %s", body)
	}

	toggled := serve(t, h, "GET", "/admin/ui/requests/1/content?as_sent=1", true).Body.String()
	if !strings.Contains(toggled, "CLIENT SYSTEM PROMPT") {
		t.Errorf("as_sent=1 missing the client's original text; body = %s", toggled)
	}
	if strings.Contains(toggled, "ARBITER INJECTED PROMPT") {
		t.Errorf("as_sent=1 leaked the guardrailed text; body = %s", toggled)
	}
	if !strings.Contains(toggled, "show as guardrailed") {
		t.Errorf("toggle link back to the guardrailed view is missing; body = %s", toggled)
	}
}

// TestRequestContentNoToggleWhenNoPreGuardrailRan is the common-case guard on
// the UI side: with only one form ever captured, there is nothing to switch
// between, and the toggle link must not appear — showing it would imply a
// pre-guardrail ran when none did.
func TestRequestContentNoToggleWhenNoPreGuardrailRan(t *testing.T) {
	h, _ := newSeededHandler(t, store.Event{
		TraceID: "t1", Provider: "p", Model: "m", LatencyMs: 1, StatusCode: 200,
		Content: &store.CapturedContent{
			Request: []store.Block{{Role: "system", Kind: "text", Body: []byte("ONLY ONE FORM")}},
		},
	})

	body := serve(t, h, "GET", "/admin/ui/requests/1/content", true).Body.String()
	if !strings.Contains(body, "ONLY ONE FORM") {
		t.Errorf("captured content missing; body = %s", body)
	}
	if strings.Contains(body, "show as sent") || strings.Contains(body, "show as guardrailed") {
		t.Errorf("toggle link shown with no pre-guardrail variant to toggle to; body = %s", body)
	}
}
