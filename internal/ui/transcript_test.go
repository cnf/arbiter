package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// The transcript page is a conversation, so this seeds one: three client turns
// with a classifier call nested under the second. It exercises the whole
// render path — the real reader paging/totals/children queries and the list and
// inspector templates — rather than a zero-data render, which
// TestEveryPageRendersWithZeroData already covers.
func TestTranscriptRendersTurnsWithNestedInternalCalls(t *testing.T) {
	now := time.Now().UTC()
	key := "sess-transcript"

	clientEvent := func(traceID string, offset time.Duration, status int, content *store.CapturedContent) store.Event {
		return store.Event{
			TraceID: traceID, SessionKey: key, Kind: "client",
			Provider: "anthropic", Model: "claude-sonnet", StatusCode: status,
			Ts: now.Add(offset), LatencyMs: 120, Content: content,
		}
	}

	events := []store.Event{
		clientEvent("tr-1", 0, 200, &store.CapturedContent{
			Request: []store.Block{
				{MsgIndex: 0, Position: 0, Role: "system", Kind: "text", Body: []byte("you are a helpful assistant")},
				{MsgIndex: 1, Position: 0, Role: "user", Kind: "text", Body: []byte("list the files in the repo")},
			},
			Response: []store.Block{
				{MsgIndex: 0, Position: 0, Role: "assistant", Kind: "text", Body: []byte("I'll look at the tree first")},
				{MsgIndex: 0, Position: 1, Role: "assistant", Kind: "tool_use",
					Body: []byte(`{"id":"call_1","name":"Bash","input":{"command":"ls -la"}}`)},
			},
		}),
		// A classifier call sharing tr-2's trace: it must nest under the
		// client turn that spawned it, not appear as a turn of its own.
		{TraceID: "tr-2", SessionKey: key, Kind: "classifier",
			Provider: "openrouter", Model: "small", StatusCode: 200,
			Ts: now.Add(1500 * time.Millisecond), LatencyMs: 40,
			Domain: "coding", Difficulty: "medium", Confidence: 0.8,
			RoutingRationale: "matched on the tool-call signal"},
		clientEvent("tr-2", 2*time.Second, 200, &store.CapturedContent{
			Request: []store.Block{
				{MsgIndex: 0, Position: 0, Role: "user", Kind: "text", Body: []byte("now edit the file")},
			},
			Response: []store.Block{
				{MsgIndex: 0, Position: 0, Role: "assistant", Kind: "text", Body: []byte("editing it now")},
			},
		}),
		clientEvent("tr-3", 4*time.Second, 500, nil),
	}

	h, _ := newSeededHandler(t, events...)
	body := serve(t, h, "GET", "/admin/ui/session?key="+key, false).Body.String()

	for _, want := range []string{
		// Numbering is the page's own (its conversation order), not the store's.
		"#1", "#2", "#3",
		// The user message reaches the list as a preview and the inspector
		// as a rendered body.
		"list the files in the repo",
		// The classifier nests as a child row, carrying its own kind.
		"classifier",
		// The tool call is rendered content-first (the command), not as a
		// JSON blob — summarizeToolInput's whole reason for existing.
		"ls -la",
		// The classifier's verdict axes are rendered from its own row.
		"domain=coding",
		"matched on the tool-call signal",
		// The failed turn is marked, not merely listed.
		`class="dot err"`,
		// The session's own header numbers come from SessionTotals.
		"requests",
		"errors",
		// Every turn's inspector ships hidden with the document.
		`id="inspector-src"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("transcript page missing %q\n--- body ---\n%s", want, body)
		}
	}

	// A turn's inspector must be present in the document for every turn, or
	// selecting that turn would empty the pane.
	if n := strings.Count(body, `class="inspector"`); n != 3+1 {
		t.Errorf("got %d inspectors on the page, want 3 client turns + 1 classifier child", n)
	}
}

// A stateless chat API resends the whole prior conversation with every
// request — turn 2's captured request body carries turn 1's user message and
// system prompt all over again, not just what's new. The transcript must
// show each turn's own new content only, or every turn after the first
// renders every earlier turn's user message and tool result a second time
// (the exact bug this guards against: two different turns display the same
// captured request body).
func TestTranscriptShowsOnlyEachTurnsOwnContent(t *testing.T) {
	now := time.Now().UTC()
	key := "sess-resend"

	events := []store.Event{
		{TraceID: "tr-1", SessionKey: key, Kind: "client",
			Provider: "anthropic", Model: "claude-sonnet", StatusCode: 200,
			Ts: now, LatencyMs: 100,
			Content: &store.CapturedContent{
				Request: []store.Block{
					{MsgIndex: 0, Position: 0, Role: "system", Kind: "text", Body: []byte("you are a helpful assistant")},
					{MsgIndex: 1, Position: 0, Role: "user", Kind: "text", Body: []byte("what files are in the repo")},
				},
				Response: []store.Block{
					{MsgIndex: 0, Position: 0, Role: "assistant", Kind: "text", Body: []byte("there are three go files")},
				},
			}},
		// Turn 2's request resends the system prompt and turn 1's user
		// message and assistant reply (a real client's history), then adds
		// its own new user message at the highest MsgIndex.
		{TraceID: "tr-2", SessionKey: key, Kind: "client",
			Provider: "anthropic", Model: "claude-sonnet", StatusCode: 200,
			Ts: now.Add(2 * time.Second), LatencyMs: 100,
			Content: &store.CapturedContent{
				Request: []store.Block{
					{MsgIndex: 0, Position: 0, Role: "system", Kind: "text", Body: []byte("you are a helpful assistant")},
					{MsgIndex: 1, Position: 0, Role: "user", Kind: "text", Body: []byte("what files are in the repo")},
					{MsgIndex: 2, Position: 0, Role: "assistant", Kind: "text", Body: []byte("there are three go files")},
					{MsgIndex: 3, Position: 0, Role: "user", Kind: "text", Body: []byte("now edit main.go")},
				},
				Response: []store.Block{
					{MsgIndex: 0, Position: 0, Role: "assistant", Kind: "text", Body: []byte("editing it now")},
				},
			}},
	}

	h, _ := newSeededHandler(t, events...)
	body := serve(t, h, "GET", "/admin/ui/session?key="+key, false).Body.String()

	if !strings.Contains(body, "what files are in the repo") {
		t.Errorf("turn 1's own user message is missing:\n%s", body)
	}
	if !strings.Contains(body, "now edit main.go") {
		t.Errorf("turn 2's own new user message is missing:\n%s", body)
	}
	// Turn 2 resent turn 1's user message as history; the inspector must not
	// render it a second time. It appears in turn 1's own row three times by
	// design (the list's search index, the list preview, and the inspector
	// body) but must not additionally appear inside turn 2's inspector.
	if n := strings.Count(body, `<div class="user-message">what files are in the repo</div>`); n != 1 {
		t.Errorf("turn 1's user message appears in %d inspector(s), want 1 (turn 2 re-rendered resent history)\n%s", n, body)
	}
}

// TestTranscriptShowsToolDefinitions is #63: tool definitions are captured
// (#60) but nothing rendered them. A tool_def block belongs to no message
// (msg_index = toolDefsMsgIndex) and must survive newestRequestMessage's
// per-turn narrowing on every turn that captured it, unlike a resent user
// message — the point isn't "this turn's own", it's "the toolset offered for
// the whole conversation".
func TestTranscriptShowsToolDefinitions(t *testing.T) {
	now := time.Now().UTC()
	key := "sess-tooldefs"

	toolDefBlock := func(pos int, name, desc string) store.Block {
		body, err := json.Marshal(struct {
			Name        string                 `json:"name"`
			Description string                 `json:"description,omitempty"`
			InputSchema map[string]interface{} `json:"input_schema,omitempty"`
		}{name, desc, map[string]interface{}{"type": "object"}})
		if err != nil {
			t.Fatalf("marshal tool def fixture: %v", err)
		}
		return store.Block{MsgIndex: -1, Position: pos, Role: "tool_def", Kind: "tool_def", Body: body}
	}

	events := []store.Event{
		{TraceID: "tr-1", SessionKey: key, Kind: "client",
			Provider: "anthropic", Model: "claude-sonnet", StatusCode: 200,
			Ts: now, LatencyMs: 100,
			Content: &store.CapturedContent{
				Request: []store.Block{
					toolDefBlock(0, "read_file", "Read a file from disk"),
					toolDefBlock(1, "write_file", ""),
					{MsgIndex: 0, Position: 0, Role: "system", Kind: "text", Body: []byte("you are a helpful assistant")},
					{MsgIndex: 1, Position: 0, Role: "user", Kind: "text", Body: []byte("read main.go")},
				},
			}},
	}

	h, _ := newSeededHandler(t, events...)
	body := serve(t, h, "GET", "/admin/ui/session?key="+key, false).Body.String()

	for _, want := range []string{
		"tools offered (2, ",
		"read_file",
		"Read a file from disk",
		"write_file",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("transcript page missing %q\n--- body ---\n%s", want, body)
		}
	}
	// The toggle's byte count is the two captured tool_def bodies' combined
	// size — small in this fixture, so it must render in plain "N B" form,
	// not spuriously scaled to kB.
	if !strings.Contains(body, " B)</button>") {
		t.Errorf("tool-defs toggle missing a plain-byte size suffix:\n%s", body)
	}
	// Both fixture tools have names, so the "(unnamed tool)" placeholder
	// (for a definition captured with no name) must not appear.
	if strings.Contains(body, "(unnamed tool)") {
		t.Errorf("named tool defs rendered as unnamed:\n%s", body)
	}
	if n := strings.Count(body, `class="tooldef-row"`); n != 2 {
		t.Errorf("got %d tool-def rows, want 2 (one per captured tool)\n%s", n, body)
	}
}

// Load-more is what makes the list pane usable on a long conversation, and it
// is an htmx fragment: the appended page carries turns, not a second toolbar
// or a second preamble modal.
func TestTranscriptLoadMoreAppendsTurnsOnly(t *testing.T) {
	now := time.Now().UTC()
	key := "sess-long"
	var events []store.Event
	for i := 0; i < 5; i++ {
		events = append(events, store.Event{
			TraceID: fmt.Sprintf("tr-%d", i), SessionKey: key, Kind: "client",
			Provider: "p", Model: "m", StatusCode: 200,
			Ts: now.Add(time.Duration(i) * time.Second), LatencyMs: 10,
		})
	}
	h, _ := newSeededHandler(t, events...)

	body := serve(t, h, "GET", "/admin/ui/session?key="+key+"&limit=2", false).Body.String()

	// The first page offers the next one, and says how far it has got.
	if !strings.Contains(body, `id="more-row"`) {
		t.Errorf("the first page of a longer conversation offers no load-more control:\n%s", body)
	}
	if !strings.Contains(body, "of 5") {
		t.Errorf("the load-more control does not say how many turns there are in total:\n%s", body)
	}

	// The appended page is the fragment form, requested by htmx: it must not
	// re-render the page chrome (crumb, jump control, stats) a second time.
	frag := serve(t, h, "GET", "/admin/ui/session?key="+key+"&limit=2&offset=2", true).Body.String()
	if strings.Contains(frag, `id="jump-trigger"`) || strings.Contains(frag, "toolbar") {
		t.Errorf("the appended page re-renders the page chrome; it must carry turns only:\n%s", frag)
	}
	if !strings.Contains(frag, "#3") || !strings.Contains(frag, "#4") {
		t.Errorf("the appended page does not carry the turns it is a page of:\n%s", frag)
	}

	// The last page ends the conversation rather than offering another page.
	last := serve(t, h, "GET", "/admin/ui/session?key="+key+"&limit=2&offset=4", true).Body.String()
	if strings.Contains(last, `id="more-row"`) {
		t.Errorf("the final page still offers a load-more control:\n%s", last)
	}
	if !strings.Contains(last, "end of conversation") {
		t.Errorf("the final page does not say the conversation ends there:\n%s", last)
	}
}

// A jump to a turn number is a server question — which page does turn N fall
// on? — and it has to answer for a turn that is not loaded, name a turn that
// does not exist, and refuse a value that is not a turn number at all.
func TestTranscriptJumpResolvesTurnToItsPage(t *testing.T) {
	now := time.Now().UTC()
	key := "sess-jump"
	var events []store.Event
	for i := 0; i < 5; i++ {
		events = append(events, store.Event{
			TraceID: fmt.Sprintf("tr-%d", i), SessionKey: key, Kind: "client",
			Provider: "p", Model: "m", StatusCode: 200,
			Ts: now.Add(time.Duration(i) * time.Second), LatencyMs: 10,
		})
	}
	h, _ := newSeededHandler(t, events...)

	// Turn 5 of 5 is not on the first page of 2, so the jump must land on the
	// page that contains it (offset 4), and open on it.
	body := serve(t, h, "GET", "/admin/ui/session?key="+key+"&limit=2&seq=5", false).Body.String()
	if !strings.Contains(body, "#5") {
		t.Errorf("jump to turn 5 did not land on the page containing it:\n%s", body)
	}
	if !strings.Contains(body, "TRANSCRIPT_SELECTED = ") {
		t.Errorf("the jump did not hand the page a turn to open on:\n%s", body)
	}
	if strings.Contains(body, "#1<") {
		t.Errorf("the jump rendered an earlier page than the one turn 5 falls on:\n%s", body)
	}

	// A turn past the end is a 404, not an empty page that looks like a bug.
	rec := serve(t, h, "GET", "/admin/ui/session?key="+key+"&seq=99", false)
	if rec.Code != 404 {
		t.Errorf("jump to a turn that does not exist = %d, want 404", rec.Code)
	}

	// A non-numeric turn is a bad request, not a silent jump to turn 1.
	rec = serve(t, h, "GET", "/admin/ui/session?key="+key+"&seq=abc", false)
	if rec.Code != 400 {
		t.Errorf("jump with a non-numeric turn = %d, want 400", rec.Code)
	}
}

// TestTranscriptJumpCentersTheWindow is #73's fix for a deep link that could
// not scroll upward: ?seq=N used to load the page-aligned window starting at
// N's own page boundary, so landing near the start of a later window left
// nothing above it to load "older" into. A centered window gives the reader
// room to scroll toward either edge.
func TestTranscriptJumpCentersTheWindow(t *testing.T) {
	now := time.Now().UTC()
	key := "sess-center"
	var events []store.Event
	for i := 0; i < 20; i++ {
		events = append(events, store.Event{
			TraceID: fmt.Sprintf("tr-%d", i), SessionKey: key, Kind: "client",
			Provider: "p", Model: "m", StatusCode: 200,
			Ts: now.Add(time.Duration(i) * time.Second), LatencyMs: 10,
		})
	}
	h, _ := newSeededHandler(t, events...)

	// Turn 10 of 20, a window of 4: a page-aligned window would start at
	// offset 8 (turn 9) and run to turn 12, all forward of turn 10. A
	// centered window starts at n-1-limit/2 = 7 (turn 8) and both has turn
	// 10 near its middle and offers a "load older" control, since offset 7
	// is not the start of the conversation.
	body := serve(t, h, "GET", "/admin/ui/session?key="+key+"&limit=4&seq=10", false).Body.String()
	if !strings.Contains(body, "#8<") {
		t.Errorf("a centered jump to turn 10 (window 4) should include turn 8, the window's start:\n%s", body)
	}
	if !strings.Contains(body, "#10<") {
		t.Errorf("a centered jump to turn 10 did not land on a window containing it:\n%s", body)
	}
	if !strings.Contains(body, `id="older-row"`) {
		t.Errorf("a jump into the middle of a long conversation offers no load-older control:\n%s", body)
	}

	// A jump near the very start still clamps to offset 0 rather than a
	// negative offset, and offers no "load older" control since there is
	// nothing before turn 1.
	early := serve(t, h, "GET", "/admin/ui/session?key="+key+"&limit=4&seq=2", false).Body.String()
	if !strings.Contains(early, "#1<") {
		t.Errorf("a jump near the start should still include turn 1:\n%s", early)
	}
	if strings.Contains(early, `id="older-row"`) {
		t.Errorf("a jump landing at the start of the conversation should offer no load-older control:\n%s", early)
	}
}

// TestTranscriptLoadMoreShipsInspectors is #73's fix for bug 1: a turn loaded
// by "load more" used to append only its list row, never the paired
// .inspector markup — only page 1 shipped inspectors, via #inspector-src — so
// clicking a later-loaded row had nothing to select.
func TestTranscriptLoadMoreShipsInspectors(t *testing.T) {
	now := time.Now().UTC()
	key := "sess-more-inspectors"
	var events []store.Event
	for i := 0; i < 4; i++ {
		events = append(events, store.Event{
			TraceID: fmt.Sprintf("tr-%d", i), SessionKey: key, Kind: "client",
			Provider: "p", Model: "m", StatusCode: 200,
			Ts: now.Add(time.Duration(i) * time.Second), LatencyMs: 10,
		})
	}
	h, _ := newSeededHandler(t, events...)

	frag := serve(t, h, "GET", "/admin/ui/session?key="+key+"&limit=2&offset=2", true).Body.String()
	if !strings.Contains(frag, `class="inspector"`) {
		t.Errorf("a load-more fragment must ship its own turns' inspector markup, or later rows are not selectable:\n%s", frag)
	}
}

// TestTranscriptLoadOlderPrependsWithInspectors is #73's fix for bug 2: there
// was no "load older" path at all, so a deep link into the middle of a long
// conversation could scroll forward but never see what came before it. The
// fragment must also ship inspector markup, for the same reason
// TestTranscriptLoadMoreShipsInspectors does.
func TestTranscriptLoadOlderPrependsWithInspectors(t *testing.T) {
	now := time.Now().UTC()
	key := "sess-older"
	var events []store.Event
	for i := 0; i < 6; i++ {
		events = append(events, store.Event{
			TraceID: fmt.Sprintf("tr-%d", i), SessionKey: key, Kind: "client",
			Provider: "p", Model: "m", StatusCode: 200,
			Ts: now.Add(time.Duration(i) * time.Second), LatencyMs: 10,
		})
	}
	h, _ := newSeededHandler(t, events...)

	// A reader sitting at offset 4 (having jumped or scrolled there) asks
	// for what came before.
	frag := serve(t, h, "GET", "/admin/ui/session?key="+key+"&limit=2&before=4", true).Body.String()
	if !strings.Contains(frag, "#3<") || !strings.Contains(frag, "#4<") {
		t.Errorf("load-older did not return the window immediately before position 4:\n%s", frag)
	}
	if !strings.Contains(frag, `class="inspector"`) {
		t.Errorf("a load-older fragment must ship its own turns' inspector markup:\n%s", frag)
	}
	// Offset 2 is not the start of a 6-turn conversation, so there is still
	// more to load older.
	if !strings.Contains(frag, `id="older-row"`) {
		t.Errorf("load-older landing short of the conversation's start should still offer another load-older control:\n%s", frag)
	}

	// Asking for what came before position 0 returns nothing more to load.
	atStart := serve(t, h, "GET", "/admin/ui/session?key="+key+"&limit=2&before=2", true).Body.String()
	if strings.Contains(atStart, `id="older-row"`) {
		t.Errorf("load-older reaching the start of the conversation should offer no further control:\n%s", atStart)
	}
}

// TestTranscriptLoadMoreControlReplacesItself guards against a regression
// where the auto-fill in transcript.js loops forever: session.html used to
// carry its own hand-copied "load more" button (hx-target="#list",
// hx-swap="beforeend") that drifted from transcriptList.html's, which had
// since moved to replacing the control in place. Two divergent copies meant
// every append left the previous #more-row behind instead of replacing it,
// so the observer kept re-discovering the same, never-advancing offset —
// confirmed against a live page with Playwright, where the request never
// advanced past offset=30. The control must always point at itself, not at
// #list, and use outerHTML, not beforeend/afterbegin, in both the page's
// initial render and every load-more/load-older fragment thereafter.
func TestTranscriptLoadMoreControlReplacesItself(t *testing.T) {
	now := time.Now().UTC()
	key := "sess-more-self-target"
	var events []store.Event
	for i := 0; i < 6; i++ {
		events = append(events, store.Event{
			TraceID: fmt.Sprintf("tr-%d", i), SessionKey: key, Kind: "client",
			Provider: "p", Model: "m", StatusCode: 200,
			Ts: now.Add(time.Duration(i) * time.Second), LatencyMs: 10,
		})
	}
	h, _ := newSeededHandler(t, events...)

	initial := serve(t, h, "GET", "/admin/ui/session?key="+key+"&limit=2", false).Body.String()
	if !strings.Contains(initial, `hx-target="#more-row" hx-swap="outerHTML"`) {
		t.Errorf("the page's own initial \"load more\" control must target itself with outerHTML, not #list/beforeend:\n%s", initial)
	}
	if strings.Contains(initial, `hx-target="#list"`) {
		t.Errorf("the initial page must not carry a hand-copied load-more control that targets #list:\n%s", initial)
	}

	frag := serve(t, h, "GET", "/admin/ui/session?key="+key+"&limit=2&offset=2", true).Body.String()
	if !strings.Contains(frag, `hx-target="#more-row" hx-swap="outerHTML"`) {
		t.Errorf("a load-more fragment's own \"load more\" control must also target itself with outerHTML:\n%s", frag)
	}

	older := serve(t, h, "GET", "/admin/ui/session?key="+key+"&limit=2&before=4", true).Body.String()
	if !strings.Contains(older, `hx-target="#older-row" hx-swap="outerHTML"`) {
		t.Errorf("a load-older fragment's own \"load older\" control must target itself with outerHTML:\n%s", older)
	}
}

// TestTranscriptIDOpensTheOwningTurn covers the Sessions lane list's "open"
// link (sessionNodeHref): unlike ?seq=N (a turn number, resolved once inside
// SessionHandler already), ?id=<request id> is what a lane node actually
// carries, and a satellite (classifier) node's id has no turn of its own —
// it must resolve through its trace_id to the client turn that spawned it,
// exactly what SessionTurnForRequest does and what used to be impossible
// because the old /admin/ui/requests destination no longer exists.
func TestTranscriptIDOpensTheOwningTurn(t *testing.T) {
	now := time.Now().UTC()
	key := "sess-id-link"
	events := []store.Event{
		{TraceID: "tr-1", SessionKey: key, Kind: "client",
			Provider: "p", Model: "m", StatusCode: 200, Ts: now, LatencyMs: 10},
		// A classifier call nested under turn 2 by trace_id.
		{TraceID: "tr-2", SessionKey: key, Kind: "classifier",
			Provider: "openrouter", Model: "small", StatusCode: 200,
			Ts: now.Add(500 * time.Millisecond), LatencyMs: 5},
		{TraceID: "tr-2", SessionKey: key, Kind: "client",
			Provider: "p", Model: "m", StatusCode: 200,
			Ts: now.Add(time.Second), LatencyMs: 10},
	}
	h, _ := newSeededHandler(t, events...)

	rows, err := h.reader.ListRequests(context.Background(), store.RequestFilter{SessionKey: key, Limit: 10})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	var turn1ID, satelliteID, turn2ID int64
	for _, row := range rows {
		switch {
		case row.Kind == "classifier":
			satelliteID = row.ID
		case row.TraceID == "tr-1":
			turn1ID = row.ID
		case row.TraceID == "tr-2" && row.Kind == "client":
			turn2ID = row.ID
		}
	}
	if turn1ID == 0 || satelliteID == 0 || turn2ID == 0 {
		t.Fatalf("did not find all three seeded rows: turn1=%d satellite=%d turn2=%d", turn1ID, satelliteID, turn2ID)
	}

	// ?id= of the turn-1 client row opens on turn 1.
	body := serve(t, h, "GET", fmt.Sprintf("/admin/ui/session?key=%s&limit=2&id=%d", key, turn1ID), false).Body.String()
	if got := selectedIDFromBody(t, body); got != turn1ID {
		t.Errorf("?id=%d (turn 1 client row) selected %d, want itself", turn1ID, got)
	}

	// ?id= of the satellite must resolve through trace_id to turn 2 (its
	// parent), land on the page containing turn 2, and select the
	// SATELLITE's own id — not the parent's — so the child row (not the
	// parent row) is what actually highlights.
	body = serve(t, h, "GET", fmt.Sprintf("/admin/ui/session?key=%s&limit=2&id=%d", key, satelliteID), false).Body.String()
	if got := selectedIDFromBody(t, body); got != satelliteID {
		t.Errorf("?id=%d (satellite) selected %d, want the satellite's own id", satelliteID, got)
	}
	if !strings.Contains(body, "#2") {
		t.Errorf("?id=%d (satellite) did not land on turn 2's page:\n%s", satelliteID, body)
	}

	// An id from a different session is a 404, not a cross-session leak.
	rec := serve(t, h, "GET", "/admin/ui/session?key=other-session&id="+fmt.Sprint(turn1ID), false)
	if rec.Code != 404 {
		t.Errorf("?id= from a different session = %d, want 404", rec.Code)
	}

	// A non-numeric id is a bad request.
	rec = serve(t, h, "GET", "/admin/ui/session?key="+key+"&id=abc", false)
	if rec.Code != 400 {
		t.Errorf("?id=abc = %d, want 400", rec.Code)
	}

	// An id that does not exist is a 404.
	rec = serve(t, h, "GET", "/admin/ui/session?key="+key+"&id=999999", false)
	if rec.Code != 404 {
		t.Errorf("?id= for a nonexistent request = %d, want 404", rec.Code)
	}
}

// selectedIDFromBody extracts window.TRANSCRIPT_SELECTED's numeric value
// from a rendered session page. html/template's contextual JS auto-escaper
// inserts protective whitespace around interpolated values inside a
// <script> block (`= 1 ;` rather than `= 1;`), so an exact-string match on
// the surrounding source is fragile; parsing the number out is not.
func selectedIDFromBody(t *testing.T, body string) int64 {
	t.Helper()
	const marker = "TRANSCRIPT_SELECTED ="
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("TRANSCRIPT_SELECTED not found in body:\n%s", body)
	}
	rest := body[i+len(marker):]
	end := strings.IndexAny(rest, ";")
	if end < 0 {
		t.Fatalf("TRANSCRIPT_SELECTED has no terminating ';':\n%s", rest)
	}
	var id int64
	if _, err := fmt.Sscanf(strings.TrimSpace(rest[:end]), "%d", &id); err != nil {
		t.Fatalf("TRANSCRIPT_SELECTED value %q did not parse as an int: %v", rest[:end], err)
	}
	return id
}

// A session key that names nothing is a plain answer, not a 500 or a blank
// page: a reader followed a link and needs to know the key did not match.
func TestTranscriptUnknownSessionSaysSo(t *testing.T) {
	now := time.Now().UTC()
	h, _ := newSeededHandler(t, store.Event{
		TraceID: "t", SessionKey: "other", Kind: "client",
		Provider: "p", Model: "m", StatusCode: 200, Ts: now, LatencyMs: 1,
	})

	rec := serve(t, h, "GET", "/admin/ui/session?key=nope", false)
	if rec.Code != 200 {
		t.Fatalf("an unknown session key = %d, want 200 with an explanation", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No requests recorded under this session key") {
		t.Errorf("an unknown key does not say so:\n%s", body)
	}

	// And the key is required: a transcript with no session is not a page.
	rec = serve(t, h, "GET", "/admin/ui/session", false)
	if rec.Code != 400 {
		t.Errorf("a missing key = %d, want 400", rec.Code)
	}
}

func TestPreambleHiddenAndEscapeRestoresIt(t *testing.T) {
	css, err := assets.ReadFile("static/app.css")
	if err != nil {
		t.Fatalf("read embedded CSS: %v", err)
	}
	if !strings.Contains(string(css), ".preamble-src:not([hidden]) {") {
		t.Error("preamble layout rule must not override the hidden attribute")
	}

	js, err := assets.ReadFile("static/transcript.js")
	if err != nil {
		t.Fatalf("read embedded transcript script: %v", err)
	}
	script := string(js)
	escapeClose := "if (preambleModal && preambleModal.classList.contains(\"open\")) {\n      closePreamble();"
	if !strings.Contains(script, escapeClose) {
		t.Error("Escape must use closePreamble so the active prompt is restored and hidden")
	}
}

// The preamble modal shows the session's standing instructions in both forms,
// and only claims a guardrail touched them when the two actually differ.
func TestTranscriptPreambleModalShowsBothForms(t *testing.T) {
	now := time.Now().UTC()
	key := "sess-pre"
	h, _ := newSeededHandler(t, store.Event{
		TraceID: "t1", SessionKey: key, Kind: "client",
		Provider: "p", Model: "m", StatusCode: 200, Ts: now, LatencyMs: 1,
		Content: &store.CapturedContent{
			Request:            []store.Block{{MsgIndex: 0, Position: 0, Role: "system", Kind: "text", Body: []byte("original instructions")}},
			RequestGuardrailed: []store.Block{{MsgIndex: 0, Position: 0, Role: "system", Kind: "text", Body: []byte("rewritten instructions")}},
		},
	})

	body := serve(t, h, "GET", "/admin/ui/session?key="+key, false).Body.String()
	for _, want := range []string{
		`id="preamble-modal"`,
		// Both forms are server-rendered, so opening the modal costs no request.
		"rewritten instructions",
		"original instructions",
		// The button says it was touched, which is what the tab and the diff
		// then explain.
		"preamble-btn touched",
		"guardrailed",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("preamble modal missing %q\n--- body ---\n%s", want, body)
		}
	}
}

func TestTranscriptShowsClassifierSignals(t *testing.T) {
	now := time.Now().UTC()
	events := []store.Event{
		{TraceID: "classified", SessionKey: "classifier-signals", Kind: "client", Provider: "p", Model: "m", StatusCode: 200,
			Ts: now, RoutingRationale: `policy router "test": domain=discovery -> provider "p"`,
			Domain: "discovery", Difficulty: "hard", CostClass: "free", Confidence: 0.91,
			RequiredCapabilities: []string{"vision", "tool_use"}},
		{TraceID: "no-match", SessionKey: "classifier-signals", Kind: "client", Provider: "p", Model: "m", StatusCode: 200,
			Ts: now.Add(time.Second), RequiredCapabilities: []string{}},
		// A classification that named only a capability, no axis. This is the
		// case the signalState enum exists to get right: reading the axes
		// alone would call it "no signals matched" and hide the capability
		// that was actually detected.
		{TraceID: "capability-only", SessionKey: "classifier-signals", Kind: "client", Provider: "p", Model: "m", StatusCode: 200,
			Ts: now.Add(2 * time.Second), RequiredCapabilities: []string{"vision"}},
		{TraceID: "unavailable", SessionKey: "classifier-signals", Kind: "client", Provider: "p", Model: "m", StatusCode: 200,
			Ts: now.Add(3 * time.Second)},
	}
	h, _ := newSeededHandler(t, events...)
	body := serve(t, h, "GET", "/admin/ui/session?key=classifier-signals", false).Body.String()
	if !strings.Contains(body, `policy router &#34;test&#34;: domain=discovery -&gt; provider &#34;p&#34;`) {
		t.Error("matched routing predicate not found in rationale")
	}
	for _, empty := range []string{`difficulty=""`, `cost_class=""`, "capabilities=[]", "request_kind=\"\""} {
		if strings.Contains(body, empty) {
			t.Errorf("rationale contains unused predicate %q", empty)
		}
	}
	if !strings.Contains(body, "classified signals") || !strings.Contains(body, "capabilities=vision, tool_use") {
		t.Error("separately rendered classified signals were not present")
	}
	if strings.Count(body, "classified; no signals matched") != 1 {
		t.Errorf("no-match label count = %d, want one (the empty case only; the capability-only turn must render its value instead)", strings.Count(body, "classified; no signals matched"))
	}
	if !strings.Contains(body, `<span class="axis">capabilities=vision</span>`) {
		t.Error("capability-only classification did not render its capability as a standalone value")
	}
}

