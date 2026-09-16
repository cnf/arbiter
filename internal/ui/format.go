package ui

import (
	"fmt"
	stdhtml "html/template"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// funcs are the template helpers. Every one of them is a *formatter*: nothing
// here marks a value safe, and nothing here reads the store. The rule the
// templates depend on is that no stored body is ever marked safe in any
// context — see TestNoUnsafeContentConversions.
var funcs = stdhtml.FuncMap{
	"usd":         fmtUSD,
	"tokens":      fmtTokens,
	"dur":         fmtDur,
	"ago":         fmtAgo,
	"ts":          fmtTs,
	"clock":       fmtClock,
	"statusClass": statusClass,
	"trunc":       truncBody,
	"wasCut":      wasCut,
	"pct":         fmtPct,
	"inc":         func(i int) int { return i + 1 },
	"avgCost":     fmtAvgCost,
	"splitList":   splitList,
	"sessionsNav": func(raw string) string { return sessionsNavHref(raw) },
	"sinceLabel":  sinceLabel,
	"chars":       fmtChars,
	// requestsForSession builds a link to the flat request list filtered to one
	// session — the request-level view of the same conversation. It is a func
	// rather than a precomputed field because it is used with a key that is
	// already in the view model, in two different templates.
	"requestsForSession": requestsForSession,
	"pivotLimit":         pivotLimitNote,
	"seriesURL":          seriesURL,
	"singleValuedHint":   func() string { return singleValuedHint },
	// requestsForBlock builds the drill-down link from a repeated block to the
	// requests containing it. A func rather than an inline expression because a
	// template must not be assembling a query string by hand.
	"requestsForBlock": requestsForBlockURL,
	// blockPreviewBytes is the truncation cap as a number, so the block page can
	// say what it cut at instead of naming a constant in prose that could drift.
	"blockPreviewBytes": func() int { return blockPreviewBytes },
	"preview":           previewText,
	"whitespaceOnly":    isWhitespaceOnly,
}

// fmtChars renders a character count compactly ("9.2k chars"). Transcript
// entries are sized in characters rather than tokens because a character count
// is exact for text the page is about to render or hide, while a token count
// would be an estimate Arbiter did not make.
func fmtChars(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d chars", n)
	case n < 1000*1000:
		return fmt.Sprintf("%.1fk chars", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1fM chars", float64(n)/1_000_000)
	}
}

// fmtAvgCost is cost-per-turn, the figure that makes two conversations of
// different length comparable at a glance — the same reason /admin/stats/epochs
// reports avg_cost_usd.
func fmtAvgCost(cost float64, turns int64) string {
	if turns <= 0 {
		return ""
	}
	return fmtUSD(cost / float64(turns))
}

// splitList splits a comma-joined column (group_concat's output) for rendering.
// It is a display helper for values the store joined, not a parser for
// structured data.
func splitList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// sinceLabel names a window for a link: the configured default when unset,
// else the raw duration the operator typed.
func sinceLabel(raw string) string {
	if raw == "" {
		return "last 168h (default)"
	}
	return "last " + raw
}

// sessionsNavHref preserves the window across a link back to the index.
func sessionsNavHref(raw string) string {
	if raw == "" {
		return "/admin/ui/sessions"
	}
	return "/admin/ui/sessions?since=" + url.QueryEscape(raw)
}

// fmtUSD renders a cost. Per-request costs are fractions of a cent, so two
// decimals would render most rows as $0.00 and hide the differences the page
// exists to show.
func fmtUSD(v float64) string {
	if v == 0 {
		return "$0"
	}
	if v < 0.01 {
		return fmt.Sprintf("$%.6f", v)
	}
	return fmt.Sprintf("$%.4f", v)
}

// fmtPct renders a 0–1 ratio as a percentage.
func fmtPct(v float64) string { return fmt.Sprintf("%.1f%%", v*100) }

// fmtTokens abbreviates a token count: exact below 10k, then 12.3k / 1.2M.
// Exactness matters at the low end (a 300-token request is a different animal
// from a 3,000-token one), readability at the high end.
func fmtTokens(n int64) string {
	switch {
	case n < 10_000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

// fmtDur renders a latency in ms as a short human duration.
func fmtDur(ms int64) string {
	switch {
	case ms <= 0:
		return "—"
	case ms < 1000:
		return fmt.Sprintf("%dms", ms)
	case ms < 60_000:
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	default:
		return fmt.Sprintf("%dm%02ds", ms/60_000, (ms%60_000)/1000)
	}
}

// fmtAgo renders how long ago a stored timestamp was, relative to now. The
// absolute time is rendered next to it (fmtClock), because "3h ago" alone
// cannot be reconciled with anything else.
func fmtAgo(ts string) string {
	t, ok := parseTS(ts)
	if !ok {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "in the future"
	case d < 60*time.Second:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// fmtClock renders a stored timestamp as a UTC clock time, truncating the
// fractional seconds: the store keeps nanoseconds, nothing here is measured to
// them, and the noise makes a column of timestamps unreadable.
func fmtClock(ts string) string {
	t, ok := parseTS(ts)
	if !ok {
		return ts
	}
	return t.Format("2006-01-02 15:04:05")
}

// fmtTs renders a stored timestamp as RFC3339 in UTC, for the tooltip that
// carries the exact value.
func fmtTs(ts string) string {
	t, ok := parseTS(ts)
	if !ok {
		return ts
	}
	return t.Format(time.RFC3339)
}

// parseTS parses what the store hands back. reader.formatTime already
// normalises to RFC3339 in UTC, but a raw sqlite value can reach a template if
// a query is added without going through it, so the driver's own layout is
// accepted too rather than rendered as garbage.
func parseTS(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// statusClass maps an HTTP status to a CSS class. It returns an identifier
// (never a body), which is the one kind of stored-ish value a template may use
// in a class attribute.
func statusClass(code int64) string {
	switch {
	case code >= 500:
		return "s-err"
	case code >= 400:
		return "s-warn"
	case code >= 300:
		return "s-note"
	case code >= 200:
		return "s-ok"
	default:
		return "s-none"
	}
}

// truncBody cuts a stored body to the preview cap on a rune boundary, so a
// multi-byte character is never split into a broken rune. The caller renders
// the full text from the same block's own URL — this returns a prefix, not
// something to further process.
func truncBody(s string) string {
	if len(s) <= blockPreviewBytes {
		return s
	}
	cut := s[:blockPreviewBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// wasCut reports whether a body was truncated, so the template can offer the
// full text only when there is more of it.
func wasCut(s string) bool { return len(s) > blockPreviewBytes }

// whitespaceOnlyNote stands in for a body that carries no printable text.
//
// It is not cosmetic. A block whose body is "\n\n" — a real case in ordinary
// traffic, where a client separates messages with blank lines — renders as an
// empty cell, which is exactly what a broken render looks like. The page's
// previews exist so a block is recognisable at a glance, and a blank one is not
// recognisable as anything: it reads as a bug rather than as two newlines.
const whitespaceOnlyNote = "(whitespace only)"

// previewText renders a block preview, naming the whitespace-only case rather
// than showing an empty cell. It returns the stored text unchanged otherwise —
// this is a display fallback, not a parser, and the value still goes through the
// template's contextual escaper as an ordinary string.
func previewText(s string) string {
	if strings.TrimSpace(s) == "" {
		return whitespaceOnlyNote
	}
	return s
}

// isWhitespaceOnly reports the same case for a body rendered in full, so the
// block page can say why its <pre> is blank.
func isWhitespaceOnly(s string) bool { return strings.TrimSpace(s) == "" }
