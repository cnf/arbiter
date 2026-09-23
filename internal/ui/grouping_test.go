package ui

import (
	"strings"
	"testing"

	"github.com/cnf/arbiter/internal/store"
)

// lineRow builds a row for the folding tests. Defaults are a streamed client
// request in session "s1" served by one provider/model, so a test only states the
// field it is varying.
func lineRow(id int64, mutate func(*store.RequestRow)) requestRowView {
	r := store.RequestRow{
		ID:         id,
		Ts:         "2026-09-20 12:00:00.000000000 +0000 UTC",
		TsRaw:      "2026-09-20 12:00:00.000000000 +0000 UTC",
		SessionKey: "s1",
		Provider:   "openrouter",
		Model:      "m",
		StatusCode: 200,
		Stream:     true,
		Kind:       "client",
	}
	if mutate != nil {
		mutate(&r)
	}
	return requestRowView{RequestRow: r, ShortSession: r.SessionKey}
}

// seededTurn is one streamed client request in a named session, which is the shape
// a working conversation writes: same session, same provider/model, streamed.
func seededTurn(trace, session string) store.Event {
	return store.Event{
		TraceID:    trace,
		SessionKey: session,
		Provider:   "p",
		Model:      "m",
		StatusCode: 200,
		Stream:     true,
	}
}

// TestFoldRequestsGroupsStreamedRuns is the feature: a run of streamed turns of
// one conversation becomes one line with a count, and everything that makes a
// request a *different event* starts a line of its own.
//
// Each case below is a field the user named as a distinct event, so the assertion
// per case is "these two do NOT fold together".
func TestFoldRequestsGroupsStreamedRuns(t *testing.T) {
	tests := []struct {
		name  string
		rows  []requestRowView
		lines int
		count int // count on the first line
	}{
		{
			name:  "one request is one line",
			rows:  []requestRowView{lineRow(1, nil)},
			lines: 1,
			count: 1,
		},
		{
			name:  "a run of identical streamed turns folds to one line",
			rows:  []requestRowView{lineRow(4, nil), lineRow(3, nil), lineRow(2, nil), lineRow(1, nil)},
			lines: 1,
			count: 4,
		},
		{
			name: "a different session is a different event",
			rows: []requestRowView{
				lineRow(3, nil),
				lineRow(2, func(r *store.RequestRow) { r.SessionKey = "s2" }),
				lineRow(1, nil),
			},
			lines: 2,
			count: 2,
		},
		{
			name: "a non-200 is a different event",
			rows: []requestRowView{
				lineRow(3, nil),
				lineRow(2, func(r *store.RequestRow) { r.StatusCode = 502 }),
				lineRow(1, nil),
			},
			lines: 2,
			count: 2,
		},
		{
			name: "a destination change is a different event",
			rows: []requestRowView{
				lineRow(3, nil),
				lineRow(2, func(r *store.RequestRow) { r.Model = "other" }),
				lineRow(1, nil),
			},
			lines: 2,
			count: 2,
		},
		{
			name: "a provider change is a different event",
			rows: []requestRowView{
				lineRow(3, nil),
				lineRow(2, func(r *store.RequestRow) { r.Provider = "anthropic" }),
				lineRow(1, nil),
			},
			lines: 2,
			count: 2,
		},
		{
			name: "an alias change is a different event",
			rows: []requestRowView{
				lineRow(3, func(r *store.RequestRow) { r.AliasUsed = "cheap" }),
				lineRow(2, func(r *store.RequestRow) { r.AliasUsed = "fast" }),
			},
			lines: 2,
			count: 1,
		},
		{
			name: "a request kind change is a different event",
			rows: []requestRowView{
				lineRow(3, nil),
				lineRow(2, func(r *store.RequestRow) { r.RequestKind = "title" }),
				lineRow(1, nil),
			},
			lines: 2,
			count: 2,
		},
		{
			name: "a non-streamed request never folds",
			rows: []requestRowView{
				lineRow(3, func(r *store.RequestRow) { r.Stream = false }),
				lineRow(2, func(r *store.RequestRow) { r.Stream = false }),
				lineRow(1, func(r *store.RequestRow) { r.Stream = false }),
			},
			lines: 3,
			count: 1,
		},
		{
			name: "a mixed run folds the streamed rows only",
			rows: []requestRowView{
				lineRow(4, nil),
				lineRow(3, func(r *store.RequestRow) { r.Stream = false }),
				lineRow(2, nil),
				lineRow(1, nil),
			},
			lines: 2, // the streamed run, plus the non-streamed row on its own
			count: 3, // the three streamed rows
		},
		{
			name: "unpinned requests never fold together",
			rows: []requestRowView{
				lineRow(3, func(r *store.RequestRow) { r.SessionKey = "" }),
				lineRow(2, func(r *store.RequestRow) { r.SessionKey = "" }),
				lineRow(1, func(r *store.RequestRow) { r.SessionKey = "" }),
			},
			lines: 3,
			count: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lines := foldRequestLines(tc.rows)
			if len(lines) != tc.lines {
				t.Fatalf("folded %d rows into %d lines, want %d", len(tc.rows), len(lines), tc.lines)
			}
			if lines[0].Count != tc.count {
				t.Errorf("first line count = %d, want %d", lines[0].Count, tc.count)
			}
			// Every row must land in exactly one line: the count is a summary of
			// the list, not a filter on it, and a row silently dropped would make
			// the page disagree with the pager and the tail.
			total := 0
			for _, line := range lines {
				total += len(line.Rows)
			}
			if total != len(tc.rows) {
				t.Errorf("lines cover %d of %d rows; folding dropped traffic", total, len(tc.rows))
			}
		})
	}
}