// #79 phase 4: the effort the CLIENT requested is a request FACT and belongs
// in the "routing & model" meta-grid next to alias/model/provider — NOT in the
// classifier-verdict `axes` span (domain=/difficulty=/cost_class=), which is
// classification output only. Both halves are asserted: the value renders in
// the grid, and `effort=` never appears as an axis on the pane.
func TestTranscriptShowsClientEffortAsRequestFact(t *testing.T) {
	now := time.Now().UTC()
	events := []store.Event{
		{TraceID: "effort-high", SessionKey: "client-effort", Kind: "client", Provider: "anthropic", Model: "claude-sonnet", StatusCode: 200,
			Ts: now, AliasUsed: "claude", ClientEffort: "high"},
		{TraceID: "effort-none", SessionKey: "client-effort", Kind: "client", Provider: "anthropic", Model: "claude-sonnet", StatusCode: 200,
			Ts: now.Add(time.Second), ClientEffort: ""},
	}
	h, _ := newSeededHandler(t, events...)
	body := serve(t, h, "GET", "/admin/ui/session?key=client-effort", false).Body.String()

	if !strings.Contains(body, `<span class="k">client effort</span><span class="v">high</span>`) {
		t.Error("requested effort was not rendered in the routing & model grid")
	}
	if !strings.Contains(body, `<span class="v">(not requested)</span>`) {
		t.Error("a request that asked for no effort did not render the (not requested) state")
	}
	// The other half of the placement rule: it must not leak into the axis row.
	if strings.Contains(body, `<span class="axis">effort=`) {
		t.Error("client effort was rendered as a classifier axis; it is a request fact and belongs in the meta-grid")
	}
}

