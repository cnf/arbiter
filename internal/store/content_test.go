package store

import (
	"context"
	"testing"
	"time"

	"github.com/cnf/arbiter/pkg/types"
)

// captureFixture opens a real store and returns a writer plus a reader over the
// same file, so these tests exercise the same transaction path the pipeline
// uses rather than a hand-rolled insert.
func captureFixture(t *testing.T) (*SQLiteWriter, *Reader) {
	t.Helper()
	w, path := newTestWriter(t)
	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return w, r
}

func textBlock(role string, msgIndex, position int, text string) Block {
	return Block{Kind: "text", Body: []byte(text), Role: role, MsgIndex: msgIndex, Position: position}
}

// TestContentIsDeduplicatedAcrossRequests is the property the whole design
// exists for: a conversation re-sends its earlier turns on every request, and
// the same block must be stored once no matter how many requests contain it.
func TestContentIsDeduplicatedAcrossRequests(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	// Three turns of one conversation, each re-sending the previous content —
	// the shape of every real multi-turn chat.
	sys := textBlock("system", 0, 0, "You are a helpful assistant.")
	greeting := textBlock("user", 1, 0, "hello there, how are you today?")

	for i := 0; i < 3; i++ {
		blocks := []Block{sys, greeting}
		for j := 0; j <= i; j++ {
			blocks = append(blocks, textBlock("user", 2+j, 0, "unique turn content "+string(rune('a'+j))+" padding"))
		}
		w.Record(Event{
			TraceID: "t", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
			Content: &CapturedContent{Request: blocks},
		})
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var bodies, refs int64
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM content`).Scan(&bodies); err != nil {
		t.Fatalf("count content: %v", err)
	}
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM content_refs`).Scan(&refs); err != nil {
		t.Fatalf("count refs: %v", err)
	}

	// 2 shared blocks + 3 unique turns + 2 repeats of an earlier turn's text
	// (turn 2 and 3 re-send turn 1's unique text verbatim) = 5 distinct bodies.
	if bodies != 5 {
		t.Errorf("content rows = %d, want 5 (deduplicated)", bodies)
	}
	// References, by contrast, grow with the conversation: 3*(sys+greeting) +
	// 1+2+3 unique = 12.
	if refs != 12 {
		t.Errorf("content_refs rows = %d, want 12 (one per block per request)", refs)
	}

	// The shared system prompt appears once but is referenced by all three.
	var uses int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT count(*) FROM content_refs WHERE hash = ?`, sys.Hash()).Scan(&uses); err != nil {
		t.Fatalf("count system refs: %v", err)
	}
	if uses != 3 {
		t.Errorf("system prompt referenced %d times, want 3", uses)
	}
}

// TestContentIsReassembledInOrder proves the reference rows carry enough to
// rebuild the conversation, which is what the detail view needs.
func TestContentIsReassembledInOrder(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	w.Record(Event{
		TraceID: "t", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{
			Request: []Block{
				textBlock("system", 0, 0, "system text"),
				textBlock("user", 1, 0, "first user message"),
				textBlock("assistant", 2, 0, "assistant reply"),
			},
			Response: []Block{textBlock("assistant", 0, 0, "the response body")},
		},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows, err := r.ListRequests(ctx, RequestFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListRequests = %v, %v", rows, err)
	}

	blocks, err := r.ContentForRequest(ctx, rows[0].ID)
	if err != nil {
		t.Fatalf("ContentForRequest: %v", err)
	}
	// Request direction first (the query orders direction DESC so "request"
	// sorts before "response"), then by message index.
	want := []struct{ body, role, direction string }{
		{"system text", "system", "request"},
		{"first user message", "user", "request"},
		{"assistant reply", "assistant", "request"},
		{"the response body", "assistant", "response"},
	}
	if len(blocks) != len(want) {
		t.Fatalf("got %d blocks, want %d: %+v", len(blocks), len(want), blocks)
	}
	for i, exp := range want {
		if blocks[i].Body != exp.body {
			t.Errorf("block %d body = %q, want %q", i, blocks[i].Body, exp.body)
		}
		if blocks[i].Role != exp.role {
			t.Errorf("block %d role = %q, want %q", i, blocks[i].Role, exp.role)
		}
		if blocks[i].Direction != exp.direction {
			t.Errorf("block %d direction = %q, want %q", i, blocks[i].Direction, exp.direction)
		}
		if !blocks[i].Captured {
			t.Errorf("block %d reported as not captured", i)
		}
	}
}

// TestRejectedContentIsStoredWithoutARequestRow proves the (b)-now shape: a
// request Arbiter never recorded still gets its content stored, under
// owner_kind="rejected", so "why was this refused" has something to look at.
func TestRejectedContentIsStoredWithoutARequestRow(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	w.RecordRejected(42, CapturedContent{
		Request: []Block{textBlock("user", 0, 0, "a request that got rejected")},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// No request row was written...
	var requests int64
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM requests`).Scan(&requests); err != nil {
		t.Fatalf("count requests: %v", err)
	}
	if requests != 0 {
		t.Errorf("requests rows = %d, want 0 for a rejection", requests)
	}

	// ...but the content and its reference are there, addressed as 'rejected'.
	var kind string
	if err := r.db.QueryRowContext(ctx,
		`SELECT owner_kind FROM content_refs WHERE owner_id = 42`).Scan(&kind); err != nil {
		t.Fatalf("read rejected ref: %v", err)
	}
	if kind != "rejected" {
		t.Errorf("owner_kind = %q, want rejected", kind)
	}

	blocks, err := r.contentFor(ctx, "rejected", 42)
	if err != nil {
		t.Fatalf("contentFor: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Body != "a request that got rejected" {
		t.Errorf("rejected content = %+v, want the one block back", blocks)
	}
}

