package ui

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// discoveryDefaultWindow is Discovery's own default lookback, wider than
// defaultWindow's 7 days. The page exists to answer "what does this client
// always send", and a 30-day window is far more likely to have accumulated
// enough distinct sessions to make that call than 7 — the first thing an
// operator does on this page is widen the window anyway, so start there.
const discoveryDefaultWindow = 30 * 24 * time.Hour

// discoveryDefaultMinSessions is the default minimum session spread.
//
// The JSON endpoint defaults this to 0, and for a raw query that is right. For
// the page it is not: a block repeated within one conversation is the ordinary
// shape of a multi-turn chat — every turn re-sends the system prompt — so a list
// ordered by request count at min_sessions=0 is topped by that, which is not a
// finding. The number that answers "is some client injecting this?" is the
// session count, so the page leads with it. It is a default, not a floor: 0 is
// still accepted.
const discoveryDefaultMinSessions = 2

// discoveryDefaultLimit matches the JSON endpoint's own default.
const discoveryDefaultLimit = 50

// discoveryUnseen is the display value for "no stored mark" — see
// store.DiscoveryStates' doc comment for why the store itself never holds
// this value.
const discoveryUnseen = "unseen"

// discoveryStateCycle is the order a click on the state dot advances through:
// unseen -> seen -> ignored -> unseen. Shared by the POST handler so the
// server (not client JS) is the one place this order is defined.
func discoveryNextState(current string) string {
	switch current {
	case discoveryUnseen:
		return store.DiscoverySeen
	case store.DiscoverySeen:
		return store.DiscoveryIgnored
	default: // store.DiscoveryIgnored, or an unrecognised value
		return discoveryUnseen
	}
}

// discoveryCacheTTL bounds how long a cached ledger result is served before
// the underlying query runs again.
//
// The query result is the same regardless of which caller measures it, so
// caching it (rather than caching per-response HTML) means a cache hit
// benefits both the page and a differently-parameterised repeat with the
// same params. The window is 30 days by default (discoveryDefaultWindow):
// a couple of minutes of staleness cannot meaningfully change an aggregate
// over that span, and Discovery exists to spot week-over-week patterns, not
// react to the last few seconds of traffic. Short enough that "I changed the
// filters and got stale data" is never a believable complaint; long enough
// that reloading the same view twice in a row (the common case — load,
// look, look again) is instant instead of paying the full query again.
//
// This is deliberately the simple version: a TTL, not an invalidate-on-write
// hook. New data arriving does not evict a cached entry early — it just
// isn't reflected until the entry expires. Finer-grained invalidation is
// explicitly deferred; see PICKUP.md.
const discoveryCacheTTL = 2 * time.Minute

// discoveryCacheResult is what's cached: everything RepeatedContent,
// ContentHashCounts, and SessionlessRequestCount compute for one parameter
// combination. Seen/ignored marks (DiscoveryStates) are deliberately NOT
// part of this — that query is cheap (a handful of rows keyed by hash) and
// an operator toggling a mark expects to see it stick immediately, not
// wait out a cache TTL.
type discoveryCacheResult struct {
	blocks      []store.RepeatedContent
	total       int64
	matching    int64
	sessionless int64
}

// discoveryCacheEntry pairs a result with when it was computed, so callers
// can decide for themselves whether it's still fresh enough.
type discoveryCacheEntry struct {
	result   discoveryCacheResult
	cachedAt time.Time
}

// discoveryCache is a process-local, in-memory cache of the Discovery
// ledger's expensive query, keyed by the parameters that change its result.
//
// A single mutex guards a plain map. Arbiter is a single-operator admin
// surface (see project memory: single-user deployment, one browser tab at a
// time in practice) — there is no concurrent-tenant load to shard against,
// and a plain mutex is not a bottleneck for a page loaded a few times a
// minute. It exists only in memory: a restart clears it, which is fine,
// since the first load after a restart pays the real cost once and every
// load after that is cheap again. On-disk persistence across restarts is
// deliberately out of scope for this pass — see PICKUP.md.
type discoveryCache struct {
	mu      sync.Mutex
	entries map[string]discoveryCacheEntry
}

func newDiscoveryCache() *discoveryCache {
	return &discoveryCache{entries: make(map[string]discoveryCacheEntry)}
}