// actually ran with, not just the session opener's — and a classifier child
// exposes its own system prompt too. newestRequestMessage narrows splitBlocks
// to the request's newest message index, which would otherwise silently
// drop every system-role block along with the deduped history; buildPreamble
// reads the system prompt from a fresh, unfiltered ContentForRequest call so
// it survives that narrowing.
func TestTranscriptShowsEachRequestsOwnSystemPrompt(t *testing.T) {
	now := time.Now().UTC()
	key := "sess-sysprompt"

	events := []store.Event{
		{TraceID: "tr-1", SessionKey: key, Kind: "client",
			Provider: "anthropic", Model: "claude-sonnet", StatusCode: 200,
			Ts: now, LatencyMs: 100,
			Content: &store.CapturedContent{
				Request: []store.Block{
					{MsgIndex: 0, Position: 0, Role: "system", Kind: "text", Body: []byte("opener preamble")},
					{MsgIndex: 1, Position: 0, Role: "user", Kind: "text", Body: []byte("hi")},
				},
			}},
		// Turn 2 resends turn 1's history but carries a DIFFERENT system
		// prompt — a title/subagent-style prompt swap mid-session — plus a
		// pre-guardrail rewrite, so both the "own prompt, not the opener's"
		// and the "original vs guardrailed" claims are exercised together.
		{TraceID: "tr-2", SessionKey: key, Kind: "client",
			Provider: "anthropic", Model: "claude-sonnet", StatusCode: 200,
			Ts: now.Add(2 * time.Second), LatencyMs: 100,
			Content: &store.CapturedContent{
				Request:            []store.Block{{MsgIndex: 0, Position: 0, Role: "system", Kind: "text", Body: []byte("turn 2's own preamble")}},
				RequestGuardrailed: []store.Block{{MsgIndex: 0, Position: 0, Role: "system", Kind: "text", Body: []byte("turn 2's rewritten preamble")}},
			}},
		// A classifier call nested under turn 1, with its own system prompt —
		// distinct from both client turns'.
		{TraceID: "tr-1", SessionKey: "classifier-key", Kind: "classifier",
			Provider: "anthropic", Model: "claude-haiku", StatusCode: 200,
			Ts: now.Add(1 * time.Second), LatencyMs: 20,
			Content: &store.CapturedContent{
				Request: []store.Block{{MsgIndex: 0, Position: 0, Role: "system", Kind: "text", Body: []byte("classifier's own preamble")}},
			}},
	}

	h, _ := newSeededHandler(t, events...)
	body := serve(t, h, "GET", "/admin/ui/session?key="+key, false).Body.String()

	for _, want := range []string{
		"opener preamble",
		"turn 2&#39;s rewritten preamble", // as-sent (guardrailed) form, shown by default
		"turn 2&#39;s own preamble",       // client original form
		"classifier&#39;s own preamble",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q from a per-request system prompt\n--- body ---\n%s", want, body)
		}
	}
	// "opener preamble" appears exactly once in one render: turn 1's own
	// (hidden, moved-on-click) preamble panel, in the single-panel form the
	// no-guardrail case now uses (see transcriptInspector.html — rendering
	// AsSent and Original separately when they're byte-identical was the
	// actual page-weight regression on a long session with a large system
	// prompt, not the DB reads). The real invariant under test is that it
	// does NOT also leak into turn 2's panel — if it did, the count would
	// climb past this exact total.
	if n := strings.Count(body, "opener preamble"); n != 1 {
		t.Errorf("opener preamble appeared %d times, want exactly 1 (turn 2 must not also show turn 1's prompt)\n%s", n, body)
	}
}

