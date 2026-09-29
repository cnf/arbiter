package store

import (
	"context"
	"testing"
)

// TestContentForRequestsMatchesTranscriptFiltering is #59's perf fix: the
// transcript page's batched read (ContentForRequests) pushes "only the
// newest resent message per direction, plus any role=system row" down into
// SQL, instead of fetching every resent copy of the conversation and
// filtering in Go (see contentFor's doc comment — that used to cost 26k rows
// / 53MB read for 315 actually-used rows on one real 100-turn page).
//
// This must NOT be compared against ContentForRequest: that call answers a
// different question (the full captured set, for the single-request detail
// view and lanePreview) and is deliberately unfiltered. What has to hold is
// that ContentForRequests' result matches what the transcript page actually
// displays — i.e. applying the same "newest message, or system" rule in Go
// to the unfiltered set produces an identical result to what the batched SQL
// version returns directly.
func TestContentForRequestsMatchesTranscriptFiltering(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	// t1 simulates a 3-turn resent conversation landing on one request row:
	// msg_index 0 is the system prompt (must survive despite not being
	// newest), 1 is an earlier turn's user message (resent, must be
	// dropped), 2 is this turn's own new user message (must survive as the
	// newest). The response side is this request's own reply.
	w.Record(Event{
		TraceID: "t1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{
			Request: []Block{
				textBlock("system", 0, 0, "SYSTEM PROMPT"),
				textBlock("user", 1, 0, "earlier turn's message, resent"),
				textBlock("user", 2, 0, "this turn's own new message"),
			},
			Response: []Block{textBlock("assistant", 0, 0, "the reply")},
		},
	})
	// t2: guardrailed system prompt plus a single (non-resent) user message,
	// to prove the role=system carve-out also applies to the guardrailed
	// direction and coexists with hasGuardrailed.
	w.Record(Event{
		TraceID: "t2", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{
			Request:            []Block{textBlock("system", 0, 0, "CLIENT SYSTEM PROMPT")},
			RequestGuardrailed: []Block{textBlock("system", 0, 0, "ARBITER INJECTED PROMPT")},
		},
	})
	w.Record(Event{
		TraceID: "t3", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		// No captured content at all — the "capture off" case.
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows, err := r.ListRequests(ctx, RequestFilter{})
	if err != nil || len(rows) != 3 {
		t.Fatalf("ListRequests = %v, %v", rows, err)
	}
	ids := make([]int64, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}

	batchBlocks, batchGuardrailed, err := r.ContentForRequests(ctx, ids)
	if err != nil {
		t.Fatalf("ContentForRequests: %v", err)
	}

	for _, id := range ids {
		fullBlocks, wantGuardrailed, err := r.ContentForRequest(ctx, id, false)
		if err != nil {
			t.Fatalf("ContentForRequest(%d): %v", id, err)
		}
		if batchGuardrailed[id] != wantGuardrailed {
			t.Errorf("id %d: hasGuardrailed = %v, want %v", id, batchGuardrailed[id], wantGuardrailed)
		}
		wantBlocks := transcriptFilterForTest(fullBlocks)
		gotBlocks := batchBlocks[id]
		if len(gotBlocks) != len(wantBlocks) {
			t.Fatalf("id %d: blocks = %+v, want (transcript-filtered) %+v", id, gotBlocks, wantBlocks)
		}
		for i := range wantBlocks {
			if gotBlocks[i].Body != wantBlocks[i].Body || gotBlocks[i].Direction != wantBlocks[i].Direction ||
				gotBlocks[i].MsgIndex != wantBlocks[i].MsgIndex {
				t.Errorf("id %d block %d: got %+v, want %+v", id, i, gotBlocks[i], wantBlocks[i])
			}
		}
	}
}

// transcriptFilterForTest mirrors newestRequestMessage's rule
// (internal/ui/transcript.go) plus the role=system carve-out
// ContentForRequests applies in SQL: keep every response-direction block,
// every system-role request block regardless of index, and only the
// highest-msg_index non-system request block(s). It exists here, rather than
// importing internal/ui, to keep this a store-level test of the SQL's
// output shape without a package-layering dependency on the UI.
func transcriptFilterForTest(blocks []ContentBlock) []ContentBlock {
	newest := int64(-1)
	for _, b := range blocks {
		if b.Direction == "request" && b.Role != "system" && b.MsgIndex > newest {
			newest = b.MsgIndex
		}
	}
	out := make([]ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		if b.Direction == "request" && b.Role != "system" && b.MsgIndex != newest {
			continue
		}
		out = append(out, b)
	}
	return out
}

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