// TestRejectedContentIsInvisibleToRequestQueries is the other half of the (b)
// contract: storing rejections must not leak into anything that reads requests,
// or every aggregate would silently include traffic that never succeeded.
func TestRejectedContentIsInvisibleToRequestQueries(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	w.RecordRejected(7, CapturedContent{
		Request: []Block{textBlock("user", 0, 0, "rejected content that must not leak")},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The "repeated content" query joins requests, so a rejection must not
	// appear in it no matter how often the same text is rejected.
	repeated, err := r.RepeatedContent(ctx, WindowFrom(time.Hour), 1, 0, 50)
	if err != nil {
		t.Fatalf("RepeatedContent: %v", err)
	}
	if len(repeated) != 0 {
		t.Errorf("repeated content = %+v, want none (rejections join no requests)", repeated)
	}

	overall, err := r.Overall(ctx, WindowFrom(time.Hour))
	if err != nil {
		t.Fatalf("Overall: %v", err)
	}
	if overall.Requests != 0 {
		t.Errorf("Overall.Requests = %d, want 0", overall.Requests)
	}
}

// TestRepeatedContentFindsCrossSessionBoilerplate is the payoff query: the text
// a client prepends to every request shows up as a block seen across many
// sessions, which is how an injected prompt is found without knowing it in
// advance.
func TestRepeatedContentFindsCrossSessionBoilerplate(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	boilerplate := textBlock("system", 0, 0, "You are an AI assistant. Always respond in Markdown.")

	// Three different sessions, each with its own user text but the same
	// client-injected preamble.
	for i, session := range []string{"session-a", "session-b", "session-c"} {
		w.Record(Event{
			TraceID: "t", SessionKey: session, Format: "openai", Provider: "p", Model: "m",
			StatusCode: 200,
			Content: &CapturedContent{Request: []Block{
				boilerplate,
				textBlock("user", 1, 0, "unique question number "+string(rune('a'+i))+" with enough text"),
			}},
		})
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	repeated, err := r.RepeatedContent(ctx, WindowFrom(time.Hour), 2, 0, 50)
	if err != nil {
		t.Fatalf("RepeatedContent: %v", err)
	}
	if len(repeated) != 1 {
		t.Fatalf("got %d repeated blocks, want just the boilerplate: %+v", len(repeated), repeated)
	}
	got := repeated[0]
	if got.Requests != 3 || got.Sessions != 3 {
		t.Errorf("reach = %d requests / %d sessions, want 3/3", got.Requests, got.Sessions)
	}
	if got.Role != "system" || got.BlockType != "text" {
		t.Errorf("role/type = %q/%q, want system/text", got.Role, got.BlockType)
	}
	if got.Preview == "" {
		t.Error("preview is empty; the UI needs something recognisable to show")
	}

	// min_sessions is what separates "this client always sends X" from "one
	// long conversation": requiring more sessions than exist must exclude it.
	none, err := r.RepeatedContent(ctx, WindowFrom(time.Hour), 2, 5, 50)
	if err != nil {
		t.Fatalf("RepeatedContent(min_sessions): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("min_sessions=5 returned %+v, want none (only 3 sessions exist)", none)
	}
}

// TestSweepRemovesExpiredContentAndThenOrphans covers the two-step GC: expire
// by request age, then drop whatever that orphaned. It runs with a zero TTL
// overriding the sweep's own cutoff so the test does not have to fabricate old
// timestamps for the reference side.
func TestSweepRemovesExpiredContentAndThenOrphans(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	oldTs := time.Now().UTC().Add(-48 * time.Hour)
	w.Record(Event{
		TraceID: "old", Ts: oldTs, Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, "an old request's text")}},
	})
	w.Record(Event{
		TraceID: "new", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, "a recent request's text")}},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Expire anything older than an hour (the old row), then orphan-sweep.
	bodies, refs, err := r.SweepContent(ctx, time.Hour)
	if err != nil {
		t.Fatalf("SweepContent: %v", err)
	}
	if refs != 1 {
		t.Errorf("expired refs = %d, want 1 (the old request's)", refs)
	}
	if bodies != 1 {
		t.Errorf("swept bodies = %d, want 1 (orphaned by that expiry)", bodies)
	}

	// The recent request's content survives intact.
	var remaining int64
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM content`).Scan(&remaining); err != nil {
		t.Fatalf("count content: %v", err)
	}
	if remaining != 1 {
		t.Errorf("content rows = %d, want 1 (the recent one)", remaining)
	}
}

// TestForgetRequestsDropsContentButKeepsMetadata pins the deliberate asymmetry:
// "forget this conversation" removes the text and leaves the routing/cost row
// standing, because the two have different lifetimes by design.
func TestForgetRequestsDropsContentButKeepsMetadata(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	w.Record(Event{
		TraceID: "t", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, "forget me")}},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows, err := r.ListRequests(ctx, RequestFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListRequests = %v, %v", rows, err)
	}

	if _, err := r.ForgetRequests(ctx, []int64{rows[0].ID}); err != nil {
		t.Fatalf("ForgetRequests: %v", err)
	}

	var bodies int64
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM content`).Scan(&bodies); err != nil {
		t.Fatalf("count content: %v", err)
	}
	if bodies != 0 {
		t.Errorf("content rows = %d, want 0 after forgetting", bodies)
	}

	// Metadata survives — that is the point of separating the two.
	after, err := r.ListRequests(ctx, RequestFilter{})
	if err != nil {
		t.Fatalf("ListRequests after forget: %v", err)
	}
	if len(after) != 1 {
		t.Errorf("requests after forget = %d, want 1 (metadata outlives content)", len(after))
	}
}

