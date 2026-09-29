package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
)

// rollupName is rollup_state's single row key. One name, not one row per
// hash, because the state is a single watermark that describes progress
// through requests.id — see schema.sql's comment on rollup_state.
const rollupName = "content_hash"

// rollupBatchSize caps how many requests one RollupContentHashStats call
// folds in. The sweep runs hourly (see cmd/arbiter/store.go's
// sweepInterval) and normal traffic volume clears in one pass; the cap
// exists so a large backlog (first run after upgrade, or a sweep that
// missed several cycles) is worked off over a few sweeps instead of one
// call holding a write-shaped transaction open for however long a full
// backlog takes.
const rollupBatchSize = 200_000

// RollupContentHashStats folds newly-arrived content_refs into
// content_hash_stats, picking up from rollup_state's watermark. It is meant
// to run on a timer (see cmd/arbiter/store.go's sweeper) rather than per
// request: Discovery is an inspection page, not something the request path
// should pay to keep current for. See schema.sql's comment on
// content_hash_stats for why this trades exactness for speed, and why that
// trade was accepted.
//
// Returns the number of requests folded in (0 means already caught up) and
// the new watermark.
func (r *Reader) RollupContentHashStats(ctx context.Context) (folded int64, watermark int64, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin content hash rollup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var last int64
	err = tx.QueryRowContext(ctx, `SELECT last_owner_id FROM rollup_state WHERE name = ?`, rollupName).Scan(&last)
	if err != nil && err != sql.ErrNoRows {
		return 0, 0, fmt.Errorf("read rollup watermark: %w", err)
	}

	var maxID sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(id) FROM requests WHERE id > ? AND kind = 'client'`, last).Scan(&maxID); err != nil {
		return 0, 0, fmt.Errorf("find rollup ceiling: %w", err)
	}
	if !maxID.Valid {
		// Nothing new since the last run.
		return 0, last, nil
	}
	upper := maxID.Int64
	if upper-last > rollupBatchSize {
		upper = last + rollupBatchSize
	}

	// Requests: a running COUNT(DISTINCT owner_id) is safe to add to directly
	// (see schema.sql) — sum the batch's own per-hash request count into
	// whatever is already stored.
	//
	// direction='request', kind='client' mirrors RepeatedContent's own
	// filter (discovery is about what the client sent, pre-guardrail) —
	// this must not drift from that definition, same reasoning as
	// ContentHashCounts' doc comment.
	const upsertRequests = `
INSERT INTO content_hash_stats (hash, block_type, role, requests, first_ts, last_ts)
SELECT cr.hash, MIN(cr.block_type), COALESCE(MIN(cr.role), ''),
       COUNT(DISTINCT cr.owner_id), MIN(r.ts), MAX(r.ts)
FROM content_refs cr
JOIN requests r ON r.id = cr.owner_id AND cr.owner_kind = 'request'
WHERE cr.direction = 'request' AND r.kind = 'client'
  AND cr.owner_id > ? AND cr.owner_id <= ?
GROUP BY cr.hash
ON CONFLICT(hash) DO UPDATE SET
    requests   = requests + excluded.requests,
    block_type = excluded.block_type,
    role       = CASE WHEN role = '' THEN excluded.role ELSE role END,
    first_ts   = MIN(first_ts, excluded.first_ts),
    last_ts    = MAX(last_ts, excluded.last_ts)`

	if _, err := tx.ExecContext(ctx, upsertRequests, last, upper); err != nil {
		return 0, 0, fmt.Errorf("rollup requests: %w", err)
	}

	// Sessions: cannot be summed the same way (the same session resends the
	// same block every turn). INSERT ... ON CONFLICT DO NOTHING RETURNING
	// against content_hash_sessions is the dedup: only pairs genuinely new
	// (never seen in this or any earlier batch) come back, and only those
	// bump content_hash_stats.sessions. Sessionless requests (no
	// session_key) are excluded here, matching RepeatedContent's
	// COUNT(DISTINCT session_key), which never counts NULLs.
	const insertSessions = `
INSERT INTO content_hash_sessions (hash, session_key)
SELECT DISTINCT cr.hash, r.session_key
FROM content_refs cr
JOIN requests r ON r.id = cr.owner_id AND cr.owner_kind = 'request'
WHERE cr.direction = 'request' AND r.kind = 'client'
  AND cr.owner_id > ? AND cr.owner_id <= ?
  AND r.session_key IS NOT NULL AND r.session_key != ''
ON CONFLICT(hash, session_key) DO NOTHING
RETURNING hash`

	rows, err := tx.QueryContext(ctx, insertSessions, last, upper)
	if err != nil {
		return 0, 0, fmt.Errorf("rollup sessions: %w", err)
	}
	newSessions := map[string]int64{}
	for rows.Next() {
		var hash []byte
		if err := rows.Scan(&hash); err != nil {
			_ = rows.Close()
			return 0, 0, fmt.Errorf("scan rollup session hash: %w", err)
		}
		newSessions[string(hash)]++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, 0, fmt.Errorf("rollup sessions: %w", err)
	}
	_ = rows.Close()

	if len(newSessions) > 0 {
		stmt, err := tx.PrepareContext(ctx, `
UPDATE content_hash_stats SET sessions = sessions + ? WHERE hash = ?`)
		if err != nil {
			return 0, 0, fmt.Errorf("prepare session bump: %w", err)
		}
		for hash, n := range newSessions {
			if _, err := stmt.ExecContext(ctx, n, []byte(hash)); err != nil {
				_ = stmt.Close()
				return 0, 0, fmt.Errorf("bump session count: %w", err)
			}
		}
		_ = stmt.Close()
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO rollup_state (name, last_owner_id) VALUES (?, ?)
ON CONFLICT(name) DO UPDATE SET last_owner_id = excluded.last_owner_id`,
		rollupName, upper); err != nil {
		return 0, 0, fmt.Errorf("advance rollup watermark: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit content hash rollup: %w", err)
	}
	return upper - last, upper, nil
}