// discoveryCacheKey turns the four query-shaping parameters into a cache
// key. It does not need to be a hash — the string is short and the map
// comparison is exact — so it is built directly from the values rather than
// through a digest, which would only add cost for no benefit at this size.
func discoveryCacheKey(since time.Duration, minRequests, minSessions, limit int) string {
	return fmt.Sprintf("%d|%d|%d|%d", since, minRequests, minSessions, limit)
}

// get returns the cached result for key if one exists and is still within
// discoveryCacheTTL, else ok=false. A stale entry is left in the map rather
// than deleted here — the next successful set overwrites it, and there is
// no separate eviction pass to keep the cache from growing: the key space is
// the four bounded query parameters as exposed by the UI, not user input
// with unbounded cardinality.
func (c *discoveryCache) get(key string) (discoveryCacheResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || time.Since(entry.cachedAt) > discoveryCacheTTL {
		return discoveryCacheResult{}, false
	}
	return entry.result, true
}

// set stores result under key, timestamped now.
func (c *discoveryCache) set(key string, result discoveryCacheResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = discoveryCacheEntry{result: result, cachedAt: time.Now()}
}

// repeatedBlockView is one repeated block plus the display-only handling of its
// hash. The full hash is what goes in the drill-down link; the short form is what
// a table can show without turning a column into 64 characters of hex.
type repeatedBlockView struct {
	store.RepeatedContent
	ShortHash string

	// State is "unseen" (default, no stored row), "seen", or "ignored" — see
	// store.DiscoveryStates. Unseen re-derives itself the same way an absent
	// row does: this field always carries one of the three, the view never
	// leaves it blank.
	State string
}

// discoveryView is the repeated-content page: which blocks recur, how widely, and
// the drill-down from a block to the requests containing it.
type discoveryView struct {
	viewBase

	Rows []repeatedBlockView

	SinceRaw    string
	Since       time.Duration
	MinRequests int
	MinSessions int
	Limit       int
	MinReqRaw   string
	MinSessRaw  string
	LimitRaw    string

	// HideIgnored drops ignored rows from the rendered list entirely, rather
	// than the default of dimming them in place. "Ignored" only means
	// something if it can get a pattern out of the way — see
	// DiscoveryHandler's filtering pass below, applied after state marks are
	// attached so it can key off the same .State the dim-in-place styling
	// uses.
	HideIgnored bool

	// Total/Matching report how many distinct blocks exist in the window
	// against how many pass the thresholds. A filter that removed everything
	// must read as a filter, not as an empty store.
	Total    int64
	Matching int64

	// Sessionless is how many requests in the window carry no session key. It
	// is here because min_sessions counts *distinct session keys*, so on a
	// store where most traffic is unpinned a high session threshold will match
	// very little — and without this the result looks like an absence of
	// boilerplate rather than a consequence of the filter.
	Sessionless int64

	// Capped says the list hit its limit, so a truncated list is not read as
	// the whole picture.
	Capped bool

	// Shown is how many rows are on the page, so the summary can say
	// "showing N of M matching" without a second COUNT.
	Shown int

	// Bounds is the accepted parameter range, from the store, so the page copy
	// and the 400 body cannot disagree.
	Bounds string
}