// TestUncapturedBlocksStillDedup proves the hash-only path: a block whose bytes
// are not stored still gets a reference and still collapses to one row, which is
// what keeps binary content addressed without being kept.
func TestUncapturedBlocksStillDedup(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	image := Block{Kind: "image", Body: nil, Role: "user", MsgIndex: 1, Position: 0}
	for i := 0; i < 2; i++ {
		w.Record(Event{
			TraceID: "t", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
			Content: &CapturedContent{Request: []Block{image}},
		})
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var bodies, refs int64
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM content`).Scan(&bodies); err != nil {
		t.Fatalf("count content: %v", err)
	}
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM content_refs`).Scan(&refs); err != nil {
		t.Fatalf("count refs: %v", err)
	}
	if bodies != 1 {
		t.Errorf("content rows = %d, want 1 (hash-only blocks still dedup)", bodies)
	}
	if refs != 2 {
		t.Errorf("content_refs rows = %d, want 2 (one per request)", refs)
	}

	var bodyNull int
	if err := r.db.QueryRowContext(ctx, `SELECT body IS NULL FROM content LIMIT 1`).Scan(&bodyNull); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if bodyNull != 1 {
		t.Error("body is not NULL for an uncaptured block; the file was stored against the decision")
	}

	// A reader must see the reference and report it as not captured.
	rows, err := r.ListRequests(ctx, RequestFilter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	blocks, err := r.ContentForRequest(ctx, rows[0].ID)
	if err != nil {
		t.Fatalf("ContentForRequest: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Captured {
		t.Errorf("blocks = %+v, want one block reported as not captured", blocks)
	}
}

// TestCaptureRequestSkipsEmptyBlocks proves empty text is not stored: an empty
// body would hash to the same address for every request and dominate the
// "appears everywhere" query with noise.
func TestCaptureRequestSkipsEmptyBlocks(t *testing.T) {
	req := &types.NormalizedRequest{
		SystemPrompt: "",
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{
				{Type: "text", Text: ""},
				{Type: "text", Text: "real content"},
			}},
		},
	}
	blocks := CaptureRequest(req)
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1 (empty text skipped): %+v", len(blocks), blocks)
	}
	if string(blocks[0].Body) != "real content" {
		t.Errorf("body = %q, want %q", blocks[0].Body, "real content")
	}
	// Position reflects the skipped block, so a rebuild can still tell where the
	// empty one was.
	if blocks[0].Position != 1 {
		t.Errorf("position = %d, want 1 (the second block)", blocks[0].Position)
	}
}