// TestFoldRequestsGathersARunsRowsToOneLine pins the shape the user asked for:
// `A A B A A` is *not* `A(2) B A(2)`. The line gathers every member of the group
// wherever it sits on the page, so the run reads as one line with one count.
func TestFoldRequestsGathersARunsRowsToOneLine(t *testing.T) {
	rows := []requestRowView{
		lineRow(5, nil),                                                              // A
		lineRow(4, nil),                                                              // A
		lineRow(3, func(r *store.RequestRow) { r.SessionKey = "s2" }),                // B
		lineRow(2, nil),                                                              // A
		lineRow(1, nil),                                                              // A
	}
	lines := foldRequestLines(rows)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (one for each session)", len(lines))
	}
	if lines[0].Count != 4 {
		t.Errorf("the A run has count %d, want 4 (gathered across the B row)", lines[0].Count)
	}
	if lines[1].Count != 1 || lines[1].Run {
		t.Errorf("the B line should be a single non-run line; got count=%d run=%v", lines[1].Count, lines[1].Run)
	}
}

// TestFoldRequestsOnlyRunsAreCollapsedAndKeyed is the boundary between a line that
// stands for many requests and one that stands for itself.
//
// Two separate properties live here and they are easy to conflate. Run (and the
// count badge and the open link) is about the *page*: only a group of streamed
// repeats collapses into a summary line. The group key is about the *row*: a
// streamed, session-pinned request carries it even when it is alone, because a
// later poll's arrival needs to find it and join it. Gating the key on Run is the
// defect this test now guards: it left every single-row line keyless, so the first
// new turn of a conversation on screen could not join its line and was prepended
// as a duplicate.
func TestFoldRequestsOnlyRunsAreCollapsedAndKeyed(t *testing.T) {
	lines := foldRequestLines([]requestRowView{
		lineRow(3, nil),
		lineRow(2, nil),
		lineRow(1, func(r *store.RequestRow) { r.StatusCode = 500 }),
	})
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if !lines[0].Run || lines[0].OpenHref == "" {
		t.Errorf("the folded run should be Run with an open link; got run=%v href=%q", lines[0].Run, lines[0].OpenHref)
	}
	if got := tailRowKey(lines[0].Head); got == "" {
		t.Error("a streamed row of a collapsed line travels with no group key, so the tail cannot find it")
	}
	if lines[1].Run || lines[1].OpenHref != "" {
		t.Errorf("a single-row line must not be flagged as a run; got run=%v href=%q", lines[1].Run, lines[1].OpenHref)
	}
	// The single streamed row is not a run, but it is still a request that belongs
	// to a conversation: it must carry its group so a later arrival can join it.
	if got := tailRowKey(lines[1].Head); got == "" {
		t.Error("a lone streamed request travels with no group key, so a new turn of its conversation cannot join its line")
	}
	if lines[1].Attr != tailRowKey(lines[1].Head) {
		t.Errorf("the line's own markup key %q and the tail key %q differ; the client could never match the row to its line",
			lines[1].Attr, tailRowKey(lines[1].Head))
	}
}

