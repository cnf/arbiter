package ui

import (
	"errors"
	"net/http"

	"github.com/cnf/arbiter/internal/store"
)

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
