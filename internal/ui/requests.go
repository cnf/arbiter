// The request view-model layer: the row/line/fold/tree helpers that turn
// store.RequestRow into something a page can render.
//
// The standalone requests *page* was deleted in #54's rip-out — the merged
// Sessions page (#52) replaced it, and the newui rebuild is greenfield. The
// old flat-list live tail (TailHandler, live.js) followed it into the rip-out
// once the Sessions page grew its own tail (#52/laneLive.js) — see #56/#57.
// What survives here is the view-model machinery Sessions and Discovery still
// consume: foldRequestLines/attachTraceChildren (Sessions' lane timelines),
// requestRowView/RoutingChain (Sessions, Discovery's drill-down), the keyset
// cursor codec, and the guardrail-diff fragment (the session transcript).
// Deleting the file wholesale to "finish the rip-out" would break all four.
package ui

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"

	"github.com/cnf/arbiter/internal/store"
)

// maxRequestRows is the page size for the two readers that need every row the
// store will give them: Flat mode, and a kind selection where the grouping is
// the subject rather than the lens.
//
// It is the reader's own cap (maxRequestListLimit, 500) rather than a new
// number, so the UI cannot ask for more than a query will return and silently
// render a short list. Below it sits defaultListLimit (100) — the grouped
// default, where folding means a hundred rows still fill a screen with distinct
// lines.
const maxRequestRows = 500

// requestRowView pairs a stored row with its display-only short session key.
//
// The shortening is a separate field rather than a transformation of SessionKey,
// because the full value still has to reach the filter link and the session
// link: a truncated key in an href fetches the wrong conversation.
type requestRowView struct {
	store.RequestRow
	ShortSession string
}

// RoutingChain collapses a request's routing facts into the fewest segments
// that carry information, per DESIGN.md's "Request rows" spec:
//
//	literal: anthropic claude-opus-4-6                                (one segment, no divergence)
//	debug: openrouter preset/bugspray → google/gemma-4-26b-a4b-it:free (two, upstream diverged)
//
// The qualifier is the alias name the client asked for (AliasUsed), or the
// word "literal" when req.Model was used as-is — AliasUsed/Model are the two
// fields the store actually records per request (see store.Event's own
// comment: "alias_used TEXT, -- NULL if req.Model was literal"). A named
// routing-rule/policy label is not a separate stored field today — the
// closest per-request fact is RoutingRationale's free text — so this does
// not attempt to reproduce DESIGN.md's "debug"-style rule-name qualifier;
// "literal" / the alias name are what the two stored fields actually give.
//
// The second segment (ActualModel) appears only when the upstream reported a
// model other than the one routed to — never a redundant "X → X".
//
// This lives on requestRowView (not a free function taking store.RequestRow)
// so the template can call it as a zero-arg method: {{.Head.RoutingChain}}.
func (r requestRowView) RoutingChain() []string {
	qualifier := "literal"
	if r.AliasUsed != "" {
		qualifier = r.AliasUsed
	}
	first := strings.TrimSpace(qualifier + ": " + strings.TrimSpace(r.Provider+" "+r.Model))
	if r.ActualModel == "" || r.ActualModel == r.Model {
		return []string{first}
	}
	return []string{first, r.ActualModel}
}

