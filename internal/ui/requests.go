package ui

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/cnf/arbiter/internal/store"
)

// defaultListLimit is the UI's own default page size. It is *below* the
// reader's cap (maxRequestListLimit, 500) deliberately: paging in 100-row
// steps means the cap is rarely reached, so the "list is capped" note is an
// exception rather than the normal case.
const defaultListLimit = 100

// requestKindFilter turns the optional ?kind= query parameter into a
// store.RequestFilter.Kind value. Absent and the literal "all" both mean no
// filter — the default view is everything, client traffic and Arbiter's own
// internal requests (classifier calls today; title-gen/subagent calls later)
// alike, since #8's nesting (see attachTraceChildren) is what makes a
// classifier row legible next to the request that spawned it rather than
// something to hide by default. Anything else is used verbatim as an exact
// match, so ?kind=client still narrows to real traffic only.
func requestKindFilter(raw string) string {
	switch raw {
	case "", "all":
		return ""
	default:
		return raw
	}
}

// requestFilterView is the filter form's state. Raw strings are kept for the
// fields the operator types, so an invalid value is echoed back into the form
// beside the error instead of being silently normalised away.
type requestFilterView struct {
	SinceRaw       string
	Provider       string
	Alias          string
	StatusRaw      string
	SessionKey     string
	SessionKeyless bool
	ErrorsOnly     bool
	LimitRaw       string

	// KindRaw is the query param exactly as given ("" for the default
	// "everything" view, "all" (the same thing spelled out), or an explicit
	// kind) — not the resolved filter value, which collapses "" and "all" to
	// the same "no filter" meaning and would make the form unable to tell
	// them apart when re-rendering which option is selected.
	KindRaw string

	// ReqKindRaw is ?request_kind= as typed: what the request IS ("title",
	// later "subagent"), which is a different question from KindRaw's who
	// sent it. Free text rather than a fixed list, because the set of kinds
	// is open — a new one is a config edit, not a code change.
	ReqKindRaw string

	// Flat is ?flat=1: render one row per request instead of collapsing runs
	// of streamed turns. It is part of the filter form's state because it is
	// part of what the reader is looking at, and the form re-renders from it.
	Flat bool

	// Any records whether any filter is set, so the empty state can offer
	// "widen" only when there is something to widen.
	Any bool
}

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
		if lines[i].Run {
			lines[i].OpenHref = flatLineHref(lines[i].Head)
		}
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
	origToOut := make(map[int]int, len(lines))               // index in `lines` -> index in `out`, client lines only
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
func attachTitleChildren(lines []requestLineView, resolveParent func(requestID int64) (sessionKey string, ok bool)) []requestLineView {
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

// flatLineHref is the flat view of one line's requests: the same list with
// grouping off, narrowed to the group's own routing facts.
//
// It filters on the parameters the list actually supports. `model` is not one of
// them, so a conversation that switched model inside one line opens slightly
// wider than the line — the alternative is a link that fetches nothing, and a
// link that over-shows while the reader can see the model column is the honest
// error of the two. The count is only ever a link target, never a claim about
// exactly what will appear.
func flatLineHref(head requestRowView) string {
	q := url.Values{}
	q.Set("flat", "1")
	if head.SessionKey != "" {
		q.Set("session", head.SessionKey)
	} else {
		// No session to narrow by: the flat list is the only view that can
		// show an unpinned row at all.
		q.Set("no_session", "1")
	}
	if head.Provider != "" {
		q.Set("provider", head.Provider)
	}
	if head.AliasUsed != "" {
		q.Set("alias", head.AliasUsed)
	}
	if head.StatusCode != 0 {
		q.Set("status", strconv.FormatInt(head.StatusCode, 10))
	}
	if head.RequestKind != "" {
		q.Set("request_kind", head.RequestKind)
	}
	return "/admin/ui/requests?" + q.Encode()
}

// flatToggleHref is the "every request" / "grouped" switch: the current filter
// set with grouping flipped, so turning it on keeps the window the reader was
// looking at. It is a plain URL for the same reason every other filter is —
// the view a reader is looking at is the address bar, so it can be reloaded,
// bookmarked and shared.
func flatToggleHref(q url.Values, flat bool) string {
	out := url.Values{}
	for k, vs := range q {
		if k == "flat" || k == "after" {
			continue
		}
		for _, v := range vs {
			out.Add(k, v)
		}
	}
	if !flat {
		out.Set("flat", "1")
	}
	if len(out) == 0 {
		return "/admin/ui/requests"
	}
	return "/admin/ui/requests?" + out.Encode()
}

// rowsView is what the request table renders. It is carried by the page and by
// the htmx fragment alike, so a swapped table and a loaded page cannot
// disagree about the rows, the pager, or the filter state.
type rowsView struct {
	// Rows is the page's rows as the store returned them, newest first, one
	// per request. It is what Flat mode renders and what the live tail's
	// template keeps its `data-id` on — a request is a row there.
	Rows []requestRowView

	// Lines is the same rows collapsed into one line per "same event happening
	// again" — see foldRequestLines. It is what the default view renders.
	Lines []requestLineView

	// Flat turns grouping off: every request gets its own row, exactly as the
	// list rendered before grouping existed. It is a real query parameter
	// (?flat=1) rather than a client-side toggle, so the view is shareable and
	// the count's own link can open the constituents in place.
	Flat bool

	// FlatHref and GroupedHref are the two halves of that switch, carrying the
	// current filters so flipping it keeps the window.
	FlatHref    string
	GroupedHref string

	// RunsOnPage counts the collapsed lines, so the page can state what it
	// folded rather than leaving the reader to wonder where rows went.
	RunsOnPage int

	More    bool
	MoreURL string
	F       requestFilterView

	// Tail is the live view's state. It rides on rowsView rather than
	// requestsView because the tail is about these rows — it appends to the table
	// the fragment renders — and keeping it here is what lets the same struct
	// serve both the page and the fragment.
	Tail tailView
}

// tailView is the live tail's initial state, rendered into data attributes.
//
// The cursor is the *newest* row already on screen, so starting the tail shows
// what arrives next rather than replaying what is already there — and because it
// comes from the rows themselves it is the stored `ts` text, which is the only
// form that compares correctly against the column.
type tailView struct {
	// Enabled is false when the store is disabled or the list is not the newest
	// page, in which case there is nothing sensible to follow.
	Enabled bool

	// Src is the tail endpoint, with the current window as a parameter. The rest
	// of the filters are passed separately so the fragment's own query string is
	// built in one place (see tailFilterQuery).
	Src string

	// Cursor is the opaque token for "everything after what you are showing".
	Cursor string

	// FilterQuery is the current filter set as a query string. It is kept opaque
	// and appended verbatim so the tail follows exactly the list it sits under.
	FilterQuery string
}

// requestsView is the full page: the table view plus the chrome.
type requestsView struct {
	viewBase
	rowsView
}

// RequestsHandler handles GET /admin/ui/requests: the request list, newest
// first, with the 7a filters as a real form.
//
// Query parameters mirror the JSON endpoint's (since, provider, session,
// alias, status, errors, limit) plus no_session and the keyset cursor
// (before_ts/before_id). A malformed value is a 400 naming the parameter, never
// a silently ignored filter: a filter that quietly returns unfiltered data
// shows wrong numbers with no indication anything was dropped.
func (h *Handler) RequestsHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	q := r.URL.Query()
	fv := requestFilterView{
		SinceRaw:   q.Get("since"),
		Provider:   q.Get("provider"),
		Alias:      q.Get("alias"),
		StatusRaw:  q.Get("status"),
		SessionKey: q.Get("session"),
		LimitRaw:   q.Get("limit"),
		KindRaw:    q.Get("kind"),
		ReqKindRaw: q.Get("request_kind"),
		Flat:       q.Has("flat"),
	}

	f := store.RequestFilter{
		Provider:       fv.Provider,
		Alias:          fv.Alias,
		SessionKey:     fv.SessionKey,
		SessionKeyless: q.Has("no_session"),
		ErrorsOnly:     q.Has("errors"),
		Limit:          defaultListLimit,
		Kind:           requestKindFilter(q.Get("kind")),
		RequestKind:    fv.ReqKindRaw,
	}
	// since: a Go duration, matching the JSON surface's own parameter so the
	// two read surfaces describe one window the same way.
	if raw := q.Get("since"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			h.fail(w, r, http.StatusBadRequest,
				`since must be a positive Go duration, e.g. "24h" or "168h"`)
			return
		}
		f.Since = time.Now().UTC().Add(-d)
	}
	if raw := q.Get("status"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 100 || n > 599 {
			h.fail(w, r, http.StatusBadRequest, "status must be an HTTP status code (100-599)")
			return
		}
		f.StatusCode = n
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			h.fail(w, r, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		f.Limit = n
	}
	// The keyset cursor, as one opaque token (see encodeCursor).
	if raw := q.Get("after"); raw != "" {
		ts, id, err := decodeCursor(raw)
		if err != nil {
			h.fail(w, r, http.StatusBadRequest, "after is not a valid cursor: "+err.Error())
			return
		}
		f.BeforeTs, f.BeforeID = ts, id
	}

	// The cursor is not part of the filter form's own state, so the form never
	// shows it; but it *is* part of "is anything filtered", because a cursor
	// means this is a later page rather than the first.
	fv.Any = f.Provider != "" || f.Alias != "" || f.SessionKey != "" || f.StatusCode != 0 ||
		f.ErrorsOnly || f.SessionKeyless || !f.Since.IsZero() || fv.KindRaw != "" || fv.ReqKindRaw != ""

	view := requestsView{viewBase: h.base("Requests"), rowsView: rowsView{F: fv}}

	// Default-list read: the page renders Lines, which fold Rows. Two readers
	// need the full five-hundred, so they keep the plain fetch:
	//
	//   - ?flat=1, which renders one row per request and would otherwise be a
	//     lie about the store ("every completed request") the moment a page
	//     needed more rows than were fetched to fill its lines;
	//   - an explicit single-kind selection (e.g. kind=classifier), where a
	//     line's count is itself the thing being studied. The default view
	//     (blank, or the equivalent "all") is a normal paged browse of mixed
	//     kinds and keeps defaultListLimit — only a narrow, specific kind
	//     widens.
	if h.reader != nil {
		if fv.Flat || (fv.KindRaw != "" && fv.KindRaw != "all") {
			f.Limit = maxRequestRows
		}
		rows, err := h.reader.ListRequests(r.Context(), f)
		if err != nil {
			h.logger.LogError(r.Context(), "error", err,
				map[string]interface{}{"phase": "admin_ui_requests"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
		for _, row := range rows {
			view.Rows = append(view.Rows, requestRowView{
				RequestRow:   row,
				ShortSession: shortSessionKey(row.SessionKey),
			})
		}
		// moreURL needs the stored rows (it reads the cursor off the last one),
		// not the view rows.
		view.MoreURL = moreURL(q, rows, f.Limit)
		view.More = view.MoreURL != ""
		view.Tail = tailFor(q, rows)
	}

	// Grouping is applied after the tail's state, not before: the tail follows
	// the *rows*, and a poll's payload is rows (see TailHandler). A grouped view
	// therefore shows half of what its live control reports until the tail grows
	// the same folding — stated in the README and in the button's own title
	// rather than left for the reader to work out.
	view.FlatHref = flatToggleHref(q, true)
	view.GroupedHref = flatToggleHref(q, false)
	// The render mode travels with the rows it describes. Leaving this unset made
	// Flat mode render the *lines* template with no lines — an empty table that
	// looked like a store with no traffic.
	view.Flat = fv.Flat
	if !fv.Flat {
		view.Lines = foldRequestLines(view.Rows)
		if h.reader != nil {
			ctx := r.Context()
			view.Lines = attachTitleChildren(view.Lines, func(id int64) (string, bool) {
				p, ok, err := h.reader.ParentSessionForTitle(ctx, id)
				if err != nil || !ok {
					return "", false
				}
				return p.SessionKey, true
			})
		}
		for _, line := range view.Lines {
			if line.Run {
				view.RunsOnPage++
			}
		}
	}

	h.render(w, r, "requests", "req-rows", view)
}

// tailFor builds the live tail's initial state from the rows on the page.
//
// It returns a disabled state when the page is not the newest one. A tail only
// makes sense on the first page: on a later page the newest row is not on screen,
// so "everything after what you are showing" would mean starting the view from
// the middle of history — the tail would then show traffic newer than a page the
// reader scrolled to, which is not what a live view means. Refusing it is the
// honest answer; the query-string cursor is what makes the page a later one, and
// it is not part of the filter form.
func tailFor(q url.Values, rows []store.RequestRow) tailView {
	src := "/admin/ui/requests/tail"
	if q.Get("after") != "" || len(rows) == 0 {
		return tailView{Src: src, FilterQuery: tailFilterQuery(q)}
	}
	ts, id, ok := store.NewestCursor(rows)
	if !ok {
		return tailView{Src: src, FilterQuery: tailFilterQuery(q)}
	}
	return tailView{
		Enabled:     true,
		Src:         src,
		Cursor:      encodeCursor(ts, id),
		FilterQuery: tailFilterQuery(q),
	}
}

// tailFilterQuery renders the filter set the tail should follow, excluding the
// paging cursor — the tail is watching the list, not a page of it — and including
// the window so the tail's `since` does not drift away from the list's.
//
// `flat` is included because it is not a filter but a *rendering mode*, and the
// tail has to follow the mode of the table it appends to: in a grouped list the
// client places a polled row into an existing line, and in a flat one it prepends
// a row. A tail that assumed the wrong mode would either duplicate a line or
// scatter rows.
func tailFilterQuery(q url.Values) string {
	out := url.Values{}
	for _, k := range []string{"since", "provider", "alias", "status", "session", "no_session", "errors", "kind", "flat"} {
		if v := q.Get(k); v != "" || (k == "errors" || k == "no_session") && q.Has(k) {
			out.Set(k, v)
		}
	}
	return out.Encode()
}

// moreURL builds the keyset continuation link: the current filter set plus the
// cursor from the last row rendered. It returns "" when the list is complete.
//
// "Complete" is decided by the page size, not by a COUNT: the reader clamps the
// limit itself, so a short page is the only signal available without a second
// query, and an empty page is the definitive end. A full page may therefore
// offer one more page that turns out to be empty — a harmless extra click,
// where the alternative (a COUNT over the same filter) costs a query on every
// page view.
func moreURL(q url.Values, rows []store.RequestRow, limit int) string {
	if len(rows) == 0 || len(rows) < limit || limit <= 0 {
		return ""
	}
	last := rows[len(rows)-1]
	if last.TsRaw == "" {
		return ""
	}
	next := url.Values{}
	for _, k := range []string{"since", "provider", "alias", "status", "session", "errors", "limit"} {
		if v := q.Get(k); v != "" || (k == "errors" && q.Has(k)) {
			next.Set(k, v)
		}
	}
	if q.Has("no_session") {
		next.Set("no_session", "1")
	}
	next.Set("after", encodeCursor(last.TsRaw, last.ID))
	return "/admin/ui/requests?" + next.Encode()
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

// detailView is the request detail page: metadata, the conversation path
// through the session, and the captured content when it was asked for.
type detailView struct {
	viewBase
	D store.RequestDetail

	// Turns is the session's requests up to and including this one, so the
	// page can show where this request sits in a conversation and link to any
	// other point in it. Empty for a request with no session key, which is not
	// a conversation.
	Turns  []store.SessionRequest
	Blocks []blockView

	// ContentLoaded distinguishes "the content fragment was requested" from
	// "this request has no captured content" — capture off and nothing
	// captured are different answers.
	ContentLoaded bool

	// ShowingAsSent is true when the client's original, pre-guardrail text is
	// being displayed instead of the default post-guardrail view (see #13).
	// The template uses it to render the toggle link's other state.
	ShowingAsSent bool

	// HasGuardrailedVariant is true when this request has a distinct
	// pre-guardrail capture at all, i.e. a pre-guardrail actually ran. When
	// false the toggle link is pointless — there is only one version of the
	// request — and the template omits it.
	HasGuardrailedVariant bool
}

// RequestHandler handles GET /admin/ui/requests/{id}.
func (h *Handler) RequestHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil || id < 1 {
		h.fail(w, r, http.StatusBadRequest, "request id must be a positive integer")
		return
	}
	view := detailView{viewBase: h.base("Requests")}

	if h.reader != nil {
		detail, ok, err := h.reader.GetRequest(r.Context(), id)
		if err != nil {
			h.logger.LogError(r.Context(), "error", err,
				map[string]interface{}{"phase": "admin_ui_request"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
		if !ok {
			h.fail(w, r, http.StatusNotFound, "no request with that id")
			return
		}
		view.D = detail
		view.Turns = h.turnsFor(r, detail)
	}
	view.Title = "request " + strconv.FormatInt(id, 10)

	h.render(w, r, "request", "request-content", view)
}

// turnsFor returns the session's requests in order, truncated at d. It gives
// the detail page a position in a conversation, which is what makes "which
// turn was this" answerable; a request with no session key has no such
// position and gets none. An error here degrades the page rather than failing
// it — the request's own metadata is already loaded and remains correct.
func (h *Handler) turnsFor(r *http.Request, d store.RequestDetail) []store.SessionRequest {
	if d.SessionKey == "" {
		return nil
	}
	rows, err := h.reader.Session(r.Context(), d.SessionKey, 500)
	if err != nil {
		h.logger.LogError(r.Context(), "warn", err,
			map[string]interface{}{"phase": "admin_ui_request_session"})
		return nil
	}
	for i, row := range rows {
		if row.ID == d.ID {
			return rows[:i+1]
		}
	}
	return nil
}

// wrapBlocks attaches the owning request id to each block.
func wrapBlocks(id int64, blocks []store.ContentBlock) []blockView {
	out := make([]blockView, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, blockView{ContentBlock: b, OwnerID: id})
	}
	return out
}

// RequestContentHandler handles GET /admin/ui/requests/{id}/content: the
// captured blocks, as an htmx fragment loaded lazily so a multi-megabyte
// prompt never delays the metadata view.
//
// It is also the "pull one point out" path: ?turn=N returns the conversation
// from its start up to turn N, so any earlier point in a session can be read
// on its own without scrolling a full transcript.
func (h *Handler) RequestContentHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil || id < 1 {
		h.fail(w, r, http.StatusBadRequest, "request id must be a positive integer")
		return
	}
	showAsSent := r.URL.Query().Get("as_sent") == "1"
	view := detailView{viewBase: h.base("Requests"), ContentLoaded: true, ShowingAsSent: showAsSent}

	if h.reader != nil {
		blocks, hasGuardrailedVariant, err := h.reader.ContentForRequest(r.Context(), id, showAsSent)
		if err != nil {
			h.logger.LogError(r.Context(), "error", err,
				map[string]interface{}{"phase": "admin_ui_request_content"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
		view.Blocks = wrapBlocks(id, blocks)
		view.ContentLoaded = true
		view.HasGuardrailedVariant = hasGuardrailedVariant
		if _, ok, err := h.reader.GetRequest(r.Context(), id); err == nil && ok {
			view.D.ID = id
		}
	}
	h.render(w, r, "request", "request-content", view)
}
