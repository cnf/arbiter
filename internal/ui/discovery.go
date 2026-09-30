// The Discovery page: finding the content blocks a client sends over and over,
// and drilling into where they land. Split across files by concern:
//
//	discovery.go        the window defaults, the page view types, and
//	                    DiscoveryHandler (the page itself)
//	discoveryblock.go   the per-block drill-down: its rows, handler, and body
//	discoverycache.go   the short-lived result cache both pages share
//	discoverystate.go   the unseen/seen state cycle and its set-state endpoint
package ui

import (
	"net/http"
	"net/url"
	"strconv"
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
const discoveryDefaultMinSessions = 5

// discoveryDefaultLimit matches the JSON endpoint's own default. Raised from
// 50 to 100: a first page this size clears most operators' actual pattern
// count without ever touching "load more", while pagination
// (Offset/HasMore/MoreURL below) makes every block past it reachable too —
// previously nothing past the hardcoded fetch limit could ever be reached
// from this page, in any filter combination, because the limit applied to
// the ranked query itself (sessions DESC) before any state filtering ran,
// with no offset to page past it.
const discoveryDefaultLimit = 100

// discoveryUnseen is the display value for "no stored mark" — see
// store.DiscoveryStates' doc comment for why the store itself never holds
// this value.
const discoveryUnseen = "unseen"

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
	//
	// Defaults to true (see DiscoveryHandler): an ignored block is, by
	// definition, boilerplate the operator already dismissed, so it should
	// not keep occupying a row on every future visit. The checkbox is then
	// an opt-IN to seeing them again, not an opt-out of hiding them.
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

	// Shown is how many rows are on the page — every block loaded so far
	// (Offset's worth already on screen, plus this page's Loaded), so the
	// summary can say "showing N of M matching" without a second COUNT.
	Shown int

	// Offset/Loaded/HasMore/MoreURL are pagination state, the same
	// convention transcript.go's sessionView uses for "load more": Offset
	// is how many rows earlier pages already fetched, Loaded is how many
	// this response added, HasMore says whether MoreURL (the next page's
	// htmx target) is worth rendering. This is what makes a block ranked
	// below the first page's Limit still reachable — before pagination
	// existed, anything past the hardcoded fetch limit was invisible on
	// this page in every filter combination, because the limit truncated
	// the ranked query itself with no way to ask for what came after it.
	// Replaces the old Capped/Bounds "hit the ceiling, widen your filters"
	// note: there is no longer a ceiling a filter needs to work around.
	Offset  int
	Loaded  int
	HasMore bool
	MoreURL string
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
		// Defaults to true — see discoveryView.HideIgnored's doc comment.
		// The checkbox is inverted from the field name: it reads
		// "show ignored" and is checked to opt IN to seeing them, so an
		// unchecked box (which, like any HTML checkbox, submits nothing)
		// correctly falls through to the hide-by-default here.
		HideIgnored: !q.Has("show_ignored"),
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
	// offset pages past the first Limit rows — see discoveryView's doc
	// comment on why this exists at all: without it, a block ranked below
	// the first page's Limit (by sessions DESC) was permanently
	// unreachable, in every filter combination. Not validated against
	// Matching here (unlike limit against MaxRepeatedLimit): an offset
	// past the end is simply an empty next page, the same way
	// SessionHandler treats one past the end of a transcript.
	offset := 0
	if raw := q.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			h.fail(w, r, http.StatusBadRequest, "offset must be a non-negative integer")
			return
		}
		offset = n
	}
	view.Offset = offset

	if h.reader != nil {
		w0 := store.Window{Since: time.Now().UTC().Add(-view.Since)}

		cacheKey := discoveryCacheKey(view.Since, view.MinRequests, view.MinSessions, view.Limit, offset)
		result, hit := h.discoveryCache.get(cacheKey)
		if !hit {
			// Reads from content_hash_stats, an incrementally-maintained
			// rollup (see store.Reader.RollupContentHashStats), not a
			// from-scratch aggregation of content_refs — this used to be
			// two ~6s full scans on every cache-cold load. The rollup is
			// deliberately all-time rather than windowed (see
			// schema.sql's comment on content_hash_stats), so `since`
			// stops shaping these two numbers; it still bounds Sessionless
			// below, which is cheap enough to query live.
			blocks, err := h.reader.ContentHashStats(r.Context(), view.MinRequests, view.MinSessions, view.Limit, offset)
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
			total, matching, err := h.reader.ContentHashStatsCounts(r.Context(), view.MinRequests, view.MinSessions)
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
		view.Loaded = len(result.blocks)
		view.Shown = offset + len(result.blocks)
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
		//
		// Deliberately adjusts Shown, not Loaded: Loaded is "how many rows
		// this fetch added" (what the next page's offset must build on —
		// see MoreURL below), which must stay the true fetch count
		// regardless of how many of them hide_ignored then dims out of
		// the total shown.
		if view.HideIgnored {
			kept := view.Rows[:0]
			for _, row := range view.Rows {
				if row.State != store.DiscoveryIgnored {
					kept = append(kept, row)
				}
			}
			view.Rows = kept
			view.Shown = offset + len(view.Rows)
		}

		// HasMore/MoreURL mirror transcript.go's fillTranscript: whether
		// this fetch returned exactly Limit rows is necessary but not
		// sufficient to know there's more — the SQL LIMIT/OFFSET window
		// could have landed exactly on the last row. Comparing against
		// Matching (the true count of blocks clearing the threshold) is
		// what makes this exact rather than an optimistic guess that
		// shows a "load more" button on a page that turns out to be
		// empty.
		view.HasMore = int64(offset+view.Loaded) < view.Matching
		if view.HasMore {
			next := url.Values{}
			next.Set("since", view.SinceRaw)
			next.Set("min_requests", strconv.Itoa(view.MinRequests))
			next.Set("min_sessions", strconv.Itoa(view.MinSessions))
			next.Set("limit", strconv.Itoa(view.Limit))
			next.Set("offset", strconv.Itoa(offset+view.Loaded))
			if !view.HideIgnored {
				next.Set("show_ignored", "1")
			}
			view.MoreURL = "/admin/ui/content/repeated?" + next.Encode()
		}
	}

	// The fragment form exists only for the "load more" append — the page
	// itself is one server-rendered document, but a result set longer than
	// one window grows by appending the next page rather than by
	// re-rendering everything already read. Mirrors SessionHandler's own
	// offset>0 branch (transcript.go): extra pages carry rows only, not
	// the toolbar.
	if offset > 0 {
		h.render(w, r, "discovery", "repeated-more", view)
		return
	}

	h.render(w, r, "discovery", "repeated-rows", view)
}

// blockRequestRowView is one row in the block drill-down: a request row plus,
// when this hash's position in this specific request is known, the link to
// its pre/post guardrail diff. GuardrailDiff needs (msg_index, position) —
// coordinates that can differ per request even for the same hash (a block
// resent at a different point in the conversation) — so this is not a
// constant computed once for the page.

func shortHash(hash string) string { return shortSessionKey(hash) }