// TestOnlyFoldableRowsCarryAKey is the other half of the boundary: a row that can
// never share a line must carry no key at all, or the client would fold an
// unrelated request into a line it does not belong to.
func TestOnlyFoldableRowsCarryAKey(t *testing.T) {
	nonStreamed := lineRow(3, func(r *store.RequestRow) { r.Stream = false })
	unpinned := lineRow(4, func(r *store.RequestRow) { r.SessionKey = "" })

	if got := tailRowKey(nonStreamed); got != "" {
		t.Errorf("a non-streamed request carries group key %q; it cannot share a line", got)
	}
	if got := tailRowKey(unpinned); got != "" {
		t.Errorf("an unpinned request carries group key %q; it has no demonstrated relationship to any line", got)
	}
}

// TestFoldRequestsDoesNotHideRowsInANonCollapsedGroup is the safety property, and
// it is the one that is easy to get wrong by rendering a group's head only: a group
// of rows that does not fold — non-streamed traffic, or unpinned rows — must still
// render every one of its rows. Folding is a summary of the list, not a filter on
// it, so the line count may never be lower than the number of distinct requests
// that are not part of a folded run.
func TestFoldRequestsDoesNotHideRowsInANonCollapsedGroup(t *testing.T) {
	// Four non-streamed requests in one session: same group identity, nothing to
	// fold, so four lines.
	rows := make([]requestRowView, 0, 4)
	for i := int64(4); i >= 1; i-- {
		rows = append(rows, lineRow(i, func(r *store.RequestRow) { r.Stream = false }))
	}
	lines := foldRequestLines(rows)
	if len(lines) != 4 {
		t.Fatalf("four non-streamed requests rendered as %d lines; folding hid traffic", len(lines))
	}
	seen := map[int64]bool{}
	for _, line := range lines {
		if seen[line.Head.ID] {
			t.Errorf("request %d rendered twice", line.Head.ID)
		}
		seen[line.Head.ID] = true
	}
}

// TestLineKeyAttrIsStableAndHidesTheSeparator. The group key contains a NUL and a
// full session hash, so it cannot ride in an attribute; the hashed form must be
// stable (the tail compares a freshly computed key against one already on screen,
// across processes) and must not contain anything needing escaping.
func TestLineKeyAttrIsStableAndHidesTheSeparator(t *testing.T) {
	a := requestLineKey(lineRow(1, nil).RequestRow)
	b := requestLineKey(lineRow(2, nil).RequestRow) // same group, different row
	if a != b {
		t.Fatalf("two rows of one group produced different keys:\n%s\n%s", a, b)
	}
	attr := lineKeyAttr(a)
	if attr != lineKeyAttr(b) {
		t.Error("the attribute form is not stable for one group")
	}
	if strings.ContainsAny(attr, `"<>& `) {
		t.Errorf("attribute form %q needs escaping", attr)
	}
	if other := lineKeyAttr(requestLineKey(lineRow(1, func(r *store.RequestRow) { r.SessionKey = "s2" }).RequestRow)); other == attr {
		t.Error("two different groups hashed to the same attribute")
	}
}

