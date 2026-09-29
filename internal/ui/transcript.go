package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cnf/arbiter/internal/store"
)

// transcriptPageSize is how many turns one page of the transcript list loads.
//
// It is deliberately the reader's own per-query cap (maxRequestListLimit) so a
// page can never ask for more rows than a query will return — the same rule
// the flat requests list follows with defaultListLimit, except that a
// transcript is read as a conversation rather than browsed as a feed, so the
// step is larger and each page is the whole conversation-so-far rather than a
// screenful.
const transcriptPageSize = 100

// sessionView is the session transcript page: a scrollable list of one
// conversation's turns (each with the classifier/title calls it triggered
// nested beneath it) beside a persistent inspector for whichever turn is
// selected.
//
// The page is server-rendered end to end — every turn's list row *and* its
// full inspector markup are produced here and shipped in one document, so
// selecting a turn is a class swap in the DOM rather than a fetch. The browser
// script (transcript.js) only moves the selection, filters the rendered list,
// and opens what is already on the page; the single thing fetched on demand is
// a captured body's full text, which goes through the existing
// /admin/ui/requests/{id}/content fragment (see #48) rather than a new
// endpoint.
type sessionView struct {
	viewBase

	Key      string
	ShortKey string

	// Found distinguishes "no such session" from "a session with no turns",
	// which are different answers for a reader who followed a link.
	Found bool

	// Turns are the client turns loaded so far, in conversation order, each
	// carrying its nested internal calls.
	Turns []transcriptTurnView

	// Totals is the whole conversation's headline numbers — not just the
	// loaded page's, because the header's job is to describe the session.
	Totals store.SessionTotals

	// Paging state. Loaded is how many turns are on the page; Total is the
	// conversation's true length; HasMore is Loaded < Total.
	Loaded  int
	Total   int64
	HasMore bool
	Offset  int
	Limit   int

	// MoreURL is the htmx target that appends the next page of turns. Empty
	// when there is nothing more to load.
	MoreURL string

	// Set only by a jump (?seq=N): the turn the page should open with.
	SelectedID int64

	// SeqLoadedMax is the highest turn number on the page, so the browser's
	// jump control can tell "jump to what is already here" from "that turn is
	// on a later page".
	SeqLoadedMax int
}

// transcriptTurnView is one turn as the transcript renders it.
type transcriptTurnView struct {
	store.RequestRow

	// Seq is the turn's 1-based position in the conversation — the number the
	// list shows and the jump control accepts. It is computed from the page's
	// offset, not from the store, because "turn number" is a property of the
	// conversation's order rather than of the row.
	Seq int

	// Dot is the list row's status dot class: "err" for a failed turn, "user"
	// when it introduced a user message, "ok" otherwise — the mockup's own
	// three-way rule.
	Dot string

	// Badge/Summary are the list row's one-line description of the turn.
	Badge   string
	Summary string

	// UserPreview is the first user message's opening text, for the list row.
	UserPreview string

	// Inspector is everything the detail pane shows for this turn.
	Inspector transcriptInspector

	// Children are the internal calls this turn spawned (a classifier call
	// today), each rendered as its own inspector when selected.
	Children []transcriptTurnView
}

