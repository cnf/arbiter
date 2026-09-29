package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/pkg/types"
)

// captureFixture opens a real store and returns a writer plus a reader over the
// same file, so these tests exercise the same transaction path the pipeline
// uses rather than a hand-rolled insert.
func captureFixture(t *testing.T) (*SQLiteWriter, *Reader) {
	t.Helper()
	w, r, _ := captureFixtureAt(t)
	return w, r
}

// captureFixtureAt is captureFixture plus the database path, for tests that need
// to reopen the file — a live tail has to observe rows written *after* it
// started reading, which means a second writer over the same file.
func captureFixtureAt(t *testing.T) (*SQLiteWriter, *Reader, string) {
	t.Helper()
	w, path := newTestWriter(t)
	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return w, r, path
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

	blocks, _, err := r.ContentForRequest(ctx, rows[0].ID, false)
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

// TestRefusedRequestContentIsVisibleInAggregates proves #5's store-layer
// invariant: a refused request (pipeline.recordFailed's Event — a real row,
// status_code + error set, content under owner_kind="request" like any other
// client request) is NOT invisible to the queries that join requests — the
// opposite of the pre-#5 owner_kind="rejected" design, which existed
// specifically to keep a refusal out of every aggregate.
func TestRefusedRequestContentIsVisibleInAggregates(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	w.Record(Event{
		TraceID: "t-refused", Format: "openai", Model: "m", Kind: "client",
		StatusCode: 429, Error: "guardrail g: request blocked by prompt match",
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, "a request that will be refused")}},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var requests int64
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM requests`).Scan(&requests); err != nil {
		t.Fatalf("count requests: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests rows = %d, want 1 — a refused request must get a real row", requests)
	}

	overall, err := r.Overall(ctx, WindowFrom(time.Hour))
	if err != nil {
		t.Fatalf("Overall: %v", err)
	}
	if overall.Requests != 1 {
		t.Errorf("Overall.Requests = %d, want 1 — the refusal is real client traffic, not invisible", overall.Requests)
	}
	if overall.Errors != 1 {
		t.Errorf("Overall.Errors = %d, want 1 (status_code 429 >= 400)", overall.Errors)
	}
}

// TestClassifierRowsDoNotInflateRepeatedContent is the regression for a
// classifier call's own captured input. A classifier row IS a requests row, and
// its input is byte-identical to a block of the client request that triggered
// it — so without a kind filter on the join, every classifier call would count
// as a second "request" containing that block, and the boilerplate page would
// report a client's preamble as twice as widespread as it is.
func TestClassifierRowsDoNotInflateRepeatedContent(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	preamble := textBlock("system", 0, 0, "You are an AI assistant. Always respond in Markdown.")

	// One real client request carrying the preamble.
	w.Record(Event{
		TraceID: "t1", SessionKey: "session-a", Format: "openai", Provider: "p", Model: "m",
		StatusCode: 200, Kind: "client",
		Content: &CapturedContent{Request: []Block{preamble}},
	})
	// A classifier call whose input is that same text, recorded as its own row.
	w.Record(Event{
		TraceID: "t1", SessionKey: "session-a", Format: "openai", Provider: "cls", Model: "cls-m",
		StatusCode: 200, Kind: "classifier",
		Content: &CapturedContent{Request: []Block{textBlock("user", 1, 0, "You are an AI assistant. Always respond in Markdown.")}},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// min_requests=2: only the classifier row could satisfy this, so the
	// preamble must not appear at all.
	repeated, err := r.RepeatedContent(ctx, WindowFrom(time.Hour), 2, 0, 50)
	if err != nil {
		t.Fatalf("RepeatedContent: %v", err)
	}
	if len(repeated) != 0 {
		t.Errorf("repeated content = %+v, want none (the classifier row must not count as a second request)", repeated)
	}

	// The drill-down must not list the classifier row either.
	rows, err := r.SessionsForContent(ctx, ContentHashHex(preamble.Hash()), 50)
	if err != nil {
		t.Fatalf("SessionsForContent: %v", err)
	}
	if len(rows) != 1 || rows[0].Kind != "client" {
		t.Errorf("SessionsForContent = %+v, want exactly the one client row", rows)
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
	blocks, _, err := r.ContentForRequest(ctx, rows[0].ID, false)
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

// TestCaptureRequestStoresToolDefinitions is #60: the tool definitions a client
// offers are stored, one block per tool, so "what tools was this agent offered"
// is answerable from the store. Tool *calls* were already recorded; the
// definitions they were made against were not, on any path.
func TestCaptureRequestStoresToolDefinitions(t *testing.T) {
	req := &types.NormalizedRequest{
		Messages: []types.Message{{Role: "user", Content: []types.ContentBlock{{Type: "text", Text: "read the file"}}}},
		Tools: []types.Tool{
			{Name: "read_file", Description: "Read a file", InputSchema: map[string]interface{}{"type": "object"}},
			{Name: "write_file", Description: "Write a file"},
		},
	}
	blocks := CaptureRequest(req)

	var defs []Block
	for _, b := range blocks {
		if b.Kind == "tool_def" {
			defs = append(defs, b)
		}
	}
	if len(defs) != 2 {
		t.Fatalf("got %d tool_def blocks, want 2 (one per tool): %+v", len(defs), blocks)
	}

	// Position is the tool's index in the request's tools array, so the store
	// can say which slot a definition occupied.
	if defs[0].Position != 0 || defs[1].Position != 1 {
		t.Errorf("positions = %d,%d, want 0,1 in tools order", defs[0].Position, defs[1].Position)
	}
	// A tool definition belongs to no message, so it must not share a
	// coordinate with one — 0 is the system prompt's index.
	if defs[0].MsgIndex != toolDefsMsgIndex || defs[1].MsgIndex != toolDefsMsgIndex {
		t.Errorf("MsgIndex = %d, want %d (tool defs are not message blocks)",
			defs[0].MsgIndex, toolDefsMsgIndex)
	}
	// The kind is "tool_def", not "tool_use": what the client OFFERED is a
	// different fact from what the model CHOSE to call, and a query that
	// conflates them cannot answer "which clients send read_file".
	if defs[0].Kind == "tool_use" {
		t.Error("tool definitions were stored as tool_use, conflating offered with called")
	}
	if !strings.Contains(string(defs[0].Body), `"read_file"`) {
		t.Errorf("tool body = %q, want the tool's name", defs[0].Body)
	}
	if !strings.Contains(string(defs[0].Body), `"Read a file"`) {
		t.Errorf("tool body = %q, want the description retained", defs[0].Body)
	}

	// The user message must still be present and still indexed from 0 — tool
	// defs are additive and must not shift the message numbering.
	user := blocks[len(blocks)-1]
	if user.Role != "user" || user.MsgIndex != 0 {
		t.Errorf("user block = %+v, want MsgIndex 0 (tool defs must not shift messages)", user)
	}
}

// TestCaptureRequestToolDefinitionsDedupByName proves the reason the store
// keeps one block per tool instead of one blob for the whole array: with an
// array blob, the same `read_file` reaching Arbiter from two clients whose
// descriptions differ slightly would hash differently and read as two
// unrelated tools, making "which clients send read_file" unanswerable.
func TestCaptureRequestToolDefinitionsDedupByName(t *testing.T) {
	a := CaptureRequest(&types.NormalizedRequest{
		Tools: []types.Tool{{Name: "read_file", Description: "Read a file"}},
	})
	b := CaptureRequest(&types.NormalizedRequest{
		Tools: []types.Tool{{Name: "read_file", Description: "Read a file"}},
	})
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("got %d and %d blocks, want 1 each", len(a), len(b))
	}
	if string(a[0].Hash()) != string(b[0].Hash()) {
		t.Error("the same tool from two requests hashed differently, so it cannot dedup")
	}

	// A different description is a genuinely different tool definition and
	// must not be silently merged into the same address.
	c := CaptureRequest(&types.NormalizedRequest{
		Tools: []types.Tool{{Name: "read_file", Description: "Read a file from disk"}},
	})
	if string(a[0].Hash()) == string(c[0].Hash()) {
		t.Error("a differently-described tool hashed the same, hiding the difference")
	}
}

// TestToolDefinitionsRoundTripThroughStore closes #60 end to end: a tool
// definition captured by CaptureRequest must actually reach the content tables
// and come back readable, addressed by its own name. Testing CaptureRequest
// alone would prove only that blocks were produced, not that the store keeps
// them — the write path, the hash-only branch, and ContentForRequest all sit
// between those two facts.
func TestToolDefinitionsRoundTripThroughStore(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	req := &types.NormalizedRequest{
		Messages: []types.Message{{Role: "user", Content: []types.ContentBlock{{Type: "text", Text: "read it"}}}},
		Tools: []types.Tool{
			{Name: "read_file", Description: "Read a file", InputSchema: map[string]interface{}{"type": "object"}},
		},
	}
	w.Record(Event{
		TraceID: "t", Format: "anthropic", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: CaptureRequest(req)},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows, err := r.ListRequests(ctx, RequestFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListRequests = %v, %v", rows, err)
	}
	blocks, _, err := r.ContentForRequest(ctx, rows[0].ID, false)
	if err != nil {
		t.Fatalf("ContentForRequest: %v", err)
	}

	var def *ContentBlock
	for i := range blocks {
		if blocks[i].BlockType == "tool_def" {
			def = &blocks[i]
		}
	}
	if def == nil {
		t.Fatalf("no tool_def block survived the store: %+v", blocks)
	}
	// Captured, not hash-only: a definition whose bytes are dropped is
	// addressable but unreadable, which is the same as not having it.
	if !def.Captured {
		t.Error("tool definition was stored hash-only, so its schema cannot be read back")
	}
	if def.Role != "tool_def" {
		t.Errorf("role = %q, want tool_def", def.Role)
	}
	if !strings.Contains(def.Body, "read_file") || !strings.Contains(def.Body, "Read a file") {
		t.Errorf("body = %q, want the name and description", def.Body)
	}
	// The message content must still reassemble alongside it.
	var sawUser bool
	for _, b := range blocks {
		if b.Role == "user" && b.Body == "read it" {
			sawUser = true
		}
	}
	if !sawUser {
		t.Errorf("tool definitions displaced the message content: %+v", blocks)
	}
}

// TestCaptureRequestSkipsNamelessTools: a nameless schema would dedup against
// every other nameless schema and address nothing — the same uselessness that
// empty text blocks are skipped for.
func TestCaptureRequestSkipsNamelessTools(t *testing.T) {
	req := &types.NormalizedRequest{
		Tools: []types.Tool{
			{Name: "", Description: "anonymous"},
			{Name: "real_tool"},
		},
	}
	blocks := CaptureRequest(req)
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1 (nameless tool skipped): %+v", len(blocks), blocks)
	}
	if blocks[0].Position != 1 {
		t.Errorf("position = %d, want 1 — the nameless tool still occupied slot 0", blocks[0].Position)
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
