package store

import "time"

// storedTimeLayouts are the layouts a sqlite TIMESTAMP value can arrive in,
// in the order worth trying.
//
// Why more than one: a plain column read gives the driver's normal layout,
// but MIN()/MAX() over a TIMESTAMP loses that conversion and comes back as
// time.Time.String()'s own layout instead. RFC3339 covers anything that has
// already been normalised by formatTime, and the dateless layout covers a
// hand-inserted row.
//
// This list was previously written out in three packages (the reader's
// formatTime, the UI's parseTS, and flow's parseStoredTs) and had already
// drifted: only one of the three accepted all four layouts. One list, one
// parser, so a new layout is added once.
var storedTimeLayouts = []string{
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05.999999999-07:00",
	time.RFC3339,
	"2006-01-02 15:04:05",
}

// ParseStoredTime parses a timestamp as sqlite hands it back, trying each
// known layout. The second result is false when none matched, leaving the
// caller free to fall back or ignore rather than treat it as fatal — a
// timestamp is presentation, and one unparseable row should not fail a list.
func ParseStoredTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range storedTimeLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