// TestGroupedPageRendersCountAndKey is the server half of the live view's contract:
// a collapsed line must carry both its count (which the client increments) and its
// group key (which the client matches on). Without the key the tail prepends a
// duplicate line for a group already on screen; without data-count it increments
// from text.
func TestGroupedPageRendersCountAndKey(t *testing.T) {
	// The same session key on all three, which is what makes them one group: a
	// fixture with three unpinned rows is three different events by definition.
	h, _ := newSeededHandler(t,
		seededTurn("a", "sess-1"),
		seededTurn("b", "sess-1"),
		seededTurn("c", "sess-1"),
	)

	body, code := getPage(t, h, "/admin/ui/requests")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(body, `data-key="`) {
		t.Error("the grouped page rendered no group key, so the live tail cannot place a row")
	}
	if !strings.Contains(body, `data-count="3"`) {
		t.Errorf("the grouped page did not render the run's count as a data attribute:\n%s", body)
	}
	if !strings.Contains(body, ">3×<") {
		t.Error("the count is not visible on the line")
	}
	// The note says what was folded, and offers the flat view.
	if !strings.Contains(body, "show every request") {
		t.Error("the grouped page does not offer the flat view")
	}
	if !strings.Contains(body, "data-tail-grouped=") {
		t.Error("the grouped page does not tell the tail which mode to place rows in")
	}
}

// TestFlatPageRendersEveryRow is the "you can still open it up" half: ?flat=1
// renders one row per request, no count, no key — and it is the same page, so the
// reader keeps their filters.
func TestFlatPageRendersEveryRow(t *testing.T) {
	h, _ := newSeededHandler(t,
		seededTurn("a", "sess-1"),
		seededTurn("b", "sess-1"),
		seededTurn("c", "sess-1"),
	)

	body, code := getPage(t, h, "/admin/ui/requests?flat=1")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if got := strings.Count(body, `data-id="`); got != 3 {
		t.Errorf("flat view rendered %d rows, want 3 (one per request)", got)
	}
	if strings.Contains(body, `data-key="`) {
		t.Error("the flat view rendered a group key; it has no lines to place rows into")
	}
	if strings.Contains(body, `data-tail-grouped=`) {
		t.Error("the flat view told the tail to place rows by group")
	}
	if !strings.Contains(body, "group streamed runs") {
		t.Error("the flat view does not offer the way back to grouping")
	}
}

// TestGroupedAndFlatProduceTheSameRequests is the invariant that makes the feature
// safe: folding is a summary, so the two views must cover exactly the same set of
// requests. A folded view that dropped one would make the page lie about the store,
// and it would be invisible — the counts would still add up.
func TestGroupedAndFlatProduceTheSameRequests(t *testing.T) {
	h, _ := newSeededHandler(t,
		seededTurn("a", "sess-1"),
		seededTurn("b", "sess-1"),
		func() store.Event { e := seededTurn("c", "sess-2"); e.Provider = "q"; e.Stream = false; return e }(),
		seededTurn("d", "sess-1"),
	)

	grouped, _ := getPage(t, h, "/admin/ui/requests")
	flat, _ := getPage(t, h, "/admin/ui/requests?flat=1")

	g, f := rowIDs(grouped), rowIDs(flat)
	if len(f) != 4 {
		t.Fatalf("flat view rendered %d requests, want 4 (the fixture)", len(f))
	}
	// The grouped view's *head* rows are a subset of the flat view's rows: every
	// request appears in flat, and the rows the grouped view shows are real ones.
	for id := range g {
		if !f[id] {
			t.Errorf("grouped view shows request %s, which the flat view does not", id)
		}
	}
	// And the counts must account for every flat row, which is the assertion that
	// folding dropped nothing. A folded line contributes its count; a line that
	// stands for one request contributes 1.
	if total := accountedRequests(grouped); total != len(f) {
		t.Errorf("grouped view accounts for %d requests but flat shows %d", total, len(f))
	}
}

// rowIDs reads the request ids out of a rendered list, one per
// data-id="N" attribute on a .reqrow row.
func rowIDs(body string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(body, `data-id="`)[1:] {
		if i := strings.Index(part, `"`); i > 0 {
			out[part[:i]] = true
		}
	}
	return out
}

// accountedRequests sums what the grouped view claims to stand for: each collapsed
// line's count, plus one for each line that stands for a single request.
//
// It walks row *segments* rather than tags, because the count badge lives inside
// the line's first cell — scanning only the opening tag finds no count at all and
// silently reports one request per line.
func accountedRequests(body string) int {
	total := 0
	for _, seg := range strings.Split(body, `data-id="`)[1:] {
		if i := strings.Index(seg, `data-count="`); i >= 0 {
			rest := seg[i+len(`data-count="`):]
			if j := strings.Index(rest, `"`); j > 0 {
				total += atoiOrZero(rest[:j])
				continue
			}
		}
		total++
	}
	return total
}