// requestLineView is one displayed line: either a single request, or a run of
// requests that are the *same event happening again* collapsed into one line
// with a count.
//
// This exists because a streamed conversation writes one row per turn, so a
// working session fills the list with near-identical rows: 100 rows on a page
// was measured as 3 distinct things, and the reader saw none of the events for
// the repeats. The rows are all still there — Flat mode and the count's link
// show every one of them — so this is a summary of the list, not a filter on it.
type requestLineView struct {
	// Key identifies the group. It is not rendered; it exists so the line's
	// constituents can be described ("rows 1-74") and so a later live-tail
	// update can find the line it belongs to instead of appending beside it.
	Key string

	// Head is the newest row of the group, which is the one whose routing,
	// provider and timestamp the line shows.
	Head requestRowView

	// Count is how many requests this line stands for. It counts the rows
	// *loaded on this page* — see the note in the template and the README.
	Count int

	// Rows is every constituent, newest first, for the expanded view. It is
	// only rendered in Flat mode; the line itself renders Head.
	Rows []requestRowView

	// Run is true when this line stands for more than one request and is
	// therefore a collapsed line rather than an ordinary request row.
	Run bool

	// Children holds non-client rows (classifier calls today) tied to this
	// line by trace_id — see attachTraceChildren. They render immediately
	// beneath their parent regardless of `ts`, which is the whole point:
	// the classifier that serves a request routinely *finishes* before its
	// parent (a fast child call inside a slower still-running request), and
	// sorting by finish time alone put it above the row that caused it. A
	// child is rendered, never re-sorted into the top-level list, so the
	// list's own newest-first order (a separate, unrelated axis — see #8)
	// is undisturbed by this.
	Children []requestLineView

	// IsChild marks a line rendered inside another line's Children. It only
	// changes markup (indentation, a quieter row style); it is not a
	// grouping identity and the tail does not need to know about it, since
	// the live tail does not yet nest arrivals under their parent (#8's
	// current scope is a static-page concern, not a live one).
	IsChild bool

	// Attr is Key reduced to a short, attribute-safe identifier, so the live
	// tail can find the line a newly-arrived request belongs to instead of
	// appending a second line beside it. A group key contains a NUL separator
	// and a full session hash, so the raw form cannot go in an attribute.
	Attr string

	// OpenHref is the flat, filtered view of exactly this line's requests:
	// the "you can still open it up" affordance. It is a real URL, not a DOM
	// toggle, because the page's filters are the address bar.
	OpenHref string

	// TitleParentState says why a request_kind="title" line is or is not
	// nested under a parent — see attachTitleChildren and titleParentState.
	// Empty for every other line: the whole question ("why isn't this
	// nested") only makes sense for a title line, and Children/IsChild alone
	// already say everything a classifier line needs to say.
	TitleParentState titleParentState
}

// titleParentState is the three-way answer a title line needs and a
// classifier line does not: a classifier's trace_id link is exact and always
// resolvable when its parent is on the page, so "not nested" only ever means
// "orphan, parent not on this page". A title line's link is inferred, and
// the inference itself can fail in a way that is not a data gap — capture
// being off is a config state, not a missing fact — so the reader needs to
// know which of the three happened rather than seeing an unnested title line
// and assuming the feature is broken.
type titleParentState int

const (
	// titleParentNested means attachTitleChildren placed this line under a
	// parent; TitleParentState is not rendered in this case (Children/IsChild
	// on the parent already show it).
	titleParentNested titleParentState = iota

	// titleParentFound means ParentSessionForTitle resolved a session, but
	// that session has no line on the current page (a different filter
	// window, or paged out) — a data-availability gap, not a capture gap.
	titleParentFound

	// titleParentNotFoundCaptureOff means neither tier of
	// ParentSessionForTitle matched, and storage.capture_content is off —
	// tier 2 (content-hash) could not have run, so this is expected, not a
	// failure of the join.
	titleParentNotFoundCaptureOff

	// titleParentNotFound means neither tier matched even though capture is
	// on — tier 2 genuinely searched and found nothing (e.g. the client
	// wrapped/expanded the text before the real send, so no verbatim block
	// reappears — see PICKUP.md §17).
	titleParentNotFound
)

// Note is the human-readable explanation for TitleParentState, rendered next
// to an unnested title line so a reader sees *why* rather than assuming the
// feature silently failed. Empty for titleParentNested — Children/IsChild on
// the parent already say everything in that case.
func (s titleParentState) Note() string {
	switch s {
	case titleParentFound:
		return "parent session not on this page"
	case titleParentNotFoundCaptureOff:
		return "no parent found — capture_content is off, so only the session-id link could be tried"
	case titleParentNotFound:
		return "no parent found"
	default:
		return ""
	}
}

