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
const discoveryDefaultMinSessions = 2

// discoveryDefaultLimit matches the JSON endpoint's own default.
const discoveryDefaultLimit = 50

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

	if h.reader != nil {
		w0 := store.Window{Since: time.Now().UTC().Add(-view.Since)}

		cacheKey := discoveryCacheKey(view.Since, view.MinRequests, view.MinSessions, view.Limit)
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
			blocks, err := h.reader.ContentHashStats(r.Context(), view.MinRequests, view.MinSessions, view.Limit)
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

func shortHash(hash string) string { return shortSessionKey(hash) }
