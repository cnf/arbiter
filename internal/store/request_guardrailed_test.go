package store

import (
	"context"
	"testing"
)

// TestContentForRequestDefaultsToGuardrailedForm is #13's core promise: when a
// pre-guardrail actually ran and both captures were written, the default view
// (showAsSent=false) returns what went upstream, not what the client sent.
func TestContentForRequestDefaultsToGuardrailedForm(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	w.Record(Event{
		TraceID: "t1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{
			Request:            []Block{textBlock("system", 0, 0, "CLIENT SYSTEM PROMPT")},
			RequestGuardrailed: []Block{textBlock("system", 0, 0, "ARBITER INJECTED PROMPT")},
		},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows, err := r.ListRequests(ctx, RequestFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListRequests = %v, %v", rows, err)
	}

	blocks, hasGuardrailed, err := r.ContentForRequest(ctx, rows[0].ID, false)
	if err != nil {
		t.Fatalf("ContentForRequest: %v", err)
	}
	if !hasGuardrailed {
		t.Fatal("hasGuardrailedVariant = false, want true")
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %+v, want exactly 1 (the guardrailed form only)", blocks)
	}
	if got := string(blocks[0].Body); got != "ARBITER INJECTED PROMPT" {
		t.Errorf("default view body = %q, want the guardrailed form", got)
	}
	// The kept row must still say "request", not "request_guardrailed" —
	// every downstream consumer (dedup, preamble-splitting, templates)
	// matches the literal string "request".
	if blocks[0].Direction != "request" {
		t.Errorf("direction = %q, want relabeled to \"request\"", blocks[0].Direction)
	}
}

// TestContentForRequestShowAsSentReturnsOriginal is the toggle's other side:
// showAsSent=true must return the client's original text, not the guardrailed
// rewrite, when both were captured.
func TestContentForRequestShowAsSentReturnsOriginal(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	w.Record(Event{
		TraceID: "t1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{
			Request:            []Block{textBlock("system", 0, 0, "CLIENT SYSTEM PROMPT")},
			RequestGuardrailed: []Block{textBlock("system", 0, 0, "ARBITER INJECTED PROMPT")},
		},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows, err := r.ListRequests(ctx, RequestFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListRequests = %v, %v", rows, err)
	}

	blocks, hasGuardrailed, err := r.ContentForRequest(ctx, rows[0].ID, true)
	if err != nil {
		t.Fatalf("ContentForRequest: %v", err)
	}
	if !hasGuardrailed {
		t.Fatal("hasGuardrailedVariant = false, want true")
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %+v, want exactly 1 (the as-sent form only)", blocks)
	}
	if got := string(blocks[0].Body); got != "CLIENT SYSTEM PROMPT" {
		t.Errorf("as-sent view body = %q, want the client's original", got)
	}
	if blocks[0].Direction != "request" {
		t.Errorf("direction = %q, want relabeled to \"request\"", blocks[0].Direction)
	}
}

// TestContentForRequestNoGuardrailedVariant covers the common case: no
// pre-guardrail ran, so only "request" was ever written. Both showAsSent
// values must return that single form, and hasGuardrailedVariant must be
// false so the UI knows not to offer a toggle with nothing behind it.
func TestContentForRequestNoGuardrailedVariant(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	w.Record(Event{
		TraceID: "t1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{
			Request: []Block{textBlock("system", 0, 0, "ONLY ONE FORM")},
		},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows, err := r.ListRequests(ctx, RequestFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListRequests = %v, %v", rows, err)
	}

	for _, showAsSent := range []bool{false, true} {
		blocks, hasGuardrailed, err := r.ContentForRequest(ctx, rows[0].ID, showAsSent)
		if err != nil {
			t.Fatalf("ContentForRequest(showAsSent=%v): %v", showAsSent, err)
		}
		if hasGuardrailed {
			t.Errorf("showAsSent=%v: hasGuardrailedVariant = true, want false (no pre-guardrail ran)", showAsSent)
		}
		if len(blocks) != 1 || string(blocks[0].Body) != "ONLY ONE FORM" {
			t.Errorf("showAsSent=%v: blocks = %+v, want the single captured form", showAsSent, blocks)
		}
	}
}

// TestContentForRequestGuardrailedFormExcludesResponse proves the response
// direction is untouched by the request-side filtering: it must appear in
// both showAsSent views, unfiltered, when a pre-guardrail also ran.
func TestContentForRequestGuardrailedFormExcludesResponse(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	w.Record(Event{
		TraceID: "t1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{
			Request:            []Block{textBlock("system", 0, 0, "as sent")},
			RequestGuardrailed: []Block{textBlock("system", 0, 0, "as guardrailed")},
			Response:           []Block{textBlock("assistant", 0, 0, "the reply")},
		},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows, err := r.ListRequests(ctx, RequestFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListRequests = %v, %v", rows, err)
	}

	for _, showAsSent := range []bool{false, true} {
		blocks, _, err := r.ContentForRequest(ctx, rows[0].ID, showAsSent)
		if err != nil {
			t.Fatalf("ContentForRequest(showAsSent=%v): %v", showAsSent, err)
		}
		var sawResponse bool
		for _, b := range blocks {
			if b.Direction == "response" {
				sawResponse = true
				if string(b.Body) != "the reply" {
					t.Errorf("response body = %q, want %q", b.Body, "the reply")
				}
			}
		}
		if !sawResponse {
			t.Errorf("showAsSent=%v: response block missing from %+v", showAsSent, blocks)
		}
		if len(blocks) != 2 {
			t.Errorf("showAsSent=%v: blocks = %+v, want exactly 2 (one request-side + the response)", showAsSent, blocks)
		}
	}
}