func atoiOrZero(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// classifierRow builds a classifier row for #8's trace-nesting tests. A
// classifier call is never streamed and has no session key of its own
// derivation reasons (see PICKUP's landmines), so a distinct helper is
// clearer than threading a lot of overrides through lineRow.
func classifierRow(id int64, traceID string, mutate func(*store.RequestRow)) requestRowView {
	r := store.RequestRow{
		ID:         id,
		TraceID:    traceID,
		Ts:         "2026-09-20 12:00:00.000000000 +0000 UTC",
		TsRaw:      "2026-09-20 12:00:00.000000000 +0000 UTC",
		Provider:   "openrouter-decisions",
		Model:      "~typesafe/jev-latest",
		StatusCode: 200,
		Kind:       "classifier",
	}
	if mutate != nil {
		mutate(&r)
	}
	return requestRowView{RequestRow: r}
}

// TestAttachTraceChildrenNestsUnderItsParent is #8's core case: a classifier
// call and the client request that spawned it share a trace_id, and the
// classifier must render as the client line's child rather than as an
// unrelated top-level row — regardless of which one's ts sorts first, which
// is the whole reason nesting exists (see attachTraceChildren's doc comment).
func TestAttachTraceChildrenNestsUnderItsParent(t *testing.T) {
	parent := lineRow(2, func(r *store.RequestRow) { r.TraceID = "t1" })
	child := classifierRow(1, "t1", nil)

	// Classifier finishes (and is written) after the client row here, i.e.
	// it would sort *above* its parent under plain ts DESC — the #8 bug.
	lines := foldRequestLines([]requestRowView{child, parent})
	if len(lines) != 1 {
		t.Fatalf("got %d top-level lines, want 1 (the child must nest, not stand alone)", len(lines))
	}
	if lines[0].Head.ID != parent.ID {
		t.Fatalf("top-level line is %d, want the parent (%d)", lines[0].Head.ID, parent.ID)
	}
	if len(lines[0].Children) != 1 || lines[0].Children[0].Head.ID != child.ID {
		t.Fatalf("parent's children = %+v, want exactly the classifier row (%d)", lines[0].Children, child.ID)
	}
	if !lines[0].Children[0].IsChild {
		t.Error("the nested classifier line is not marked IsChild")
	}
}

// TestAttachTraceChildrenLeavesOrphansAtTopLevel covers #19's own finding: a
// classifier row whose parent client row never landed (a rejected/failed
// request) has no trace sibling on the page. It must still render — folding
// is a summary of the list, not a filter on it — just unnested.
func TestAttachTraceChildrenLeavesOrphansAtTopLevel(t *testing.T) {
	orphan := classifierRow(1, "no-such-trace", nil)
	lines := foldRequestLines([]requestRowView{orphan})
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 (the orphaned classifier row, unnested)", len(lines))
	}
	if lines[0].Head.ID != orphan.ID || lines[0].IsChild {
		t.Errorf("orphan rendered as %+v, want the classifier row itself, not marked as a child", lines[0])
	}
}