// transcriptInspector is one turn's detail-pane content.
type transcriptInspector struct {
	// IsClassifier marks a call Arbiter made on its own behalf, which is
	// rendered with the same layout as a client turn plus a verdict section —
	// the mockup's rule, and the reason a classifier call is legible in
	// context rather than hidden.
	IsClassifier bool

	// ParentSeq/ParentID are set on a child turn, so its inspector can offer
	// "back to parent request".
	ParentSeq int
	ParentID  int64

	// Verdict is the classifier's own answer. Zero-valued for a client turn
	// except for the rationale, which every request carries.
	Domain     string
	Effort     string
	CostClass  string
	Confidence float64
	HasVerdict bool

	// Content, split the way a reader scans it rather than the way it was
	// stored: the user message(s), the assistant's reasoning, and the tool
	// calls with their results paired up.
	UserMessages []blockView
	Reasoning    []blockView
	Calls        []toolCallPairView

	// Headers is the inbound request's captured headers, nil when none were
	// recorded (an internal call never has any).
	Headers map[string]string

	// SystemPrompt is this request's own system prompt, in both forms when a
	// pre-guardrail touched it — the same shape transcriptPreamble builds for
	// the session opener (see systemPromptFor), but scoped to this request
	// rather than assuming the opener's preamble applies to every turn.
	SystemPrompt transcriptPreamble

	// CaptureOff is true when nothing was captured for this request at all —
	// rendered as its own note, because "nothing captured" and "nothing to
	// capture" are different answers and the store keeps them apart.
	CaptureOff bool

	// ContentLoaded records that a content read was attempted, so a turn whose
	// read failed is not presented as a turn with no content.
	ContentLoaded bool
	ContentErr    bool
}

// toolCallPairView is one tool_use block with the tool_result that answered it.
//
// Pairing them is what makes a call readable: the mockup renders each call as
// one block (name, its input, its result) rather than as two unrelated
// entries, and the store's tool_result body carries the for_id that makes the
// match exact rather than positional.
type toolCallPairView struct {
	Call   toolCallView
	Result *toolResultView
}

// transcriptPreamble is the session's system preamble, in both forms, for the
// page's preamble modal.
type transcriptPreamble struct {
	Present bool

	// Guardrailed is true when a pre-guardrail rewrote the preamble before it
	// went upstream — the mockup's "touched dot". AsSent and Original are
	// identical when it is false, and the modal says so rather than rendering
	// an empty diff.
	Guardrailed bool

	AsSent   string
	Original string
	Diff     []store.DiffOp
}

// SessionHandler handles GET /admin/ui/session?key=…: one conversation, as a
// turn-by-turn list beside a persistent inspector.
//
// The key is a query parameter rather than a path segment because session keys
// are opaque and may be arbitrary client-supplied values — a `/` or a `:` in
// one would break the route. It mirrors /admin/stats/session's own `?key=`.
func (h *Handler) SessionHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		h.fail(w, r, http.StatusBadRequest, "missing required query parameter: key")
		return
	}

	q := r.URL.Query()
	offset := 0
	if raw := q.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			h.fail(w, r, http.StatusBadRequest, "offset must be a non-negative integer")
			return
		}
		offset = n
	}
	limit := transcriptPageSize
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			h.fail(w, r, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = n
	}

	view := sessionView{
		viewBase: h.base(r.Context(), "Sessions"),
		Key:      key,
		ShortKey: shortSessionKey(key),
		Offset:   offset,
		Limit:    limit,
	}
	view.Title = "session " + shortSessionKey(key)

	if h.reader != nil {
		// A jump to a turn number is resolved here, before any turns are read:
		// the handler has to know how far into the conversation that turn is
		// before it can load a page containing it. Reading it as its own step
		// also means a jump is one request — "load the page turn N falls on" —
		// rather than the browser paging forward until it arrives.
		if raw := q.Get("seq"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				h.fail(w, r, http.StatusBadRequest, "seq must be a positive integer (a turn number)")
				return
			}
			id, ok, err := h.reader.SessionTurnAt(r.Context(), key, n)
			if err != nil {
				h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
				return
			}
			if !ok {
				h.fail(w, r, http.StatusNotFound,
					fmt.Sprintf("session %s has no turn #%d", shortSessionKey(key), n))
				return
			}
			offset = ((n - 1) / limit) * limit
			view.Offset = offset
			view.SelectedID = id
		} else if raw := q.Get("id"); raw != "" {
			// ?id=<request id> is how a link from outside the transcript
			// (a Sessions lane node, a satellite/child call included) names
			// its target: an id has no turn number of its own to page by, so
			// it is resolved to the turn its owning client row falls on —
			// SessionTurnForRequest does the client-vs-satellite distinction
			// laneRow.OpenHref itself doesn't need to know about. The turn
			// only decides which page to load; SelectedID stays the id that
			// was asked for, so a satellite child is what actually gets
			// selected — select() in transcript.js already matches
			// .child-row by its own data-id, not just .row.
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				h.fail(w, r, http.StatusBadRequest, "id must be an integer (a request id)")
				return
			}
			turnKey, n, ok, err := h.reader.SessionTurnForRequest(r.Context(), id)
			if err != nil {
				h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
				return
			}
			if !ok || turnKey != key {
				h.fail(w, r, http.StatusNotFound,
					fmt.Sprintf("session %s has no request #%d", shortSessionKey(key), id))
				return
			}
			offset = ((n - 1) / limit) * limit
			view.Offset = offset
			view.SelectedID = id
		}

		if err := h.fillTranscript(r, &view); err != nil {
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}

		// The fragment form exists only for the "load more" append: the page
		// itself is one server-rendered document, but a conversation longer
		// than one page grows by appending the next one rather than by
		// re-rendering everything already read. Extra pages carry turns only;
		// the toolbar and the preamble modal belong to the first page and are
		// not duplicated into an append.
		if offset > 0 {
			h.render(w, r, "session", "transcript-more", view)
			return
		}
	}

	h.render(w, r, "session", "session-turns", view)
}

