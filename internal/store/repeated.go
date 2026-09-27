package store

import (
	"context"
	"encoding/hex"
	"fmt"
)

// RepeatedContent is one block that appears more than once, with the reach of
// its repetition: how many requests contain it and how many distinct sessions.
//
// This is the query the whole content-addressed design exists to make possible
// — "find the text that appears in every query" is the post-hoc detection
// mechanism for client-injected boilerplate, with no need to know the prompt in
// advance. A block appearing across many sessions but few requests is noise; one
// appearing in every session is the client's own preamble.
type RepeatedContent struct {
	Hash      string `json:"hash"`
	BlockType string `json:"block_type"`
	Role      string `json:"role,omitempty"`
	Preview   string `json:"preview"` // first 200 chars of the body, for recognisability
	Requests  int64  `json:"requests"`
	Sessions  int64  `json:"sessions"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
}

// RepeatedContent finds blocks seen in at least minRequests distinct requests
// over a window, most widespread first. minSessions>0 additionally requires the
// block to span at least that many distinct sessions, which is what separates
// "this client always prepends X" from "this one conversation is long".
func (r *Reader) RepeatedContent(ctx context.Context, w Window, minRequests, minSessions, limit int) ([]RepeatedContent, error) {
	if minRequests < 2 {
		minRequests = 2
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	// Sessions are counted with COUNT(DISTINCT) because session_key is
	// nullable: rows with no session key collapse into one group rather than
	// inflating the count, which is the honest reading of "distinct sessions".
	//
	// direction = 'request' restricts this to what the client actually sent,
	// pre-guardrail. request_guardrailed (the post-rewrite text a guardrail
	// rule already produced) and response (the model's own reply) are
	// excluded: discovery exists to find new patterns worth writing a rule
	// against, and mixing in a rule's own output would silently double-count
	// a pattern that already has one (or misreport its reach) instead of
	// showing what the client is sending.
	//
	// Ordered by session count alone: how many requests share a block only
	// measures how long one conversation ran (a resent block appears once per
	// owner_id it's referenced from), which is not itself a finding — see
	// RepeatedContent's doc comment above.
	// The content join is deliberately pulled outside the aggregation. If
	// LEFT JOIN content sits in the FROM clause before GROUP BY, SQLite
	// dereferences every matching content row (millions of content_refs rows
	// in a real deployment) before the HAVING/LIMIT gets a chance to discard
	// almost all of them. Measured against a 1.1GB production database
	// (~4M direction='request' refs): with the join inlined the query took
	// ~108s; aggregating first and joining content only for the ~50
	// surviving rows took ~6s. Same result set, ~17x faster.
	const q = `
SELECT h.hash, h.block_type, h.role,
       COALESCE(SUBSTR(c.body, 1, 200), ''),
       h.requests, h.sessions, h.first_ts, h.last_ts
FROM (
    SELECT cr.hash AS hash,
           MIN(cr.block_type) AS block_type,
           COALESCE(MIN(cr.role), '') AS role,
           COUNT(DISTINCT cr.owner_id) AS requests,
           COUNT(DISTINCT r.session_key) AS sessions,
           MIN(r.ts) AS first_ts, MAX(r.ts) AS last_ts
    FROM content_refs cr
    JOIN requests r ON r.id = cr.owner_id AND cr.owner_kind = 'request'
    WHERE r.ts >= ? AND r.kind = 'client' AND cr.direction = 'request'
    GROUP BY cr.hash
    HAVING COUNT(DISTINCT cr.owner_id) >= ?
       AND COUNT(DISTINCT r.session_key) >= ?
    ORDER BY sessions DESC
    LIMIT ?
) h
LEFT JOIN content c ON c.hash = h.hash`

	rows, err := r.db.QueryContext(ctx, q, w.Since, minRequests, minSessions, limit)
	if err != nil {
		return nil, fmt.Errorf("repeated content: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []RepeatedContent{}
	for rows.Next() {
		var (
			rc             RepeatedContent
			hash           []byte
			first, lastRaw interface{}
		)
		if err := rows.Scan(&hash, &rc.BlockType, &rc.Role, &rc.Preview,
			&rc.Requests, &rc.Sessions, &first, &lastRaw); err != nil {
			return nil, fmt.Errorf("scan repeated content: %w", err)
		}
		rc.Hash = hex.EncodeToString(hash)
		rc.FirstSeen = formatTime(first)
		rc.LastSeen = formatTime(lastRaw)
		out = append(out, rc)
	}
	return out, rows.Err()
}
