package ui

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// epochChoices builds the anchor picker's options from real config changes.
//
// It degrades to an empty list on error rather than failing the page: the picker
// is a convenience over a free-form datetime input, and losing it should not
// cost the operator the whole Overview.
func (h *Handler) epochChoices(r *http.Request, selected time.Time) []epochChoice {
	// A generous lookback: the picker's whole value is reaching a change that
	// happened before the window currently on screen.
	w := store.Window{Since: time.Now().UTC().Add(-30 * 24 * time.Hour)}
	anchors, err := h.reader.ConfigEpochs(r.Context(), w, overviewMaxEpochAnchors)
	if err != nil {
		h.logger.LogError(r.Context(), "warn", err,
			map[string]interface{}{"phase": "admin_ui_overview_epochs"})
		return nil
	}
	out := make([]epochChoice, 0, len(anchors))
	for _, a := range anchors {
		if a.Started.IsZero() {
			continue
		}
		label := fmt.Sprintf("%s · %s · %s req",
			a.Started.Format("Jan 02 15:04"), shortEpoch(a.Epoch), fmtTokens(a.Requests))
		if a.Merged > 1 {
			// Saying so matters: the operator made several saves and this is
			// the one that actually served traffic.
			label += fmt.Sprintf(" · settled after %d saves", a.Merged)
		}
		// Selection is decided on the *formatted* value, not on time.Equal.
		// The option's value is RFC3339, which carries no sub-second part, so a
		// stored timestamp's nanoseconds are lost in the round trip and an
		// Equal comparison against the reparsed value always fails — the select
		// then reopened blank and the choice read as ignored. Seeded test data
		// hid this because its timestamps have zero nanoseconds; live rows do
		// not.
		value := a.Started.Format(time.RFC3339)
		out = append(out, epochChoice{
			Value:    value,
			Label:    label,
			Selected: !selected.IsZero() && selected.Format(time.RFC3339) == value,
		})
	}
	return out
}

// compareWindows builds the before/after pair around an anchor.
//
// The before-side is always a closed window ending exactly at the anchor, so the
// two sides tile without double-counting the request on the boundary (see
// store.Window's own comment). With spanToNow the after-side is open-ended and
// the before-side matches its *elapsed* length, which is what makes the two
// comparable — a fixed-length before against a still-growing after would show a
// spurious volume drop that shrinks as the day goes on.
func compareWindows(anchor time.Time, span time.Duration, mode anchorSpan) (before, after store.Window) {
	if mode == spanToNow {
		elapsed := time.Since(anchor)
		if elapsed <= 0 {
			elapsed = span
		}
		return store.Window{Since: anchor.Add(-elapsed), Until: anchor},
			store.Window{Since: anchor}
	}
	return store.Window{Since: anchor.Add(-span), Until: anchor},
		store.Window{Since: anchor, Until: anchor.Add(span)}
}

// parseAnchor accepts both what the picker emits (RFC3339) and what a raw
// datetime-local input emits (no zone). A zoneless value is read as UTC,
// matching the rest of the UI, which labels every timestamp UTC.
func parseAnchor(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable anchor %q", raw)
}

// nodeURL builds the drawer's fetch URL for one node, carrying the window so the
// drawer and the diagram always describe the same traffic.
func nodeURL(id, since, anchor, span string) string {
	v := url.Values{}
	v.Set("id", id)
	if since != "" {
		v.Set("since", since)
	}
	if anchor != "" {
		v.Set("anchor", anchor)
	}
	if span != "" {
		v.Set("span", span)
	}
	return "/admin/ui/overview/node?" + v.Encode()
}

// sinceChoices are the trailing-window presets the range control offers.
func sinceChoices() []string { return []string{"1h", "6h", "24h", "72h", "168h", "720h"} }

// defaultSinceChoice is the option the window select opens on, as the *string
// the select offers* rather than overviewDefaultWindow.String().
//
// Those differ: Duration.String() renders 24h as "24h0m0s", which matches no
// option value, so the select fell back to displaying its first entry ("last
// 1h") while the page rendered 24 hours of data. The two must be the same
// literal, and this constant is checked against overviewDefaultWindow by
// TestDefaultSinceChoiceIsOffered.
const defaultSinceChoice = "24h"