// DiscoveryHandler handles GET /admin/ui/content/repeated: the blocks that recur
// across requests, most widespread first.
//
// A bad parameter is a 400 naming the parameter, never a silent clamp — with the
// one documented exception that min_requests below 2 is refused rather than
// raised, because the operator asked for something the query cannot mean.
func (h *Handler) DiscoveryHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	q := r.URL.Query()

	view := discoveryView{
		viewBase:    h.base(r.Context(), "Discovery"),
		SinceRaw:    q.Get("since"),
		MinReqRaw:   q.Get("min_requests"),
		MinSessRaw:  q.Get("min_sessions"),
		LimitRaw:    q.Get("limit"),
		Since:       discoveryDefaultWindow,
		MinRequests: store.MinRepeatedRequests,
		MinSessions: discoveryDefaultMinSessions,
		Limit:       discoveryDefaultLimit,
		Bounds:      store.RepeatedBoundsNote(),
		HideIgnored: q.Has("hide_ignored"),
	}
	view.Title = "discovery"

	if raw := q.Get("since"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			h.fail(w, r, http.StatusBadRequest,
				`since must be a positive Go duration, e.g. "24h" or "168h"`)
			return
		}
		view.Since = d
	}
	if raw := q.Get("min_requests"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < store.MinRepeatedRequests {
			h.fail(w, r, http.StatusBadRequest,
				"min_requests must be an integer of at least "+strconv.Itoa(store.MinRepeatedRequests)+
					" — a block seen once is not repeated")
			return
		}
		view.MinRequests = n
	}
	if raw := q.Get("min_sessions"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			h.fail(w, r, http.StatusBadRequest, "min_sessions must be a non-negative integer")
			return
		}
		view.MinSessions = n
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxRepeatedLimit {
			h.fail(w, r, http.StatusBadRequest,
				"limit must be an integer from 1 to "+strconv.Itoa(store.MaxRepeatedLimit))
			return
		}
		view.Limit = n
	}

	if h.reader != nil {
		w0 := store.Window{Since: time.Now().UTC().Add(-view.Since)}

		cacheKey := discoveryCacheKey(view.Since, view.MinRequests, view.MinSessions, view.Limit)
		result, hit := h.discoveryCache.get(cacheKey)
		if !hit {
			blocks, err := h.reader.RepeatedContent(r.Context(), w0, view.MinRequests, view.MinSessions, view.Limit)
			if err != nil {
				h.logger.LogError(r.Context(), "error", err,
					map[string]interface{}{"phase": "admin_ui_discovery"})
				h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
				return
			}

			// Both are extras: the block list already loaded and is the point of
			// the page, so a failure here degrades the explanation rather than the
			// result. Each is logged, and its absence is visible as a missing line
			// rather than as a wrong number.
			total, matching, err := h.reader.ContentHashCounts(r.Context(), w0, view.MinRequests, view.MinSessions)
			if err != nil {
				h.logger.LogError(r.Context(), "warn", err,
					map[string]interface{}{"phase": "admin_ui_discovery_counts"})
			}

			sessionless, err := h.reader.SessionlessRequestCount(r.Context(), w0)
			if err != nil {
				h.logger.LogError(r.Context(), "warn", err,
					map[string]interface{}{"phase": "admin_ui_discovery_sessionless"})
			}

			result = discoveryCacheResult{
				blocks:      blocks,
				total:       total,
				matching:    matching,
				sessionless: sessionless,
			}
			h.discoveryCache.set(cacheKey, result)
		}

		for _, b := range result.blocks {
			view.Rows = append(view.Rows, repeatedBlockView{
				RepeatedContent: b,
				ShortHash:       shortHash(b.Hash),
				State:           discoveryUnseen,
			})
		}
		view.Shown = len(result.blocks)
		view.Capped = len(result.blocks) == view.Limit
		view.Total, view.Matching = result.total, result.matching
		view.Sessionless = result.sessionless

		// Attach seen/ignored marks, and re-derive "unseen" for a block whose
		// last_seen has advanced past the moment it was marked seen (see
		// schema.sql's comment on discovery_state) — a stale "seen" mark must
		// not hide a pattern that reappeared since. Deliberately not cached
		// (see discoveryCacheResult's doc comment): an operator toggling a
		// mark must see it stick on the very next load.
		if len(view.Rows) > 0 {
			hashes := make([]string, len(view.Rows))
			for i, row := range view.Rows {
				hashes[i] = row.Hash
			}
			states, err := h.reader.DiscoveryStates(r.Context(), hashes)
			if err != nil {
				h.logger.LogError(r.Context(), "warn", err,
					map[string]interface{}{"phase": "admin_ui_discovery_states"})
			}
			for i := range view.Rows {
				mark, ok := states[view.Rows[i].Hash]
				if !ok {
					continue
				}
				if mark.State == store.DiscoveryIgnored || mark.MarkedAtLastSeen >= view.Rows[i].LastSeen {
					view.Rows[i].State = mark.State
				}
				// Otherwise the block's last_seen moved past the mark: leave
				// State at discoveryUnseen, the zero-value default set above.
			}
		}

		// hide_ignored drops ignored rows from the page after state marks are
		// known — it must run after the loop above, not fold into the SQL
		// query, because "ignored" is a UI-only mark (discovery_state), not a
		// property RepeatedContent's aggregation knows about. Shown adjusts
		// with it so "N shown of M matching" still describes what is on the
		// page, not what the query returned before filtering.
		if view.HideIgnored {
			kept := view.Rows[:0]
			for _, row := range view.Rows {
				if row.State != store.DiscoveryIgnored {
					kept = append(kept, row)
				}
			}
			view.Rows = kept
			view.Shown = len(view.Rows)
		}
	}

	h.render(w, r, "discovery", "repeated-rows", view)
}