// A body clips to a fixed height with a fade and an expand-link once it runs
// past the mockup's own visual threshold (900 chars for a message/reasoning
// block) — not once it runs past blockPreviewBytes (8KB), the much higher cap
// that only bounds what goes on the wire. Gating the clip on the wire cap, as
// an earlier version of this page did, meant a merely-long body (the common
// case) sat clipped inside a 220px box with no way to see the rest: "show
// full" appeared to do nothing because it never appeared at all.
func TestTranscriptLongBodyGetsShowFull(t *testing.T) {
	now := time.Now().UTC()
	key := "sess-clamp"
	// Longer than the mockup's 900-char clamp threshold, but far short of
	// blockPreviewBytes (8KB) — the gap where the old gate never fired.
	long := strings.Repeat("this is a long reasoning paragraph. ", 40)

	h, _ := newSeededHandler(t, store.Event{
		TraceID: "t1", SessionKey: key, Kind: "client",
		Provider: "p", Model: "m", StatusCode: 200, Ts: now, LatencyMs: 1,
		Content: &store.CapturedContent{
			Request:  []store.Block{{MsgIndex: 0, Position: 0, Role: "user", Kind: "text", Body: []byte(long)}},
			Response: []store.Block{{MsgIndex: 0, Position: 0, Role: "assistant", Kind: "text", Body: []byte(long)}},
		},
	})
	body := serve(t, h, "GET", "/admin/ui/session?key="+key, false).Body.String()

	if !strings.Contains(body, "clamped") {
		t.Errorf("a body past the visual clamp threshold did not get the clamped class:\n%s", body)
	}
	if !strings.Contains(body, "expand-link") {
		t.Errorf("a clamped body has no expand-link, so \"show full\" has nothing to click:\n%s", body)
	}
}