// lineKeySeparator splits the parts of a group key. A NUL cannot occur in any
// part (they are ids, enum-ish words and identifier text), so no two different
// groups can collide on a joined key.
const lineKeySeparator = "\x00"

// noSessionLinePrefix keeps unpinned requests from collapsing into one another.
//
// A request with no session key is not in a conversation, so "the same event
// happening again" is not a claim that can be made about two of them: an
// unpinned row and another unpinned row have no demonstrated relationship. They
// therefore never join a run, and the row's own id is what makes its key unique.
const noSessionLinePrefix = "nosession:"

// requestLineKey is the identity that decides whether two requests are "the same
// event happening again".
//
// Six parts, and each earns its place because changing any of them is a fact the
// reader wants to see rather than a repeat to fold away:
//
//   - the session, or the row's own id when it has none (see above) — rows from
//     two different conversations are never the same event, whatever else they
//     share. This is the part that matters most: without it a burst of one row
//     per session (an upstream outage) would collapse into a single line and
//     hide exactly the spread of damage the reader needs to see.
//   - provider and model: a session that switched model mid-conversation is
//     showing a re-route, which is the thing the list exists to reveal.
//   - alias_used: naming an alias and naming a literal model are different
//     routing facts even when both end at the same upstream model.
//   - status_code: a 502 among 200s is the single most important row on the
//     page and must not be folded into the successes around it.
//   - request_kind: a title-generation call is not the conversation's turn.
//
// Stream is part of the key too, so that a non-streamed request can never join a
// streamed run — the collapse is offered for streamed turns only (see isRun).
//
// What is deliberately *not* here is a time window. The gap between two turns of
// a conversation is a tuning knob with no correct value (measured counts climb
// smoothly with it), and leaving it out means the whole feature has no threshold
// to mis-set.
func requestLineKey(r store.RequestRow) string {
	session := r.SessionKey
	if session == "" {
		return noSessionLinePrefix + strconv.FormatInt(r.ID, 10)
	}
	return strings.Join([]string{
		session,
		r.Provider,
		r.Model,
		r.AliasUsed,
		strconv.FormatInt(r.StatusCode, 10),
		r.RequestKind,
		strconv.FormatBool(r.Stream),
	}, lineKeySeparator)
}

// lineKeyAttr reduces a group key to a short, stable, attribute-safe identifier.
//
// A hash rather than the key itself: the key carries a NUL separator (which
// cannot appear in an HTML attribute or survive an escaping round trip, the same
// trap the tail's timestamp cursor has) and a 64-character session hash, which
// would bloat every row. A truncated sha256 is stable across processes, so the
// client can compare a polled row's key against the lines on screen.
func lineKeyAttr(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6])
}

// lineAttr is the group identity a row's markup carries, or "" when the row must
// stand on its own.
//
// It is the *row's* answer, not the folded line's, and that is the load-bearing
// part: a line's Run flag says "this batch held more than one row of this group",
// which is false for a single-row line — but such a row must still carry its key,
// because it is the row a later poll's arrival will need to join. Gating the
// attribute on Run made every single-row line keyless, so the first new turn of
// any conversation on screen prepended a duplicate line instead of bumping the
// one already there.
//
// Only a streamed, session-pinned request can ever share a line (see
// foldRequestLines), so only those carry a key. An unpinned or non-streamed row
// gets none and can never be folded into a line it does not belong to.
func lineAttr(row requestRowView) string {
	if !row.Stream || row.SessionKey == "" {
		return ""
	}
	return lineKeyAttr(requestLineKey(row.RequestRow))
}