// fillTranscript reads one page of a conversation and everything the page
// shows about it. Every read degrades rather than failing the page: a session
// whose content cannot be read still has correct metadata, and a reader
// following a link is better served by a partially-filled page than by a 500.
// The one read that cannot degrade is the page of turns itself — a transcript
// with no list is not a transcript — so that failure is returned for the
// caller to report.
func (h *Handler) fillTranscript(r *http.Request, view *sessionView) error {
	ctx := r.Context()

	totals, err := h.reader.SessionTotals(ctx, view.Key)
	if err != nil {
		h.logger.LogError(ctx, "warn", err,
			map[string]interface{}{"phase": "admin_ui_transcript_totals", "session": view.Key})
	}
	view.Totals = totals
	view.Total = totals.Turns

	rows, err := h.reader.SessionClientPage(ctx, view.Key, view.Limit, view.Offset)
	if err != nil {
		h.logger.LogError(ctx, "error", err,
			map[string]interface{}{"phase": "admin_ui_transcript", "session": view.Key})
		return err
	}

	turns := h.buildTurns(r, rows, view.Offset)
	view.Turns = turns
	view.Found = len(rows) > 0 || view.Offset > 0
	view.Loaded = len(rows)
	view.HasMore = int64(view.Offset+len(rows)) < totals.Turns
	if view.Loaded > 0 {
		view.SeqLoadedMax = view.Offset + view.Loaded
		if view.HasMore {
			next := url.Values{}
			next.Set("key", view.Key)
			next.Set("offset", strconv.Itoa(view.Offset+view.Loaded))
			next.Set("limit", strconv.Itoa(view.Limit))
			view.MoreURL = "/admin/ui/session?" + next.Encode()
		}
	}

	return nil
}