// "Show full" must actually reveal the whole body, not just the part within
// blockPreviewBytes (8KB): the div is clipped visually (CSS max-height), so
// its DOM content must be the complete text or "show full" silently caps out
// past 8KB with no way to see the rest — the real second-round "show full
// doesn't work" bug, only visible on a body long enough to cross that cap.
func TestTranscriptShowFullRevealsPastWireCap(t *testing.T) {
	now := time.Now().UTC()
	key := "sess-clamp-huge"
	// Comfortably longer than blockPreviewBytes (8KB) so a body-length gate
	// tied to that cap, not the mockup's 900-char visual threshold, would
	// truncate it before the browser ever sees the tail.
	huge := strings.Repeat("x", 20000)
	marker := "END-OF-BODY-MARKER"
	body := huge + marker

	h, _ := newSeededHandler(t, store.Event{
		TraceID: "t1", SessionKey: key, Kind: "client",
		Provider: "p", Model: "m", StatusCode: 200, Ts: now, LatencyMs: 1,
		Content: &store.CapturedContent{
			Request: []store.Block{{MsgIndex: 0, Position: 0, Role: "user", Kind: "text", Body: []byte(body)}},
		},
	})
	page := serve(t, h, "GET", "/admin/ui/session?key="+key, false).Body.String()

	if !strings.Contains(page, marker) {
		t.Errorf("a >8KB body was truncated before the marker at its end — \"show full\" can never reveal it:\n(body omitted, %d bytes)", len(page))
	}
}