// foldRequestLines collapses the page's rows into lines: one per group, in the
// order each group's *newest* row appears.
//
// A group becomes a collapsed line only when it repeats something that should be
// folded away, which is a narrow condition on purpose:
//
//   - it must hold more than one row (nothing to collapse otherwise), and
//   - every row in it must be a streamed request. This is the user's own rule —
//     "just the streams" — and it is why stream is in the key: a non-streamed
//     request cannot end up inside a streamed run, and a group of non-streamed
//     requests keeps one row per request, exactly as the list renders today.
//
// Grouping is done over the rows of the page, so the count is "how many rows on
// this page", not the conversation's true total. That is a limit of folding a
// page rather than querying the store, and the template says so rather than
// letting the number read as a total (the same rule the capped-list note
// follows).
func foldRequestLines(rows []requestRowView) []requestLineView {
	lines := make([]requestLineView, 0, len(rows))
	index := make(map[string]int, len(rows))

	for _, row := range rows {
		key := requestLineKey(row.RequestRow)
		i, seen := index[key]
		if !seen {
			index[key] = len(lines)
			lines = append(lines, requestLineView{
				Key:  key,
				Attr: lineAttr(row),
				Head: row,
				Rows: []requestRowView{row},
			})
			continue
		}
		lines[i].Rows = append(lines[i].Rows, row)
	}

	for i := range lines {
		lines[i].Count = len(lines[i].Rows)
		lines[i].Run = lines[i].Count > 1 && allStreamed(lines[i].Rows)
	}

	// A group that does not fold expands back into one line per row, in page
	// order.
	//
	// This is not a formatting detail. A non-collapsed group of several rows is a
	// group by *identity* only — a non-streamed request, or one unpinned row — and
	// rendering just its head would hide every other row in it, which is the exact
	// failure this whole feature exists to undo. "Not a run" therefore has to mean
	// "one line per request", which is what the list rendered before grouping
	// existed.
	out := make([]requestLineView, 0, len(lines))
	for _, line := range lines {
		if line.Run {
			out = append(out, line)
			continue
		}
		for _, row := range line.Rows {
			out = append(out, requestLineView{
				Key:  line.Key,
				Attr: lineAttr(row),
				Head: row,
				Rows: []requestRowView{row},
				// Count stays 1 and Run stays false: one line, one request.
				Count: 1,
			})
		}
	}
	return attachTraceChildren(out)
}

// attachTraceChildren nests each non-client line (a classifier call today)
// under the client line sharing its trace_id, so a request and the one
// classifier call `trace_id` ties to it (#19 confirmed this is always 1:1 —
// every trace with a classifier call holds exactly one) render as one visual
// unit instead of two unrelated-looking rows.
//
// This exists because ordering alone does not fix legibility (#8): a
// classifier call frequently *finishes* before the request that spawned it —
// it is a fast detour inside a slower still-running request — so `ts DESC`
// can put the child above or below its own cause depending on timing.
// Nesting sidesteps the sort question entirely: a child always renders under
// its parent, wherever the parent sits in the list. The top-level list order
// (#8's derived-arrival-vs-finish-time question) is untouched here; this only
// changes what happens once a row and its cause are both on the page.
//
// A client *line* can be a folded run of several streamed turns (see
// foldRequestLines), and only its Head's row is shown — but each turn folded
// into it is its own request with its own trace_id, and a classifier can
// belong to any of them, not just the newest. So the match is keyed on every
// row inside every client line, not just Head, or a classifier tied to an
// older turn of a folded run would show up as unmatched.
//
// A non-client line with no client sibling on this page (its parent fell off
// the page, or never got a client row at all — #19 found 65 such traces,
// consistent with a rejected/failed request whose client row was never
// written) stays at the top level, in its original position, rather than
// being dropped or moved: this is a summary of the list, not a filter on it,
// matching foldRequestLines's own rule.
func attachTraceChildren(lines []requestLineView) []requestLineView {
	byTrace := make(map[string]int, len(lines)) // trace_id -> line's own index in `lines`
	for i, line := range lines {
		if line.Head.Kind != "client" {
			continue
		}
		for _, row := range line.Rows {
			if row.TraceID != "" {
				byTrace[row.TraceID] = i
			}
		}
	}

	children := make(map[int][]requestLineView, len(lines)) // parent's index in `lines` -> its children
	origToOut := make(map[int]int, len(lines))              // index in `lines` -> index in `out`, client lines only
	out := make([]requestLineView, 0, len(lines))
	for i, line := range lines {
		if line.Head.Kind == "client" {
			origToOut[i] = len(out)
			out = append(out, line)
			continue
		}
		if parent, ok := byTrace[line.Head.TraceID]; ok && line.Head.TraceID != "" {
			line.IsChild = true
			children[parent] = append(children[parent], line)
			continue
		}
		// No client sibling on this page: keep it where it was.
		origToOut[i] = len(out)
		out = append(out, line)
	}

	for parent, kids := range children {
		out[origToOut[parent]].Children = kids
	}
	return out
}