// TestAttachTraceChildrenMatchesAnyTurnOfAFoldedRun is the folded-run edge
// case in attachTraceChildren's own doc comment: a client line can stand for
// several streamed turns, each with its own trace_id, and only the newest
// (Head) is shown. A classifier tied to an *older* folded turn must still
// find its parent line rather than being reported as an orphan.
func TestAttachTraceChildrenMatchesAnyTurnOfAFoldedRun(t *testing.T) {
	older := lineRow(1, func(r *store.RequestRow) { r.TraceID = "older-turn" })
	newer := lineRow(2, func(r *store.RequestRow) { r.TraceID = "newer-turn" })
	child := classifierRow(3, "older-turn", nil)

	lines := foldRequestLines([]requestRowView{child, newer, older})
	if len(lines) != 1 {
		t.Fatalf("got %d top-level lines, want 1 (the folded run, with the classifier nested)", len(lines))
	}
	if !lines[0].Run {
		t.Fatalf("the two client rows should have folded into one run; got Run=%v", lines[0].Run)
	}
	if len(lines[0].Children) != 1 || lines[0].Children[0].Head.ID != child.ID {
		t.Fatalf("the run's children = %+v, want the classifier tied to its older turn", lines[0].Children)
	}
}
// titleRow builds a row for the title-nesting tests — a title-gen request:
// still Kind="client" (a real HTTP call the client made), tagged
// RequestKind="title", with its own session_key distinct from the session it
// is titling (the common case: a title call rarely shares session_key with
// its parent unless the client sent a session-affinity header on both).
func titleRow(id int64, ownSession string, mutate func(*store.RequestRow)) requestRowView {
	r := store.RequestRow{
		ID:          id,
		Ts:          "2026-09-20 12:00:00.000000000 +0000 UTC",
		TsRaw:       "2026-09-20 12:00:00.000000000 +0000 UTC",
		SessionKey:  ownSession,
		Provider:    "openrouter",
		Model:       "cheap-claude",
		StatusCode:  200,
		Kind:        "client",
		RequestKind: "title",
	}
	if mutate != nil {
		mutate(&r)
	}
	return requestRowView{RequestRow: r, ShortSession: r.SessionKey}
}

// resolverFor builds an attachTitleChildren resolver from a plain map, for
// tests that don't need a real store.Reader.
func resolverFor(byRequestID map[int64]string) func(int64) (string, bool) {
	return func(id int64) (string, bool) {
		sk, ok := byRequestID[id]
		return sk, ok
	}
}

// TestAttachTitleChildrenNestsUnderResolvedParent is the feature: a title
// line nests under the client line for the session ParentSessionForTitle
// resolved to, the same visual pattern attachTraceChildren uses for a
// classifier call — but keyed on the resolver's session_key, not trace_id,
// since a title request shares no trace with the session it titles.
func TestAttachTitleChildrenNestsUnderResolvedParent(t *testing.T) {
	parent := lineRow(2, func(r *store.RequestRow) { r.SessionKey = "real-session" })
	title := titleRow(1, "title-own-session", nil)

	lines := foldRequestLines([]requestRowView{title, parent})
	lines = attachTitleChildren(lines, true, resolverFor(map[int64]string{1: "real-session"}))

	if len(lines) != 1 {
		t.Fatalf("got %d top-level lines, want 1 (the title must nest, not stand alone)", len(lines))
	}
	if lines[0].Head.ID != parent.ID {
		t.Fatalf("top-level line is %d, want the parent (%d)", lines[0].Head.ID, parent.ID)
	}
	if len(lines[0].Children) != 1 || lines[0].Children[0].Head.ID != title.ID {
		t.Fatalf("parent's children = %+v, want exactly the title row (%d)", lines[0].Children, title.ID)
	}
	if !lines[0].Children[0].IsChild {
		t.Error("the nested title line is not marked IsChild")
	}
	if lines[0].Children[0].TitleParentState != titleParentNested {
		t.Errorf("nested title's TitleParentState = %v, want titleParentNested (zero value — Children/IsChild already say it nested)", lines[0].Children[0].TitleParentState)
	}
}

// TestAttachTitleChildrenLeavesUnresolvedAtTopLevel covers ok=false from the
// resolver with capture_content on — neither tier of ParentSessionForTitle
// found anything, including tier 2, which could actually run. The title
// still renders, same as an orphaned classifier call: folding is a summary
// of the list, not a filter on it. TitleParentState must say the join
// genuinely searched and missed, not that it couldn't run.
func TestAttachTitleChildrenLeavesUnresolvedAtTopLevel(t *testing.T) {
	title := titleRow(1, "lonely-session", nil)
	lines := foldRequestLines([]requestRowView{title})
	lines = attachTitleChildren(lines, true, resolverFor(nil))

	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 (the unresolved title row, unnested)", len(lines))
	}
	if lines[0].Head.ID != title.ID || lines[0].IsChild {
		t.Errorf("unresolved title rendered as %+v, want the title row itself, not marked as a child", lines[0])
	}
	if lines[0].TitleParentState != titleParentNotFound {
		t.Errorf("TitleParentState = %v, want titleParentNotFound (capture was on, tier 2 ran and missed)", lines[0].TitleParentState)
	}
}