// buildTurns turns a page's client rows into turn views, numbering them by the
// conversation's order and attaching each turn's internal calls.
//
// Every DB read below is batched once for the whole page — content blocks,
// request extras (confidence/headers) and children — rather than once per
// turn. A transcript page used to run two queries per turn (one contentFor,
// one GetRequest) on top of the children query, which was the dominant cost
// once pages stopped being a handful of rows; see #59.
func (h *Handler) buildTurns(r *http.Request, rows []store.RequestRow, offset int) []transcriptTurnView {
	ctx := r.Context()

	traceIDs := make([]string, 0, len(rows))
	byTrace := make(map[string]int, len(rows))
	for i, row := range rows {
		if row.TraceID != "" {
			traceIDs = append(traceIDs, row.TraceID)
			byTrace[row.TraceID] = i
		}
	}

	children := make([][]store.RequestRow, len(rows))
	if kids, err := h.reader.SessionChildren(ctx, traceIDs); err != nil {
		h.logger.LogError(ctx, "warn", err,
			map[string]interface{}{"phase": "admin_ui_transcript_children"})
	} else {
		for _, kid := range kids {
			if i, ok := byTrace[kid.TraceID]; ok {
				children[i] = append(children[i], kid)
			}
		}
	}

	// One id list covering every row that will need a buildTurn call —
	// parents and children alike — so the content and extras reads below are
	// each a single query for the whole page, not one per turn.
	ids := make([]int64, 0, len(rows)*2)
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	for _, kids := range children {
		for _, kid := range kids {
			ids = append(ids, kid.ID)
		}
	}

	page := transcriptPageContent{}
	if blocks, hasGuardrailed, err := h.reader.ContentForRequests(ctx, ids); err != nil {
		h.logger.LogError(ctx, "warn", err,
			map[string]interface{}{"phase": "admin_ui_transcript_content"})
	} else {
		page.blocks = blocks
		page.hasGuardrailed = hasGuardrailed
		page.contentLoaded = true
	}
	if extras, err := h.reader.RequestExtras(ctx, ids); err != nil {
		h.logger.LogError(ctx, "warn", err,
			map[string]interface{}{"phase": "admin_ui_transcript_extras"})
	} else {
		page.extras = extras
	}

	// The parent's turn number is needed by each child's "back to parent"
	// link, so numbering happens before the children are built.
	out := make([]transcriptTurnView, 0, len(rows))

	for i, row := range rows {
		turn := h.buildTurn(r, row, offset+i+1, page)
		for _, kid := range children[i] {
			child := h.buildTurn(r, kid, 0, page)
			child.Inspector.ParentSeq = turn.Seq
			child.Inspector.ParentID = turn.ID
			turn.Children = append(turn.Children, child)
		}
		out = append(out, turn)
	}
	return out
}

// transcriptPageContent is one page's worth of per-request reads, fetched
// once in buildTurns and looked up by buildTurn — see buildTurns' doc
// comment. A row missing from blocks/extras just means that id had none
// (capture off, or no matching requests row); contentLoaded distinguishes
// "the batch read failed" from "this id legitimately has zero blocks", the
// same distinction buildTurn's own ContentErr already made per-request.
type transcriptPageContent struct {
	blocks         map[int64][]store.ContentBlock
	hasGuardrailed map[int64]bool
	extras         map[int64]store.RequestExtra
	contentLoaded  bool
}

// buildTurn derives one request's list row and inspector from data already
// read for the whole page (see transcriptPageContent) — it makes no DB calls
// of its own.
func (h *Handler) buildTurn(r *http.Request, row store.RequestRow, seq int, page transcriptPageContent) transcriptTurnView {
	ctx := r.Context()

	turn := transcriptTurnView{
		RequestRow: row,
		Seq:        seq,
		Dot:        "ok",
		Badge:      row.RequestKind,
		Inspector: transcriptInspector{
			IsClassifier: row.Kind != "client",
			Domain:       row.Domain,
			Effort:       row.Effort,
			CostClass:    row.CostClass,
		},
	}
	if turn.Badge == "" {
		turn.Badge = row.Kind
	}

	if page.contentLoaded {
		blocks := page.blocks[row.ID]
		hasGuardrailed := page.hasGuardrailed[row.ID]
		turn.Inspector.ContentLoaded = true
		turn.Inspector.CaptureOff = len(blocks) == 0
		h.splitBlocks(&turn, blocks)
		turn.Inspector.SystemPrompt = h.buildPreamble(ctx, row.ID, blocks, hasGuardrailed)
	} else {
		turn.Inspector.ContentErr = true
	}

	extra := page.extras[row.ID]
	turn.Inspector.Confidence = extra.Confidence
	turn.Inspector.Headers = extra.Headers
	turn.Inspector.HasVerdict = row.Domain != "" || row.Effort != "" ||
		row.CostClass != "" || extra.Confidence > 0
	if row.StatusCode >= 400 {
		turn.Dot = "err"
	}

	h.summarizeTurn(&turn)
	return turn
}