// The blue treatment for internal rows must not swallow the row states that
// sit above it in the file. `.entry .row.internal` and the pre-existing
// `.entry .row:hover` / `.row.selected` / `.row.error-row` rules all carry one
// class beyond `.entry .row`, so specificity ties and layout order alone picks
// the winner — meaning a colour rule here silently overrides hover, selection,
// and the error colour.
//
// The colours are undecided for now (see the .internal block in app.css), so
// this asserts the INVARIANT rather than a token: whichever colours internal
// rows eventually get, if a base rule paints one, each state it would override
// must be re-asserted after it. That keeps the guarantee live without pinning
// a palette the design has not settled.
func TestInternalRowKeepsRowStates(t *testing.T) {
	css, err := assets.ReadFile("static/app.css")
	if err != nil {
		t.Fatalf("read embedded CSS: %v", err)
	}
	sheet := string(css)

	// The treatment must hang off the transcript's own row, never a bare class
	// name: .badge and .summary are shared with other pages, and an unscoped
	// rule would repaint rows this change was never about.
	for _, bare := range []string{"\n.badge.kind {", "\n.badge.kind\n", "\n.row.internal {"} {
		if strings.Contains(sheet, bare) {
			t.Errorf("internal-row styling is not scoped to .entry .row (%q): it would leak to other pages", bare)
		}
	}

	// Every rule targeting an internal row, in source order. A "base" rule is
	// one that is not already a state variant; those are the ones that can
	// steal a state from the plain rules above them.
	type rule struct {
		sel string
		at  int
	}
	var bases []rule
	for _, m := range regexp.MustCompile(`\.entry \.row\.internal[^{]*\{`).FindAllStringIndex(sheet, -1) {
		sel := strings.TrimSpace(sheet[m[0] : m[1]-1])
		switch {
		case strings.Contains(sel, ":hover"), strings.Contains(sel, ".selected"),
			strings.Contains(sel, ".error-row"):
			continue // a state re-assertion, not a base
		}
		bases = append(bases, rule{sel: sel, at: m[0]})
	}

	// No colour chosen yet: the class is present for the markup to hang off,
	// and the rows correctly inherit the ordinary row treatment.
	if len(bases) == 0 {
		t.Log("internal rows carry no colour rule yet — nothing to re-assert; " +
			"add the four state rules below when the palette is settled")
		return
	}

	last := bases[len(bases)-1]
	for _, state := range []string{
		".entry .row.internal:hover {",
		".entry .row.internal.selected {",
		".entry .row.internal.error-row .dot {",
		".entry .row.internal.error-row .main .summary {",
	} {
		at := strings.Index(sheet, state)
		if at < 0 {
			t.Errorf("%q paints internal rows on equal specificity to %q, overriding it; "+
				"it needs a scoped re-assertion after the base rule", last.sel, state)
			continue
		}
		if at < last.at {
			t.Errorf("%q must come after the base %q (source order decides), or the base rule wins", state, last.sel)
		}
	}
}