// TestAttachTitleChildrenLeavesUnresolvedCaptureOff covers the same ok=false
// case, but with capture_content off: tier 2 (content-hash) could not run at
// all, so this is an expected gap, not a failed search, and the state must
// say so distinctly from TestAttachTitleChildrenLeavesUnresolvedAtTopLevel.
// This is piece 3 of #11.
func TestAttachTitleChildrenLeavesUnresolvedCaptureOff(t *testing.T) {
	title := titleRow(1, "lonely-session", nil)
	lines := foldRequestLines([]requestRowView{title})
	lines = attachTitleChildren(lines, false, resolverFor(nil))

	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 (the unresolved title row, unnested)", len(lines))
	}
	if lines[0].Head.ID != title.ID || lines[0].IsChild {
		t.Errorf("unresolved title rendered as %+v, want the title row itself, not marked as a child", lines[0])
	}
	if lines[0].TitleParentState != titleParentNotFoundCaptureOff {
		t.Errorf("TitleParentState = %v, want titleParentNotFoundCaptureOff (tier 2 could not run)", lines[0].TitleParentState)
	}
}

// TestAttachTitleChildrenLeavesUnplaceableAtTopLevel covers a resolved
// parent session_key with no line on this page (the parent session's
// requests aren't in the current window/filter) — the title must stay
// top-level rather than being silently dropped, and the state must say this
// is a paging fact, not a capture-config one, regardless of capture_content.
func TestAttachTitleChildrenLeavesUnplaceableAtTopLevel(t *testing.T) {
	title := titleRow(1, "title-own-session", nil)
	lines := foldRequestLines([]requestRowView{title})
	lines = attachTitleChildren(lines, false, resolverFor(map[int64]string{1: "session-not-on-page"}))

	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 (the unplaceable title row, unnested)", len(lines))
	}
	if lines[0].Head.ID != title.ID || lines[0].IsChild {
		t.Errorf("unplaceable title rendered as %+v, want the title row itself, not marked as a child", lines[0])
	}
	if lines[0].TitleParentState != titleParentFound {
		t.Errorf("TitleParentState = %v, want titleParentFound (resolved, just not on this page) even with capture off", lines[0].TitleParentState)
	}
}

// TestAttachTitleChildrenDoesNotNestUnderAnotherTitle guards the self-
// collision case at the rendering layer too: bySession is only built from
// non-title client lines, so a title line can never become another title
// line's parent even if they happen to share a session_key.
func TestAttachTitleChildrenDoesNotNestUnderAnotherTitle(t *testing.T) {
	title1 := titleRow(1, "shared-key", nil)
	title2 := titleRow(2, "shared-key", nil)

	lines := foldRequestLines([]requestRowView{title2, title1})
	lines = attachTitleChildren(lines, true, resolverFor(map[int64]string{2: "shared-key"}))

	if len(lines) != 2 {
		t.Fatalf("got %d top-level lines, want 2 (titles must not nest under each other)", len(lines))
	}
	for _, l := range lines {
		if l.IsChild {
			t.Errorf("line %d marked IsChild, want no title nested under another title", l.Head.ID)
		}
	}
}

// TestAttachTitleChildrenComposesWithTraceChildren checks the two nesting
// passes stack rather than clobber each other: a run-of-the-mill client
// request can have both a classifier child (trace_id) and a title child
// (resolved session) on the same page, and both must end up under it.
func TestAttachTitleChildrenComposesWithTraceChildren(t *testing.T) {
	parent := lineRow(3, func(r *store.RequestRow) { r.SessionKey = "real-session"; r.TraceID = "t1" })
	classifier := classifierRow(2, "t1", nil)
	title := titleRow(1, "title-own-session", nil)

	lines := foldRequestLines([]requestRowView{title, classifier, parent})
	lines = attachTitleChildren(lines, true, resolverFor(map[int64]string{1: "real-session"}))

	if len(lines) != 1 {
		t.Fatalf("got %d top-level lines, want 1", len(lines))
	}
	if len(lines[0].Children) != 2 {
		t.Fatalf("parent's children = %+v, want both the classifier and the title", lines[0].Children)
	}
}