// splitBlocks sorts one request's captured blocks into the inspector's
// sections and derives the list row's one-line preview.
//
// Only the newest message on the request side is considered "this turn's
// content" — a stateless chat API resends the whole prior conversation with
// every request, so without this every turn after the first would show every
// earlier user message and every earlier tool call all over again (see the
// bug this fixes: two different turns rendering byte-identical user-message
// panes). newestRequestMessage narrows the request-direction blocks to the
// single highest MsgIndex before anything else runs; response-direction
// blocks need no such filter, since a response is always this turn's own
// output, never a resend.
func (h *Handler) splitBlocks(turn *transcriptTurnView, blocks []store.ContentBlock) {
	blocks = newestRequestMessage(blocks)

	// tool_use blocks are indexed by their tool id so each result can be
	// paired with the call it answered; the store records that id in the
	// result's for_id, so the match is exact rather than positional.
	pending := make(map[string]int, len(blocks))
	var calls []toolCallPairView

	for _, b := range blocks {
		if !b.Captured {
			continue
		}
		bv := blockView{ContentBlock: b, OwnerID: turn.ID}
		switch {
		case b.BlockType == "tool_use":
			v := parseToolCall(b.Body)
			calls = append(calls, toolCallPairView{Call: v})
			if v.ID != "" {
				pending[v.ID] = len(calls) - 1
			}
		case b.BlockType == "tool_result":
			v := parseToolResult(b.Body)
			if i, ok := pending[v.ForID]; ok {
				calls[i].Result = &v
				continue
			}
			// A result whose call is not on this request (a replayed
			// conversation re-sends results without the calls): render it on
			// its own rather than dropping it.
			calls = append(calls, toolCallPairView{Result: &v})
		case b.Direction == "request" && b.Role == "user":
			turn.Inspector.UserMessages = append(turn.Inspector.UserMessages, bv)
			if turn.UserPreview == "" {
				turn.UserPreview = oneLine(b.Body, 90)
			}
		case b.Direction == "response" && isReasoning(b.BlockType):
			turn.Inspector.Reasoning = append(turn.Inspector.Reasoning, bv)
		case b.Direction == "response" && b.BlockType == "text":
			turn.Inspector.Reasoning = append(turn.Inspector.Reasoning, bv)
		}
	}
	if len(turn.Inspector.UserMessages) > 0 {
		turn.Dot = "user"
	}
	turn.Inspector.Calls = calls
}

// summarizeTurn fills the list row's badge and summary: the assistant's
// reasoning if there is any, else the tool calls, else a plain note. This is
// the mockup's own precedence, and it is what makes a row scannable without
// opening it.
func (h *Handler) summarizeTurn(turn *transcriptTurnView) {
	if len(turn.Inspector.Reasoning) > 0 {
		turn.Badge = "reasoning"
		turn.Summary = oneLine(turn.Inspector.Reasoning[0].Body, 110)
		return
	}
	if len(turn.Inspector.Calls) > 0 {
		first := turn.Inspector.Calls[0]
		badge, text := summarizeCallPair(first)
		if len(turn.Inspector.Calls) > 1 {
			badge = fmt.Sprintf("%d× %s", len(turn.Inspector.Calls), badge)
		}
		turn.Badge = badge
		turn.Summary = text
		return
	}
	if turn.UserPreview != "" {
		turn.Badge = "user"
		turn.Summary = turn.UserPreview
	}
}

// summarizeCallPair renders one call's badge and one-line text for the list.
func summarizeCallPair(p toolCallPairView) (badge, text string) {
	switch {
	case p.Call.Name == "" && p.Result != nil:
		return "result", oneLine(p.Result.Content, 110)
	case p.Call.Name == "":
		return "—", "no tool call"
	}
	badge = toolBadge(p.Call.Name)
	text = p.Call.Summary
	if text == "" && p.Result != nil {
		text = oneLine(p.Result.Content, 110)
	}
	return badge, text
}