// A title or subagent request is an internal call the client did not send
// under its own prompt, and the list has to say so: #78 persisted the kind,
// but summarizeTurn overwrote the badge with the turn's content label
// ("reasoning"), so every internal row rendered exactly like an ordinary turn
// and the kind never reached the page at all. These are the bytes the browser
// gets, so this checks the rendered HTML, not the view model.
func TestTranscriptListBadgesInternalRequestKinds(t *testing.T) {
	now := time.Now().UTC()
	key := "sess-kinds"

	// Each internal kind gets a reasoning block, i.e. the shape that used to
	// clobber the badge — the regression is only visible with content present.
	internal := func(traceID, kind string, offset time.Duration) store.Event {
		return store.Event{
			TraceID: traceID, SessionKey: key, Kind: "client", RequestKind: kind,
			Provider: "openrouter", Model: "openrouter/auto", StatusCode: 200,
			Ts: now.Add(offset), LatencyMs: 2200,
			Content: &store.CapturedContent{
				Request:  []store.Block{{MsgIndex: 0, Position: 0, Role: "user", Kind: "text", Body: []byte("generate a title for this conversation")}},
				Response: []store.Block{{MsgIndex: 0, Position: 0, Role: "assistant", Kind: "reasoning", Body: []byte("weighing the topic against the instructions")}},
			},
		}
	}
	userTurn := store.Event{
		TraceID: "tr-user", SessionKey: key, Kind: "client",
		Provider: "anthropic", Model: "claude-sonnet", StatusCode: 200,
		Ts: now, LatencyMs: 900,
		Content: &store.CapturedContent{
			Request:  []store.Block{{MsgIndex: 0, Position: 0, Role: "user", Kind: "text", Body: []byte("what does the router do")}},
			Response: []store.Block{{MsgIndex: 0, Position: 0, Role: "assistant", Kind: "text", Body: []byte("it picks a provider")}},
		},
	}

	h, _ := newSeededHandler(t, userTurn, internal("tr-title", "title", time.Second), internal("tr-sub", "subagent", 2*time.Second))
	body := serve(t, h, "GET", "/admin/ui/session?key="+key, false).Body.String()

	// The kind reaches the list as a badge of its own, for each internal kind.
	for _, want := range []string{
		`<span class="badge kind">title</span>`,
		`<span class="badge kind">subagent</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("transcript list missing %q — an internal request's kind must be listed\n--- body ---\n%s", want, body)
		}
	}

	// The rows carry the class the blue treatment hangs off, and the ordinary
	// user turn does not — the distinction is the whole point.
	if n := strings.Count(body, `class="row internal"`); n != 2 {
		t.Errorf("got %d rows marked internal, want 2 (title + subagent); a user turn must not be:\n--- body ---\n%s", n, body)
	}
	if !strings.Contains(body, `class="row" data-id=`) {
		t.Errorf("the ordinary user turn lost its plain row class:\n--- body ---\n%s", body)
	}

	// The content label no longer replaces the kind — the exact regression.
	// Scoped to the internal rows: an ordinary turn legitimately badges its own
	// reasoning, so a page-wide Contains would fail on a correct page.
	for _, m := range regexp.MustCompile(`<div class="row internal".*?</div>`).FindAllString(body, -1) {
		if strings.Contains(m, `<span class="badge">reasoning</span>`) {
			t.Errorf("an internal row still fell back to its content label instead of its kind:\n%s", m)
		}
	}

	// An internal row keeps a content summary beside its kind, so the line
	// reads "subagent — <what it did>" rather than a bare badge.
	if !strings.Contains(body, "weighing the topic against the instructions") {
		t.Errorf("an internal row lost its content summary:\n--- body ---\n%s", body)
	}

	// The inspector header's kind chip: the LABEL is the change (it names the
	// internal call), and the class records the intent that it is a kind label
	// rather than a warning. Its colour is a follow-up — see
	// TestKindChipKeepsKindClassOnWarnColour for why it is still warn.
	for _, want := range []string{
		`<span class="chip kind">title</span>`,
		`<span class="chip kind">subagent</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("inspector header missing %q — the kind must be labelled for a client turn\n--- body ---\n%s", want, body)
		}
	}
}

