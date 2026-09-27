package ui

import (
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// cacheLabel renders a cache-hit rate, distinguishing "0%" from "nothing here
// was cacheable" — the two look identical as a number and mean opposite things.
func cacheLabel(m store.Measures) string {
	if !m.Cacheable() {
		return "—"
	}
	return fmtPct(m.CacheHitRate())
}

// windowLabel describes a window for the toolbar.
func windowLabel(w store.Window) string {
	return fmtWindowEdge(w.Since) + " → " + untilLabel(w)
}

func untilLabel(w store.Window) string {
	if !w.Bounded() {
		return "now"
	}
	return fmtWindowEdge(w.Until)
}

// fmtWindowEdge renders a window bound. It takes a time.Time rather than going
// through fmtClock, which parses the store's own text layout — these bounds are
// computed in Go and were never stored, so there is nothing to parse.
func fmtWindowEdge(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") }

// shortEpoch truncates a config hash for display, same reasoning as shortHash:
// an opaque handle is shown short and carried whole.
func shortEpoch(epoch string) string {
	if len(epoch) <= 8 {
		return epoch
	}
	return epoch[:8]
}

// errorClass and latencyClass turn a measurement into a severity the template
// only has to name. The thresholds are here rather than in CSS because they are
// judgements about this deployment's traffic, not styling.
func errorClass(rate float64) string {
	switch {
	case rate >= 0.05:
		return "bad"
	case rate > 0:
		return "warn"
	default:
		return "good"
	}
}

func latencyClass(ms int64) string {
	switch {
	case ms >= 30_000:
		return "warn"
	default:
		return ""
	}
}

// sinceLabelFor names a window duration for the select, and says what the
// duration *does* in each mode — the same "168h" means "the last 7 days" on a
// single window and "7 days either side of the change" in a compare, and a
// control whose meaning shifts silently under a mode is the one thing a reader
// cannot recover from the page.
func sinceLabelFor(raw string, compare bool) string {
	if compare {
		return raw + " each side"
	}
	return "last " + raw
}

// activeSince renders the window duration for a form value, so the select
// reopens on what is actually in play. An empty raw value means the handler
// applied the default, which is the option literal rather than the Duration's
// own String() form (see defaultSinceChoice).
func activeSince(raw string) string {
	if raw == "" {
		return defaultSinceChoice
	}
	return raw
}
