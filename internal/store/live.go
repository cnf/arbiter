package store

import (
	"context"
	"fmt"
	"strings"
)

// MaxTailLimit caps one live-tail poll. It is deliberately small: a tail is a
// view of what is happening *now*, and a poll that returns hundreds of rows is
// describing a backlog rather than a tail. A burst larger than this is reported
// as truncated so the page can say so instead of silently showing a window.
const MaxTailLimit = 50

// ListRequestsAfter returns requests strictly newer than the (ts, id) cursor,
// **newest-first** — the same order as the request list this feeds, so a poll's
// rows can be prepended to the table in one go and keep their order.
//
// The direction is the one thing about this worth stating twice, because the
// obvious reading is the other way round: "rows after a cursor" suggests walking
// forwards from it, oldest-first. That ordering is wrong here — the list is
// `ts DESC`, so a new row belongs at the *top*, and returning batches
// oldest-first put the newest request below rows hours older than it.
//
// It is a separate query from ListRequests rather than a flag on it, because the
// two differ in more than direction: this one takes an *upper*-exclusive cursor
// and returns rows strictly newer than it, while ListRequests takes a
// lower-exclusive cursor and returns rows older. Folding both into one query with
// a direction switch would make the cursor's meaning depend on a flag, which is
// exactly the kind of thing that pages wrongly later.
//
// The row-value comparison `(ts, id) > (?, ?)` matches the ordering, so a row
// written in the same nanosecond as the cursor is neither skipped nor repeated.
// ts is compared as the *stored text*, which is why the caller echoes back the
// exact value it was handed (RequestRow.TsRaw) rather than reformatting a
// timestamp: a value rebuilt from a Go time would carry a different fraction
// length and sort wrongly. This is the `ts` landmine, and why the cursor is
// opaque.
func (r *Reader) ListRequestsAfter(ctx context.Context, f RequestFilter, afterTs string, afterID int64, limit int) ([]RequestRow, error) {
	where := []string{"1 = 1"}
	args := []interface{}{}

	if !f.Since.IsZero() {
		where = append(where, "ts >= ?")
		args = append(args, f.Since)
	}
	if f.Provider != "" {
		where = append(where, "provider = ?")
		args = append(args, f.Provider)
	}
	if f.SessionKey != "" {
		where = append(where, "session_key = ?")
		args = append(args, f.SessionKey)
	}
	if f.SessionKeyless {
		where = append(where, "(session_key IS NULL OR session_key = '')")
	}
	if f.Alias != "" {
		where = append(where, "alias_used = ?")
		args = append(args, f.Alias)
	}
	if f.StatusCode != 0 {
		where = append(where, "status_code = ?")
		args = append(args, f.StatusCode)
	}
	if f.ErrorsOnly {
		where = append(where, "status_code >= 400")
	}

	// Every tail poll is ordered newest-first, matching the request list it is
	// appended to. That is not a style choice: the list is `ts DESC`, so a new
	// row belongs at the *top* of the table, and returning the batch in the same
	// order the reader sees means the client can prepend it wholesale and keep
	// its internal order. An earlier version returned the batch oldest-first,
	// which appended new rows to the bottom of a newest-first table — the newest
	// request visible below rows hours older than it.
	// Both halves or neither. An id with no timestamp is refused rather than
	// ignored: treating it as "no cursor" would silently restart the tail from
	// the top, which looks like the view resetting rather than like an error.
	if (afterTs == "") != (afterID == 0) {
		return nil, fmt.Errorf("tail cursor needs both a timestamp and an id")
	}
	if afterTs != "" {
		// Strictly newer than the cursor. The row-value comparison matches the
		// ordering, so a row written in the same nanosecond as the cursor is
		// neither skipped nor repeated.
		where = append(where, "(ts, id) > (?, ?)")
		args = append(args, afterTs, afterID)
	}
	// No cursor is the first poll: everything the filters select, which is also
	// "everything newer than nothing".
	args = append(args, clampTailLimit(limit))
	q := "SELECT" + requestRowColumns + " FROM requests WHERE " +
		strings.Join(where, " AND ") + " ORDER BY ts DESC, id DESC LIMIT ?"
	return r.queryRequests(ctx, q, args, "tail")
}

// NewestCursor returns the (ts, id) cursor of the newest row in a tail response,
// and whether there is one.
//
// This exists because the index is not the same on both kinds of poll, and
// getting it wrong is silent. A first poll is newest-first, so the newest row is
// rows[0]; every later poll is oldest-first, so it is rows[len-1]. A caller that
// takes "the last row" uniformly, as an obvious reading suggests, walks the
// cursor *backwards* on the first poll and then re-reports the newest rows on
// every subsequent one — an unbounded repeat rather than an error.
//
// So the rule is named once, here, next to the query whose ordering it depends
// on, and both orderings are covered by TestNewestCursorHandlesBothOrders.
func NewestCursor(rows []RequestRow) (string, int64, bool) {
	if len(rows) == 0 {
		return "", 0, false
	}
	newest := rows[0]
	for _, row := range rows[1:] {
		// (ts, id) is the same comparison the SQL orders by, so "newest" here
		// and "newest last" there cannot disagree.
		if row.TsRaw > newest.TsRaw || (row.TsRaw == newest.TsRaw && row.ID > newest.ID) {
			newest = row
		}
	}
	if newest.TsRaw == "" {
		return "", 0, false
	}
	return newest.TsRaw, newest.ID, true
}

func clampTailLimit(limit int) int {
	if limit <= 0 || limit > MaxTailLimit {
		return MaxTailLimit
	}
	return limit
}

// queryRequests runs a request-list projection and scans it, so the tail and the
// list cannot disagree about how a row is read.
func (r *Reader) queryRequests(ctx context.Context, q string, args []interface{}, what string) ([]RequestRow, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	defer func() { _ = rows.Close() }()

	out := []RequestRow{}
	for rows.Next() {
		s, err := scanRequestRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