// attachTitleChildren nests a request_kind="title" line under a line sharing
// its resolved parent session, the same rendering pattern attachTraceChildren
// uses for a classifier call (#8) — but it cannot reuse that function's
// trace_id match: a title-gen request is its own top-level HTTP call with no
// trace_id in common with the session it titles. The link instead comes from
// resolveParent (store.Reader.ParentSessionForTitle), called once per title
// line on the page.
//
// Only titles the page can actually place are nested: a resolved parent
// session_key with no line on this page keeps the title line top-level,
// exactly like attachTraceChildren's orphaned-classifier case — folding is a
// summary of the list, not a filter on it, so an unplaceable title still
// renders, just unnested.
//
// This shares the classifier nesting's known display quirk with streamed
// runs (grouping/stream-collapsing) — tracked separately, not addressed
// here; see PICKUP.md.
func attachTitleChildren(lines []requestLineView, captureContent bool, resolveParent func(requestID int64) (sessionKey string, ok bool)) []requestLineView {
	bySession := make(map[string]int, len(lines)) // session_key -> line's own index in `lines`, non-title client lines only
	for i, line := range lines {
		if line.Head.Kind != "client" || line.Head.RequestKind == "title" {
			continue
		}
		if line.Head.SessionKey == "" {
			continue
		}
		if _, ok := bySession[line.Head.SessionKey]; !ok {
			bySession[line.Head.SessionKey] = i
		}
	}

	children := make(map[int][]requestLineView, len(lines))
	origToOut := make(map[int]int, len(lines))
	out := make([]requestLineView, 0, len(lines))
	for i, line := range lines {
		if line.Head.Kind != "client" || line.Head.RequestKind != "title" {
			origToOut[i] = len(out)
			out = append(out, line)
			continue
		}
		sk, ok := resolveParent(line.Head.ID)
		if ok && sk != "" {
			if parent, ok := bySession[sk]; ok {
				line.IsChild = true
				children[parent] = append(children[parent], line)
				continue
			}
			// Resolved to a real session, just not one with a line on this
			// page (a different filter window, or paged out) — a page-
			// scoping fact, not a capture-config one.
			line.TitleParentState = titleParentFound
		} else if captureContent {
			// Both tiers ran and neither matched — tier 2 genuinely searched.
			line.TitleParentState = titleParentNotFound
		} else {
			// Tier 1 (session_key) found nothing, and tier 2 (content-hash)
			// could not run at all with capture off — an expected gap, not
			// a failed join.
			line.TitleParentState = titleParentNotFoundCaptureOff
		}
		// No resolvable parent, or its session has no line on this page: keep
		// it where it was.
		origToOut[i] = len(out)
		out = append(out, line)
	}

	for parent, kids := range children {
		out[origToOut[parent]].Children = append(out[origToOut[parent]].Children, kids...)
	}
	return out
}

