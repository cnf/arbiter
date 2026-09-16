package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// A session key is opaque and can be 64 hex characters, so it is shown
// truncated while the full value does the linking. The truncation must be
// display-only: a shortened key in an href would fetch the wrong session.
func TestSessionKeyTruncatedForDisplayOnly(t *testing.T) {
	full := strings.Repeat("a1b2c3d4", 8) // 64 chars, the content-derived shape
	if got := shortSessionKey(full); len(got) != sessionKeyDisplayLen {
		t.Errorf("shortSessionKey(%d chars) = %d chars, want %d", len(full), len(got), sessionKeyDisplayLen)
	}
	// Short and empty keys pass through rather than being padded or cut.
	for _, key := range []string{"", "short", strings.Repeat("x", sessionKeyDisplayLen)} {
		if got := shortSessionKey(key); got != key {
			t.Errorf("shortSessionKey(%q) = %q, want it unchanged", key, got)
		}
	}

	h, _ := newSeededHandler(t,
		store.Event{TraceID: "t", Provider: whichProviderA, Model: "m", StatusCode: 200,
			LatencyMs: 1, SessionKey: full},
		store.Event{TraceID: "t2", Provider: whichProviderA, Model: "m", StatusCode: 200,
			LatencyMs: 1, Ts: timeAt(), SessionKey: full},
	)

	index := serve(t, h, "GET", "/admin/ui/sessions", false).Body.String()
	if !strings.Contains(index, shortSessionKey(full)) {
		t.Error("the index does not show the shortened key")
	}
	// The visible text is shortened; every href and tooltip that carries the key
	// carries all of it, because a truncated key fetches the wrong session.
	if strings.Contains(index, ">"+full+"<") {
		t.Error("the index renders the full key as visible text; it should be shortened")
	}
	if !strings.Contains(index, "/admin/ui/session?key="+full) {
		t.Error("the index link does not carry the full session key")
	}
	if !strings.Contains(index, `title="`+full+`"`) {
		t.Error("the full key is not available as a tooltip")
	}

	detail := serve(t, h, "GET", "/admin/ui/session?key="+full, false).Body.String()
	if !strings.Contains(detail, full) {
		t.Error("the transcript page does not carry the full key")
	}
	if !strings.Contains(detail, shortSessionKey(full)) {
		t.Error("the transcript page does not show the shortened key")
	}
}

// A conversation sent twice appears as one session with two turns, and its
// transcript reads in order with each turn linked to its own request — which is
// what makes a single point in a conversation reachable.
func TestSessionIndexAndTranscript(t *testing.T) {
	key := "conv-abc"
	base := timeAt()
	h, _ := newSeededHandler(t,
		store.Event{TraceID: "t1", Provider: "alpha", Model: "m1", StatusCode: 200,
			LatencyMs: 5, Ts: base, SessionKey: key,
			Content: &store.CapturedContent{Request: []store.Block{
				{MsgIndex: 0, Position: 0, Role: "user", Kind: "text", Body: []byte("first question")}}}},
		store.Event{TraceID: "t2", Provider: "beta", Model: "m2", StatusCode: 200,
			LatencyMs: 7, Ts: base.Add(time.Minute), SessionKey: key,
			Content: &store.CapturedContent{
				Request: []store.Block{
					{MsgIndex: 0, Position: 0, Role: "user", Kind: "text", Body: []byte("first question")},
					{MsgIndex: 1, Position: 0, Role: "user", Kind: "text", Body: []byte("second question")}},
				Response: []store.Block{
					{MsgIndex: 0, Position: 0, Role: "assistant", Kind: "text", Body: []byte("the answer")}},
			}},
		// An unrelated conversation, and one request with no key at all.
		store.Event{TraceID: "t3", Provider: "alpha", Model: "m1", StatusCode: 200,
			LatencyMs: 1, Ts: base.Add(2 * time.Minute), SessionKey: "other-conv"},
		store.Event{TraceID: "t4", Provider: "alpha", Model: "m1", StatusCode: 200,
			LatencyMs: 1, Ts: base.Add(3 * time.Minute)},
	)

	index := serve(t, h, "GET", "/admin/ui/sessions", false).Body.String()
	if strings.Count(index, "/admin/ui/session?key="+key) != 1 {
		t.Errorf("the conversation does not appear exactly once on the index")
	}
	if !strings.Contains(index, `class="num">2</td>`) {
		t.Errorf("the turn count is not rendered; body = %s", firstLine(index))
	}
	// Both providers that served it are listed.
	if !strings.Contains(index, "alpha") || !strings.Contains(index, "beta") {
		t.Error("the index does not list every provider the conversation used")
	}
	// The sessionless request is not a conversation, so it is counted
	// separately rather than folded into the list.
	if !strings.Contains(index, "with no session key") {
		t.Error("the index does not report the sessionless requests")
	}
	if strings.Contains(index, "other-conv") == false {
		t.Error("the second conversation is missing from the index")
	}

	transcript := serve(t, h, "GET", "/admin/ui/session?key="+key, false).Body.String()
	if !strings.Contains(transcript, "2 turns") {
		t.Errorf("the transcript does not report its turn count; body = %s", firstLine(transcript))
	}
	// Turn by turn: each turn's blocks are together, and every turn links to its
	// own request so a point in the conversation can be pulled out.
	for _, want := range []string{"second question", "the answer", "/admin/ui/requests/2"} {
		if !strings.Contains(transcript, want) {
			t.Errorf("the transcript is missing %q", want)
		}
	}
	// Turn order: the second turn's marker must come after the first's.
	if strings.Index(transcript, `id="turn-1"`) > strings.Index(transcript, `id="turn-2"`) {
		t.Error("turns are not rendered in order")
	}
	// The unrelated conversation's content must not leak into this transcript.
	if strings.Contains(transcript, "other-conv") {
		t.Error("content from another session leaked into the transcript")
	}
}