// blockRequestRowView is one row in the block drill-down: a request row plus,
// when this hash's position in this specific request is known, the link to
// its pre/post guardrail diff. GuardrailDiff needs (msg_index, position) —
// coordinates that can differ per request even for the same hash (a block
// resent at a different point in the conversation) — so this is not a
// constant computed once for the page.
type blockRequestRowView struct {
	requestRowView
	HasDiffLink bool
	DiffMsg     int64
	DiffPos     int64
}

// GuardrailDiffURL builds the on-demand diff fragment's fetch URL for this
// row. A method rather than an inline template expression for the same
// reason requestsForBlockURL is a func: the template must not hand-build a
// query string.
func (r blockRequestRowView) GuardrailDiffURL() string {
	return "/admin/ui/requests/" + strconv.FormatInt(r.ID, 10) +
		"/guardrail-diff?msg=" + strconv.FormatInt(r.DiffMsg, 10) +
		"&pos=" + strconv.FormatInt(r.DiffPos, 10)
}

// blockRequestsView is the drill-down: one block's text and the sessions that
// first sent it — one row per distinct session (the earliest request in that
// session that carries this hash), not one row per raw request. A client
// resends its whole history every turn, so listing every reference or every
// request would repeat the same session many times over; this answers "how
// many different sessions sent this, and what did each look like first".
type blockRequestsView struct {
	viewBase

	Hash      string
	ShortHash string
	Block     store.ContentBlock

	// Captured is false when the hash is known but no body was stored — the
	// block is still real and still repeated, which is why the page says so
	// rather than showing an empty box.
	Captured bool

	Rows      []blockRequestRowView
	Total     int64
	Limit     int
	Capped    bool
	Sessionle bool
}

// discoveryBlockBodyView is the workspace pane's on-demand full-body fragment
// (#50 follow-up): entering workspace mode used to show only the row's
// 200-char SQL preview (data-preview) stretched into a box the CSS still
// clamped to 220px — "full screen" that showed less text than the inline
// pane. This reuses the block drill-down's own store.ContentByHash, but
// renders the body untruncated — the user asked for workspace mode to show
// "the full text, untruncated", so blockPreviewBytes' 8KB render cap (used
// by the drill-down page and everywhere else a stored body renders) is
// deliberately skipped here.
type discoveryBlockBodyView struct {
	Hash      string
	ShortHash string
	Block     store.ContentBlock
	Captured  bool
}

