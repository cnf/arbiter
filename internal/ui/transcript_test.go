package ui

import (
	"context"
	"fmt"
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
			Domain: "coding", Effort: "medium", Confidence: 0.8,
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
	if !strings.Contains(body, `id="preamble-btn"`) {
		t.Error("the first page is missing the preamble control")
	}

	// The appended page is the fragment form, requested by htmx.
	frag := serve(t, h, "GET", "/admin/ui/session?key="+key+"&limit=2&offset=2", true).Body.String()
	if strings.Contains(frag, `id="preamble-btn"`) || strings.Contains(frag, "toolbar") {
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