// ContentHashStats is RepeatedContent's replacement read path: the same
// question ("which blocks recur, most widespread first"), answered from the
// rollup table instead of aggregating content_refs from scratch. Same
// shape/ordering/filtering contract as RepeatedContent (see its doc
// comment) so the two are interchangeable from the caller's point of view —
// minRequests/minSessions/limit clamp the same way.
//
// Deliberately no time window: content_hash_stats is all-time by design
// (see schema.sql). A caller that still wants "since" for other numbers
// (SessionlessRequestCount) queries that separately.
func (r *Reader) ContentHashStats(ctx context.Context, minRequests, minSessions, limit int) ([]RepeatedContent, error) {
	if minRequests < 2 {
		minRequests = 2
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	const q = `
SELECT h.hash, h.block_type, h.role,
       COALESCE(SUBSTR(c.body, 1, 200), ''),
       h.requests, h.sessions, h.first_ts, h.last_ts
FROM (
    SELECT hash, block_type, role, requests, sessions, first_ts, last_ts
    FROM content_hash_stats
    WHERE requests >= ? AND sessions >= ?
    ORDER BY sessions DESC
    LIMIT ?
) h
LEFT JOIN content c ON c.hash = h.hash`

	rows, err := r.db.QueryContext(ctx, q, minRequests, minSessions, limit)
	if err != nil {
		return nil, fmt.Errorf("content hash stats: %w", err)
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
			return nil, fmt.Errorf("scan content hash stats: %w", err)
		}
		rc.Hash = hex.EncodeToString(hash)
		rc.FirstSeen = formatTime(first)
		rc.LastSeen = formatTime(lastRaw)
		out = append(out, rc)
	}
	return out, rows.Err()
}

// ContentHashStatsCounts is ContentHashCounts' replacement: total distinct
// blocks vs how many pass the thresholds, read from the rollup instead of
// aggregated from scratch. See ContentHashStats' doc comment on scope.
func (r *Reader) ContentHashStatsCounts(ctx context.Context, minRequests, minSessions int) (total, matching int64, err error) {
	const q = `
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN requests >= ? AND sessions >= ? THEN 1 ELSE 0 END), 0)
FROM content_hash_stats`

	err = r.db.QueryRowContext(ctx, q, minRequests, minSessions).Scan(&total, &matching)
	if err != nil {
		return 0, 0, fmt.Errorf("content hash stats counts: %w", err)
	}
	return total, matching, nil
}