// BlockRequestsHandler handles GET /admin/ui/content/block?hash=…: the
// sessions that first sent one block, most recent first.
//
// The hash is a query parameter rather than a path segment because it is hex and
// long, and a path segment would need its own escaping rules for a value that is
// already opaque.
func (h *Handler) BlockRequestsHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	q := r.URL.Query()
	hash := q.Get("hash")
	if hash == "" {
		h.fail(w, r, http.StatusBadRequest, "hash is required")
		return
	}

	view := blockRequestsView{
		viewBase:  h.base(r.Context(), "Discovery"),
		Hash:      hash,
		ShortHash: shortHash(hash),
		Limit:     store.MaxRepeatedLimit,
	}
	view.Title = "block"

	seen := discoveryDefaultWindow
	if raw := q.Get("since"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			seen = d
		}
	}

	if h.reader != nil {
		rows, err := h.reader.SessionsForContent(r.Context(), hash, view.Limit)
		if err != nil {
			// A malformed hash is the operator's input, not a server failure:
			// the store refuses it by shape, and reporting that as a 500 would
			// send them looking for a bug in the wrong place.
			if errors.Is(err, store.ErrBadContentHash) {
				h.fail(w, r, http.StatusBadRequest, err.Error())
				return
			}
			h.logger.LogError(r.Context(), "error", err,
				map[string]interface{}{"phase": "admin_ui_block_requests"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
		for _, row := range rows {
			view.Rows = append(view.Rows, blockRequestRowView{
				requestRowView: requestRowView{
					RequestRow:   row,
					ShortSession: shortSessionKey(row.SessionKey),
				},
			})
		}
		// Total is the number of sessions found (one row per session, see
		// SessionsForContent). It was declared and never set, so the page
		// announced "0 requests contain this block" above a table of five —
		// the stated count and the rendered rows must come from one value,
		// which is why this is set here and the template reads only this.
		view.Total = int64(len(rows))
		view.Capped = len(rows) == view.Limit

		// Attach each row's diff-link coordinates. An extra, like the body and
		// sessionless count below: a lookup failure here loses the "view diff"
		// link, not the request list itself.
		if len(rows) > 0 {
			ids := make([]int64, len(rows))
			for i, row := range rows {
				ids[i] = row.ID
			}
			positions, err := h.reader.PositionsForContent(r.Context(), hash, ids)
			if err != nil {
				h.logger.LogError(r.Context(), "warn", err,
					map[string]interface{}{"phase": "admin_ui_block_positions"})
			}
			for i := range view.Rows {
				if p, ok := positions[view.Rows[i].ID]; ok {
					view.Rows[i].HasDiffLink = true
					view.Rows[i].DiffMsg = p.MsgIndex
					view.Rows[i].DiffPos = p.Position
				}
			}
		}

		// The body and the sessionless count are extras; the request list is
		// the answer. A failure on either still leaves a usable page.
		if block, ok, err := h.reader.ContentByHash(r.Context(), hash); err != nil {
			if errors.Is(err, store.ErrBadContentHash) {
				h.fail(w, r, http.StatusBadRequest, err.Error())
				return
			}
			h.logger.LogError(r.Context(), "warn", err,
				map[string]interface{}{"phase": "admin_ui_block_body"})
		} else if ok {
			view.Block = block
			view.Captured = true
		}

		w0 := store.Window{Since: time.Now().UTC().Add(-seen)}
		if n, err := h.reader.SessionlessRequestCount(r.Context(), w0); err == nil {
			view.Sessionle = n > 0
		}
	}

	h.render(w, r, "block", "block-requests", view)
}

// DiscoveryBlockBodyHandler handles GET /admin/ui/content/block/body?hash=…: a
// fragment-only endpoint (there is no full-page form — workspace mode is the
// only caller) returning one block's full stored body. It exists because the
// ledger row only ever carries a 200-char SQL preview (RepeatedContent's own
// SUBSTR cap, kept small deliberately so a cross-session GROUP BY never drags
// megabyte bodies through the aggregation) — workspace mode is exactly the
// place that preview is not enough, so it fetches the real thing on entry the
// same way the drill-down page already does.
func (h *Handler) DiscoveryBlockBodyHandler(w http.ResponseWriter, r *http.Request) {
	if !fragmentsRequested(r) {
		h.fail(w, r, http.StatusBadRequest, "this endpoint only serves htmx fragment requests")
		return
	}
	if disabled := h.storeDisabled(w, r); disabled {
		return
	}

	hash := r.URL.Query().Get("hash")
	if hash == "" {
		h.fail(w, r, http.StatusBadRequest, "hash is required")
		return
	}

	view := discoveryBlockBodyView{Hash: hash, ShortHash: shortHash(hash)}

	block, ok, err := h.reader.ContentByHash(r.Context(), hash)
	if err != nil {
		if errors.Is(err, store.ErrBadContentHash) {
			h.fail(w, r, http.StatusBadRequest, err.Error())
			return
		}
		h.logger.LogError(r.Context(), "error", err,
			map[string]interface{}{"phase": "admin_ui_block_body"})
		h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	if ok {
		view.Block = block
		view.Captured = true
	}

	h.exec(w, r, h.fragments, "fragments", "discovery-block-body", view)
}

// discoveryStateURL builds the state-cycle POST target for one block. A func
// (not a method on repeatedBlockView) because it also needs LastSeen, which
// the row already carries — kept as a free function since the template calls
// it with two explicit values rather than one struct, matching
// requestsForBlockURL's own shape.
func discoveryStateURL(hash, lastSeen string) string {
	return "/admin/ui/content/repeated/state?hash=" + url.QueryEscape(hash) +
		"&last_seen=" + url.QueryEscape(lastSeen)
}

// requestsForBlockURL builds the drill-down link. It is a func rather than an
// inline template expression because the hash arrives already in hex and the
// template must not be building a query string by hand.
func requestsForBlockURL(hash string) string {
	return "/admin/ui/content/block?hash=" + url.QueryEscape(hash)
}

// shortHash is shortSessionKey's counterpart for a content hash. Both are
// delibately the same idea: an opaque 64-character handle is shown truncated and
// carried in full in links, because truncation is a display concern and a query
// built from the truncated form would match nothing.
func shortHash(hash string) string { return shortSessionKey(hash) }

// discoveryStateDotView is the state-dot fragment: one block's short hash and
// its (now-cycled) state, enough for the dot to redraw itself and for the row
// to carry the new state forward into the next click. It is not
// repeatedBlockView because the state-set endpoint has no RepeatedContent to
// hand — the dot's own markup needs only the hash and the state, not the
// preview/counts the row's markup also carries.
type discoveryStateDotView struct {
	Hash      string
	ShortHash string
	State     string

	// LastSeen carries the block's RepeatedContent.LastSeen through the
	// fragment response, unchanged from what the click sent in, so the
	// swapped-in button's own hx-post URL still has it for the *next*
	// click — the button is its own source for this value once rendered
	// once, the same way the row's data-last-seen never changes underneath
	// a state cycle.
	LastSeen string
}

// DiscoverySetStateHandler handles POST
// /admin/ui/content/repeated/state?hash=…&last_seen=…: advances one block's
// seen/ignored mark by one step (unseen -> seen -> ignored -> unseen) and
// returns the updated state-dot fragment. It is the first POST route under
// /admin/ui/ — every other route there is a read — following the same
// Gate-then-Methods("POST") convention /admin/reload already uses, so a
// fronting proxy can allow GETs under /admin/ui/ while still denying this one
// if it chooses to.
//
// last_seen is a query parameter carrying the block's current
// RepeatedContent.LastSeen (the page already has it — no extra query here) so
// a "seen" mark can be timestamped against the value the operator actually
// saw, per discovery_state's re-flag rule.
func (h *Handler) DiscoverySetStateHandler(w http.ResponseWriter, r *http.Request) {
	if disabled := h.storeDisabled(w, r); disabled && fragmentsRequested(r) {
		return
	}
	q := r.URL.Query()
	hash := q.Get("hash")
	if hash == "" {
		h.fail(w, r, http.StatusBadRequest, "hash is required")
		return
	}
	lastSeen := q.Get("last_seen")

	view := discoveryStateDotView{Hash: hash, ShortHash: shortHash(hash), State: discoveryUnseen, LastSeen: lastSeen}
	if h.reader != nil {
		states, err := h.reader.DiscoveryStates(r.Context(), []string{hash})
		if err != nil {
			if errors.Is(err, store.ErrBadContentHash) {
				h.fail(w, r, http.StatusBadRequest, err.Error())
				return
			}
			h.logger.LogError(r.Context(), "error", err,
				map[string]interface{}{"phase": "admin_ui_discovery_state_read"})
			h.fail(w, r, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
		current := discoveryUnseen
		if mark, ok := states[hash]; ok {
			current = mark.State
		}
		next := discoveryNextState(current)

		var setErr error
		if next == discoveryUnseen {
			setErr = h.reader.ClearDiscoveryState(r.Context(), hash)
		} else {
			setErr = h.reader.SetDiscoveryState(r.Context(), hash, next, lastSeen)
		}
		if setErr != nil {
			if errors.Is(setErr, store.ErrBadContentHash) || errors.Is(setErr, store.ErrBadDiscoveryState) {
				h.fail(w, r, http.StatusBadRequest, setErr.Error())
				return
			}
			h.logger.LogError(r.Context(), "error", setErr,
				map[string]interface{}{"phase": "admin_ui_discovery_state_write"})
			h.fail(w, r, http.StatusInternalServerError, "update failed: "+setErr.Error())
			return
		}
		view.State = next
	}

	h.exec(w, r, h.fragments, "fragments", "discovery-state-dot", view)
}