// A conversation re-sends its whole context every turn, so the transcript must
// show what each turn *introduced* and collapse what it merely repeated —
// otherwise the page is a multiple of the conversation rather than a record of
// it. The split is by content hash, so it is exact.
func TestTranscriptSeparatesNewContentFromReplay(t *testing.T) {
	key := "growing"
	base := timeAt()

	// One block reused verbatim across all three turns (a client preamble), one
	// system block, and a fresh user turn each time.
	sysBlock := store.Block{MsgIndex: 0, Position: 0, Role: "system", Kind: "text",
		Body: []byte("you are a helpful assistant, always answer briefly")}
	first := store.Block{MsgIndex: 1, Position: 0, Role: "user", Kind: "text",
		Body: []byte("first question please")}
	second := store.Block{MsgIndex: 2, Position: 0, Role: "user", Kind: "text",
		Body: []byte("second question please")}
	reply := func(s string) store.Block {
		return store.Block{MsgIndex: 0, Position: 0, Role: "assistant", Kind: "text", Body: []byte(s)}
	}

	h, _ := newSeededHandler(t,
		// Turn 1: system + first user turn, and a reply.
		store.Event{TraceID: "t1", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1,
			Ts: base, SessionKey: key,
			Content: &store.CapturedContent{Request: []store.Block{sysBlock, first}, Response: []store.Block{reply("answer one")}}},
		// Turn 2: the same system block plus turn 1 replayed, and a new user turn.
		store.Event{TraceID: "t2", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1,
			Ts: base.Add(time.Minute), SessionKey: key,
			Content: &store.CapturedContent{Request: []store.Block{sysBlock, first,
				{MsgIndex: 2, Position: 0, Role: "assistant", Kind: "text", Body: []byte("answer one")},
				second}, Response: []store.Block{reply("answer two")}}},
		// Turn 3: everything again, one more user turn.
		store.Event{TraceID: "t3", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1,
			Ts: base.Add(2 * time.Minute), SessionKey: key,
			Content: &store.CapturedContent{Request: []store.Block{sysBlock, first,
				{MsgIndex: 2, Position: 0, Role: "assistant", Kind: "text", Body: []byte("answer one")},
				second,
				{MsgIndex: 3, Position: 0, Role: "assistant", Kind: "text", Body: []byte("answer two")},
				{MsgIndex: 4, Position: 0, Role: "user", Kind: "text", Body: []byte("third question please")}},
				Response: []store.Block{reply("answer three")}}},
	)

	body := serve(t, h, "GET", "/admin/ui/session?key="+key, false).Body.String()

	// The shared system block is rendered exactly ONCE in the whole page — not
	// once per turn behind a collapsed <details>, which would ship the same
	// bytes to the browser and leave the page just as large.
	preCount := 0
	for _, m := range regexp.MustCompile(`(?s)<pre>(.*?)</pre>`).FindAllStringSubmatch(body, -1) {
		if strings.Contains(m[1], "you are a helpful assistant") {
			preCount++
		}
	}
	if preCount != 1 {
		t.Errorf("the shared system block appears in %d <pre> blocks; want exactly 1", preCount)
	}
	if strings.Count(body, "you are a helpful assistant") != 1 {
		t.Errorf("the shared system block appears %d times in the HTML at all; want 1",
			strings.Count(body, "you are a helpful assistant"))
	}
	// The same for a replayed conversation turn.
	if n := strings.Count(body, "answer one"); n != 1 {
		t.Errorf("a replayed reply appears %d times; want only where it was introduced", n)
	}

	// Every turn's own new question and answer are rendered at full size.
	for _, want := range []string{"first question please", "second question please", "third question please",
		"answer one", "answer two", "answer three"} {
		found := false
		for _, m := range regexp.MustCompile(`(?s)<pre>(.*?)</pre>`).FindAllStringSubmatch(body, -1) {
			if strings.Contains(m[1], want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q is not rendered at full size anywhere", want)
		}
	}

	// Replay is NOT announced per turn: the text is already on the page at the
	// turn that introduced it, so a pointer at every turn is noise.
	if strings.Contains(body, "see turn #") {
		t.Error("replay pointers are rendered; they are redundant noise")
	}
	if strings.Contains(body, `class="replayref"`) {
		t.Error("the replay pointer markup is still rendered")
	}
	// But the turn header still reports how much it replayed.
	if !strings.Contains(body, "replayed") {
		t.Error("no turn header reports how much it replayed")
	}
	// The preamble is its own collapsible field.
	if !strings.Contains(body, `class="preamble"`) {
		t.Error("the system preamble is not its own collapsible field")
	}
	// The header reports how much was replayed for that turn.
	if !strings.Contains(body, "replayed") {
		t.Error("no turn reports how much it replayed")
	}

	// Turn 1 introduces everything it sends, so its header reports no replay.
	firstTurn := body[strings.Index(body, `id="turn-1"`):strings.Index(body, `id="turn-2"`)]
	if strings.Contains(firstTurn, "replayed") {
		t.Error("the opening turn reports replay, but it introduced everything it sent")
	}
	if !strings.Contains(firstTurn, `class="preamble"`) {
		t.Error("the opening turn does not show the system preamble it introduced")
	}
}

// One turn can contribute BOTH its preamble and some conversation history to a
// later turn's replay. Those must stay separate groups: merging them mislabels
// the history as preamble and undercounts neither while misdescribing both.
func TestReplayKeepsPreambleAndHistoryApart(t *testing.T) {
	key := "both-kinds"
	base := timeAt()
	sys := store.Block{MsgIndex: 0, Position: 0, Role: "system", Kind: "text", Body: []byte("THE PREAMBLE TEXT")}
	q1 := store.Block{MsgIndex: 1, Position: 0, Role: "user", Kind: "text", Body: []byte("question one")}
	a1 := store.Block{MsgIndex: 2, Position: 0, Role: "assistant", Kind: "text", Body: []byte("answer one")}
	q2 := store.Block{MsgIndex: 3, Position: 0, Role: "user", Kind: "text", Body: []byte("question two")}

	h, _ := newSeededHandler(t,
		// Turn 1 introduces the preamble and the first exchange.
		store.Event{TraceID: "t1", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1,
			Ts: base, SessionKey: key,
			Content: &store.CapturedContent{Request: []store.Block{sys, q1},
				Response: []store.Block{a1}}},
		// Turn 2 re-sends all of it and adds one question.
		store.Event{TraceID: "t2", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1,
			Ts: base.Add(time.Minute), SessionKey: key,
			Content: &store.CapturedContent{Request: []store.Block{sys, q1, a1, q2}}},
	)

	body := serve(t, h, "GET", "/admin/ui/session?key="+key, false).Body.String()
	turn2 := body[strings.Index(body, `id="turn-2"`):]

	// The preamble belongs to the turn that introduced it, so the second turn
	// does not re-show it...
	if strings.Contains(turn2, `class="preamble"`) {
		t.Error("turn 2 re-shows the system preamble; it belongs to the turn that introduced it")
	}
	// ...but it *did* re-send it, so the count includes it: preamble + question
	// one + answer one. The count is a statement about what went over the wire,
	// which is why it covers everything re-sent and not only the history.
	want := len("THE PREAMBLE TEXT") + len("question one") + len("answer one")
	if !strings.Contains(turn2, charsText(want)+" replayed") {
		t.Errorf("turn 2's replay count is not %s; got %s", charsText(want), firstLine(turn2))
	}
	// The content this turn *introduced* must not be counted as replayed.
	if strings.Contains(turn2, charsText(want+len("question two"))+" replayed") {
		t.Error("turn 2's replay count includes the question it introduced")
	}
}

// charsText renders a character count the way the template does, so a test can
// assert on what a reader sees rather than on a number it computed itself.
func charsText(n int) string { return fmtChars(n) }

// A turn introducing a block that an *earlier* turn also had must not be
// mistaken for introducing it: only the first appearance counts as new.
func TestReplaySplitGoesByFirstAppearance(t *testing.T) {
	key := "reordered"
	base := timeAt()
	shared := store.Block{MsgIndex: 0, Position: 0, Role: "user", Kind: "text", Body: []byte("the shared question")}

	h, _ := newSeededHandler(t,
		store.Event{TraceID: "t1", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1,
			Ts: base, SessionKey: key,
			Content: &store.CapturedContent{Request: []store.Block{shared}}},
		store.Event{TraceID: "t2", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1,
			Ts: base.Add(time.Minute), SessionKey: key,
			Content: &store.CapturedContent{Request: []store.Block{shared}}},
	)
	body := serve(t, h, "GET", "/admin/ui/session?key="+key, false).Body.String()

	// Turn 1 shows it; turn 2 collapses it.
	turn1 := body[strings.Index(body, `id="turn-1"`):strings.Index(body, `id="turn-2"`)]
	turn2 := body[strings.Index(body, `id="turn-2"`):]
	if strings.Contains(turn1, "replayed") {
		t.Error("the first appearance was counted as replay")
	}
	if !strings.Contains(turn2, "replayed") {
		t.Error("the second appearance was not counted as replay; the split ignores history")
	}
	// The text itself renders once, at its origin, and is not repeated.
	if strings.Count(body, "the shared question") != 1 {
		t.Errorf("the shared block appears %d times; want once, at its origin turn",
			strings.Count(body, "the shared question"))
	}
}

// An unknown key is an empty transcript, not an error: a link from an old
// request may point at a key whose requests have aged out of the store.
func TestUnknownSessionKeyIsEmptyNotError(t *testing.T) {
	h, _ := newSeededHandler(t, store.Event{TraceID: "t", Provider: "p", Model: "m",
		StatusCode: 200, LatencyMs: 1})
	rec := serve(t, h, "GET", "/admin/ui/session?key=nope", false)
	if rec.Code != 200 {
		t.Errorf("unknown key = %d, want 200 with an empty transcript", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No turns for that session key") {
		t.Errorf("the empty transcript does not explain itself; body = %s", firstLine(rec.Body.String()))
	}
	// A missing key is a malformed request, not an empty result.
	if got := serve(t, h, "GET", "/admin/ui/session", false).Code; got != 400 {
		t.Errorf("missing key = %d, want 400", got)
	}
}

// A turn whose content was never captured says so rather than rendering as an
// empty turn, and a turn with hash-only blocks still shows the block.
func TestTranscriptTurnWithoutContentExplainsItself(t *testing.T) {
	h, _ := newSeededHandler(t, store.Event{TraceID: "t", Provider: "p", Model: "m",
		StatusCode: 200, LatencyMs: 1, SessionKey: "nocontent"})
	body := serve(t, h, "GET", "/admin/ui/session?key=nocontent", false).Body.String()
	if !strings.Contains(body, "No content stored for this turn") {
		t.Errorf("a turn with no content does not explain itself; body = %s", firstLine(body))
	}
}

// The transcript's per-turn links and the request detail page must agree on the
// handle, or "pull one point out" lands somewhere else.
func TestTranscriptLinksResolveToTurnRequests(t *testing.T) {
	key := "linked"
	base := timeAt()
	h, _ := newSeededHandler(t,
		store.Event{TraceID: "t1", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1,
			Ts: base, SessionKey: key},
		store.Event{TraceID: "t2", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1,
			Ts: base.Add(time.Second), SessionKey: key},
	)
	transcript := serve(t, h, "GET", "/admin/ui/session?key="+key, false).Body.String()
	for _, id := range []string{"/admin/ui/requests/1", "/admin/ui/requests/2"} {
		if !strings.Contains(transcript, id) {
			t.Fatalf("the transcript does not link to %s", id)
		}
		// Follow it: the link must resolve, not 404.
		if got := serve(t, h, "GET", id, false).Code; got != 200 {
			t.Errorf("%s = %d, want 200", id, got)
		}
	}
}