// TestCaptureRequestPutsSystemFirst proves the system prompt is its own block at
// index 0 — concatenating it with a user turn would make the client's injected
// preamble hash together with real content and stop it grouping.
func TestCaptureRequestPutsSystemFirst(t *testing.T) {
	req := &types.NormalizedRequest{
		SystemPrompt: "injected preamble",
		Messages:     []types.Message{{Role: "user", Content: []types.ContentBlock{{Type: "text", Text: "question"}}}},
	}
	blocks := CaptureRequest(req)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	if blocks[0].Role != "system" || string(blocks[0].Body) != "injected preamble" {
		t.Errorf("first block = %+v, want the system text alone", blocks[0])
	}
	if blocks[1].Role != "user" || string(blocks[1].Body) != "question" {
		t.Errorf("second block = %+v, want the user text", blocks[1])
	}
	if blocks[1].MsgIndex != 1 {
		t.Errorf("user MsgIndex = %d, want 1 (system occupies 0)", blocks[1].MsgIndex)
	}
}

// TestBlockHashIsOverTheTextNotJSON pins the reason text hashes raw: if it were
// hashed as JSON, the same string with different escaping would look like
// different content and the boilerplate query would fragment.
func TestBlockHashIsOverTheTextNotJSON(t *testing.T) {
	verbatim := Block{Kind: "text", Body: []byte("hello \"world\"\nnewline")}
	// Two blocks with identical text always share an address...
	if string(verbatim.Hash()) != string((Block{Kind: "text", Body: []byte("hello \"world\"\nnewline")}).Hash()) {
		t.Error("identical text produced different hashes")
	}
	// ...and text that differs only by JSON escaping is genuinely different.
	if string(verbatim.Hash()) == string((Block{Kind: "text", Body: []byte(`"hello \"world\"\nnewline"`)}).Hash()) {
		t.Error("a JSON-escaped form hashed the same as the raw text")
	}
}
