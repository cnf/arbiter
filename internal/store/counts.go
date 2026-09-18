package store

import (
	"context"
	"fmt"
	"time"
)

// CountSince returns how many requests the store recorded at or after since.
//
// This exists to seed a rate-limit guardrail's counters. Those counters used to
// live only in memory, so they were lost whenever the pipeline was rebuilt — on
// every config save — which made a per-day cap meaningless in practice: 1000
// requests allowed, then 1000 again after an unrelated edit.
//
// Deliberately counts EVERY kind of request, including Arbiter's own internal
// calls (classifier, title-gen). Those are real upstream requests, and a limit
// meant to mirror an upstream's published cap has to see them or it would not
// reflect what the upstream counts. The kind filter used by the spend
// aggregates exists to keep classifier cost out of the operator's own traffic
// figures, which is a different question from "how many requests has this
// upstream received".
// ts is stored as TEXT in the writer's own layout, which includes the LOCAL
// offset ("2026-09-18T22:44:28+02:00"). The comparison is therefore lexicographic
// on that string, so the bound must be formatted the SAME way — binding a UTC
// instant produces "20:15:00+00:00", which sorts before a local "22:15:00+02:00"
// and silently matches rows outside the window. That is not a subtle off-by-one;
// it made a 30-minute window include everything from the last two hours.
//
// Both callers format through tsText so the two cannot drift apart.
func (r *Reader) CountSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM requests WHERE ts >= ?`, tsText(since)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count requests since %s: %w", tsText(since), err)
	}
	return n, nil
}

// CountSinceByProvider is the same count narrowed to one provider, for the
// per-provider limits REQUIREMENTS §3 asks for (mirroring e.g. an upstream's
// published 10/min). It uses idx_requests_provider_ts.
func (r *Reader) CountSinceByProvider(ctx context.Context, provider string, since time.Time) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM requests WHERE provider = ? AND ts >= ?`,
		provider, tsText(since)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count requests for provider %q since %s: %w",
			provider, tsText(since), err)
	}
	return n, nil
}

// tsText renders a time the way the writer stores it, so a comparison against the
// ts column is like-for-like.
//
// This must match how insertRequestTx binds ev.Ts exactly. The driver formats a
// time.Time in its own local layout, and the column is TEXT, so the comparison is
// a string comparison — formatting the bound differently (e.g. .UTC()) silently
// changes which rows match rather than failing.
func tsText(t time.Time) string {
	return t.Format("2006-01-02 15:04:05.999999999-07:00")
}