// Internal rows carry no colour of their own yet: DESIGN.md has drifted from
// the intended look, so the palette is being settled before any token is
// picked. This pins the neutral state so a stray rule cannot quietly reintroduce
// a colour that then reads wrong against the rest of the UI, and so the point
// where colours land is a deliberate, visible change rather than a drift.
func TestInternalRowsCarryNoColourYet(t *testing.T) {
	css, err := assets.ReadFile("static/app.css")
	if err != nil {
		t.Fatalf("read embedded CSS: %v", err)
	}
	sheet := string(css)

	// Walk every rule whose selector targets an internal row and inspect its
	// declarations, rather than searching for particular tokens: this stays
	// honest whichever colour someone reaches for.
	for _, m := range regexp.MustCompile(`\.entry \.row\.internal[^{]*\{[^}]*\}`).FindAllString(sheet, -1) {
		brace := strings.Index(m, "{")
		sel, decls := strings.TrimSpace(m[:brace]), m[brace:]
		for _, colour := range []string{"color:", "background:", "border-color:", "box-shadow:"} {
			if strings.Contains(decls, colour) {
				t.Errorf("internal rows have a %s declaration again (%q) — the palette is\n"+
					"undecided (DESIGN.md has drifted from the intended look). Either settle\n"+
					"the token deliberately and re-assert :hover/.selected/.error-row, or\n"+
					"leave these rows inheriting the ordinary treatment.", colour, sel)
			}
		}
	}
}

// The kind chip's CLASS is what says "this is a kind label, not a warning".
// Its colour is deliberately unchanged for now — it still resolves to the warn
// token, which reads as a problem indicator on ordinary title/subagent traffic,
// but repainting it means picking a palette token, and DESIGN.md's drift means
// that choice has to be made with the rest of the colours, not ahead of it.
// This pins the class so the intent is recorded; the colour is a follow-up.
func TestKindChipKeepsKindClassOnWarnColour(t *testing.T) {
	css, err := assets.ReadFile("static/app.css")
	if err != nil {
		t.Fatalf("read embedded CSS: %v", err)
	}
	sheet := string(css)

	at := strings.Index(sheet, ".f-head .chip.kind {")
	if at < 0 {
		t.Fatal("the kind chip has no rule of its own")
	}
	rule := sheet[at : strings.Index(sheet[at:], "}")+at]
	// Unchanged on purpose. When the palette is settled, this assertion and
	// the rule change together — that pairing is the reminder.
	if !strings.Contains(rule, "var(--warn)") {
		t.Errorf("the kind chip's colour changed outside the palette decision; it should still\n"+
			"resolve to the old token until a colour is chosen deliberately, got:\n%s", rule)
	}
	if strings.Contains(sheet, ".f-head .chip.warn {") {
		t.Error("the chip rule kept a .warn selector while the markup uses .kind — the class would not apply")
	}
}