// toolBadge names a tool for the list's badge column, grouping the several
// names different clients use for the same capability. It consults the same
// classification summarizeToolInput uses (see toolKindMatchers), so the badge
// and the summary can never disagree about what a given tool name is.
func toolBadge(name string) string {
	switch classifyTool(name) {
	case toolKindTerminal:
		return "terminal"
	case toolKindSearch:
		return "search"
	case toolKindEdit:
		return "edit"
	case toolKindRead:
		return "read"
	default:
		return name
	}
}

// isReasoning reports whether a block type carries the model's own thinking,
// which the inspector shows as "assistant reasoning" rather than as output.
func isReasoning(blockType string) bool {
	return blockType == "thinking" || blockType == "reasoning"
}

// newestRequestMessage narrows a request's captured blocks to only the
// request-direction message the client actually added on this turn.
//
// A stateless chat API resends the whole prior conversation with every
// request — turn 5's request body carries turns 1 through 4 all over again,
// not just what changed. Response-direction blocks need no filtering (a
// response is always this turn's own output), but without filtering the
// request side, every turn after the first would show every earlier turn's
// user message and every earlier tool result a second time.
//
// The newest thing the client added is always the highest MsgIndex among the
// request's blocks: a plain follow-up turn appends one new user message; a
// tool round-trip appends the assistant's own text (a resend of what this
// conversation's *previous* turn already produced as its Response, and so
// not shown again here) followed by the tool result answering it — and the
// tool result lands at the higher index. Parallel tool results from one round
// share that same single index as separate blocks (same MsgIndex, distinct
// Position), so they all survive the filter together. A client that queues
// more than one genuinely new message before ever sending a request is the
// one case this does not handle — narrower than "since last turn" would be,
// but doing that instead needs a high-water mark threaded across the whole
// page, which would wrongly clip a classifier child's own low indices when
// mixed with its much-further-along parent's; keeping this per-request and
// stateless was the safer trade.
func newestRequestMessage(blocks []store.ContentBlock) []store.ContentBlock {
	newest := int64(-1)
	for _, b := range blocks {
		if b.Direction == "request" && b.MsgIndex > newest {
			newest = b.MsgIndex
		}
	}
	out := make([]store.ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		if b.Direction == "request" && b.MsgIndex != newest {
			continue
		}
		out = append(out, b)
	}
	return out
}

// oneLine collapses a body to a single trimmed line for a preview, cutting on
// a rune boundary.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
}

// buildPreamble derives one request's system prompt in both forms: the form
// that actually went upstream (asSentBlocks, which the caller already has from
// its own content read) and, only when a pre-guardrail actually produced a
// distinct capture (hasGuardrailed), the client's original — fetched with a
// second query only in that case. The common case (no guardrail touched this
// request) costs zero extra queries: AsSent and Original are identical, so
// there is nothing a second read could add.
func (h *Handler) buildPreamble(ctx context.Context, requestID int64, asSentBlocks []store.ContentBlock, hasGuardrailed bool) transcriptPreamble {
	asSent := systemText(asSentBlocks)
	if !hasGuardrailed {
		p := transcriptPreamble{AsSent: asSent, Original: asSent}
		p.Present = asSent != ""
		return p
	}

	originalBlocks, _, err := h.reader.ContentForRequest(ctx, requestID, true)
	if err != nil {
		h.logger.LogError(ctx, "warn", err,
			map[string]interface{}{"phase": "admin_ui_transcript_preamble_orig", "request_id": requestID})
		return transcriptPreamble{}
	}

	p := transcriptPreamble{
		AsSent:   asSent,
		Original: systemText(originalBlocks),
	}
	p.Present = p.AsSent != "" || p.Original != ""
	p.Guardrailed = p.AsSent != p.Original
	if p.Guardrailed {
		p.Diff = store.LineDiff(p.Original, p.AsSent)
	}
	return p
}

// systemText returns the joined text of a block set's system-role request
// blocks — a client's standing instructions, which is what a "system preamble"
// means in practice.
func systemText(blocks []store.ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Direction == "request" && b.Role == "system" && b.Captured && b.Body != "" {
			parts = append(parts, b.Body)
		}
	}
	return strings.Join(parts, "\n\n")
}