// allStreamed reports whether every row of a group is a streamed request, which
// is what makes the group a candidate for collapsing.
func allStreamed(rows []requestRowView) bool {
	for _, row := range rows {
		if !row.Stream {
			return false
		}
	}
	return true
}

// blockView pairs a captured block with the request it belongs to, so the
// block partial can link back to its own full body. The store's ContentBlock
// is deliberately owner-agnostic (a rejected request's content has no request
// id), so the owner is supplied by whoever knows it.
type blockView struct {
	store.ContentBlock
	OwnerID int64
}

// cursorSeparator splits the two halves of a cursor payload. It cannot occur in
// either half: a timestamp is digits and punctuation, an id is digits.
const cursorSeparator = "\x00"

// encodeCursor packs the keyset position into one opaque, URL-safe token.
//
// It is opaque deliberately. The honest cursor is the row's stored timestamp
// text, which is `2026-09-16 11:59:39.812343302 +0000 UTC` — a value whose `+`
// characters a query string is entitled to read as spaces, and a client or
// proxy that does so produces a bound matching no row at all. That failure is
// silent: the page renders "no results" rather than an error, which is exactly
// the shape this codebase refuses elsewhere. Base64 keeps the payload exact and
// URL-safe, and hides the store's internal time format from the address bar.
func encodeCursor(ts string, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(ts + cursorSeparator + strconv.FormatInt(id, 10)))
}

// decodeCursor reverses encodeCursor. A malformed cursor is an error the caller
// reports as a 400, never a silently ignored page position.
func decodeCursor(raw string) (string, int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", 0, fmt.Errorf("not base64")
	}
	parts := strings.SplitN(string(b), cursorSeparator, 2)
	if len(parts) != 2 || parts[0] == "" {
		return "", 0, fmt.Errorf("malformed payload")
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id < 1 {
		return "", 0, fmt.Errorf("malformed id")
	}
	return parts[0], id, nil
}

// guardrailDiffView is the diff fragment: one block's before/after text,
// rendered as a unified line diff. Requested on demand (see #48's "guardrail
// touch" chip) rather than computed eagerly for every block — almost every
// request has at most one touched block (the system preamble), so this is cheap
// even though the mechanism itself is general.
type guardrailDiffView struct {
	OK  bool
	Ops []store.DiffOp
}

// GuardrailDiffHandler handles GET
// /admin/ui/requests/{id}/guardrail-diff?msg=N&pos=M: the line-level diff
// between what the client sent and what actually went upstream for one
// block. Msg/pos are query parameters, not part of the block itself, because
// this is reached from a trigger element that only carries the block's
// position — the same reasoning as the hash-based /content?hash= link.
func (h *Handler) GuardrailDiffHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil || id < 1 {
		h.fail(w, r, http.StatusBadRequest, "request id must be a positive integer")
		return
	}
	msgIndex, err1 := strconv.ParseInt(r.URL.Query().Get("msg"), 10, 64)
	position, err2 := strconv.ParseInt(r.URL.Query().Get("pos"), 10, 64)
	if err1 != nil || err2 != nil {
		h.fail(w, r, http.StatusBadRequest, "msg and pos must be integers")
		return
	}

	view := guardrailDiffView{}
	if h.reader != nil {
		before, after, ok, err := h.reader.GuardrailDiff(r.Context(), id, msgIndex, position)
		if err != nil {
			h.logger.LogError(r.Context(), "error", err,
				map[string]interface{}{"phase": "admin_ui_guardrail_diff"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
		view.OK = ok
		if ok {
			view.Ops = store.LineDiff(before, after)
		}
	}
	// Always a fragment: this is reached only from a "guardrail touch" chip's
	// htmx fetch, never a page a reader navigates to directly, so there is no
	// full-page form to fall back to (unlike render's page/fragment split).
	h.exec(w, r, h.fragments, "fragments", "guardrail-diff", view)
}
