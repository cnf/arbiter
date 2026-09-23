package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// MaxRepeatedLimit caps a discovery query. It matches the bound RepeatedContent
// already enforced inline, named so a caller can validate against it rather than
// hard-coding 200 in two places.
const MaxRepeatedLimit = 200

// MinRepeatedRequests is the smallest request count that can mean "repeated".
// RepeatedContent clamps anything lower up to 2, so it is stated here and checked
// by the caller instead: silently raising a number the operator typed is how a
// page ends up describing a different query than the one on screen.
const MinRepeatedRequests = 2

// RequestsForContent returns the requests that contain one content block, newest
// first. It is the drill-down from a repeated block to the traffic it came from,
// and it is the whole reason the block list is worth having — a hash tells you
// nothing about which client sends it, and the request rows do.
//
// It is `id IN (subquery)` rather than a join against content_refs for two
// reasons. A join multiplies a request once per matching *reference* — a block
// re-sent in three messages of one conversation is three rows for one request —
// so the list would report a request three times. And the subquery lets
// requestRowColumns and scanRequestRow be reused exactly as ListRequests uses
// them, so a column added to the list projection cannot land in one query and
// not the other.
//
// The subquery is served by idx_content_refs_hash.
func (r *Reader) RequestsForContent(ctx context.Context, hash string, limit int) ([]RequestRow, error) {
	raw, err := decodeHash(hash)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > maxRequestListLimit {
		limit = maxRequestListLimit
	}

	q := "SELECT" + requestRowColumns + ` FROM requests
WHERE kind = 'client' AND id IN (
    SELECT cr.owner_id FROM content_refs cr
    WHERE cr.owner_kind = 'request' AND cr.hash = ?
)
ORDER BY ts DESC, id DESC LIMIT ?`

	rows, err := r.db.QueryContext(ctx, q, raw, limit)
	if err != nil {
		return nil, fmt.Errorf("requests for content: %w", err)
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

// ErrBadContentHash reports a content hash that is not the 32-byte hex form the
// store keys on. It is a sentinel so a caller can distinguish the operator's bad
// input from a query failure, rather than matching on the message text: a
// malformed hash reported as a 500 sends someone looking for a server bug.
var ErrBadContentHash = errors.New("bad content hash")

// decodeHash turns the hex form the UI carries in a URL back into the 32 raw
// bytes content_refs.hash stores. The text form a page shows and the blob form
// the index is on are different values: binding the hex string directly compares
// 64 characters of ASCII against a 32-byte blob, which matches nothing and looks
// exactly like a hash that is simply absent.
func decodeHash(hash string) ([]byte, error) {
	raw, err := hex.DecodeString(hash)
	if err != nil {
		return nil, fmt.Errorf("%w: must be hex: %v", ErrBadContentHash, err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("%w: must be 32 bytes, got %d decoded from %d hex characters",
			ErrBadContentHash, len(raw), len(hash))
	}
	return raw, nil
}

// ContentByHash returns one stored block by its hash, and whether it exists. It
// is the drill-down's body: the block list shows a 200-character preview, and
// the question that follows is always "what does the whole thing say".
//
// A hash that is known but whose body was not captured returns ok=true with
// Captured=false. That is a real and reachable state — content is written even
// when capture of the body was off — and reporting it as "not found" would
// contradict the block list that just showed the hash.
func (r *Reader) ContentByHash(ctx context.Context, hash string) (ContentBlock, bool, error) {
	raw, err := decodeHash(hash)
	if err != nil {
		return ContentBlock{}, false, err
	}

	// content.kind and content_refs.block_type hold the same value for a block
	// (the reference denormalises it for filtering), so this reads the type from
	// content and takes the role from the reference — the one field content
	// does not carry. MAX() rather than a bare column because the join fans out
	// one row per reference; a hash is referenced with one role in practice, and
	// if that ever stops being true the prompt-injection case is the interesting
	// one, which is what MAX() picks.
	const q = `
SELECT c.hash, c.kind, c.body, COALESCE(MAX(cr.role), '')
FROM content c
LEFT JOIN content_refs cr ON cr.hash = c.hash
WHERE c.hash = ?
GROUP BY c.hash, c.kind, c.body`

	var (
		got      ContentBlock
		hashBlob []byte
		bodyNull sql.NullString
	)
	err = r.db.QueryRowContext(ctx, q, raw).Scan(&hashBlob, &got.BlockType, &bodyNull, &got.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return ContentBlock{}, false, nil
	}
	if err != nil {
		return ContentBlock{}, false, fmt.Errorf("content by hash: %w", err)
	}
	got.Hash = hex.EncodeToString(hashBlob)
	got.Body = bodyNull.String
	got.Captured = bodyNull.Valid
	return got, true, nil
}

// ContentHashCounts reports how much repeated content exists in a window: the
// total number of distinct blocks, and how many survive the given thresholds.
//
// It exists because the two numbers answer different questions and only one of
// them is a finding. min_sessions is a filter, and on a store where most traffic
// has no session key a block that is genuinely widespread can still fail it — so
// a page that showed only the matching count would present a filter's effect as
// the state of the world. Total is what makes that readable.
func (r *Reader) ContentHashCounts(ctx context.Context, w Window, minRequests, minSessions int) (total, matching int64, err error) {
	// The HAVING conditions mirror RepeatedContent's; TestContentHashCountsAgree
	// holds the two together on the same seeded rows, which is what keeps this
	// from drifting into a different definition of "repeated".
	const q = `
SELECT COUNT(*) AS total,
       COALESCE(SUM(CASE WHEN requests >= ? AND sessions >= ? THEN 1 ELSE 0 END), 0) AS matching
FROM (
    SELECT cr.hash,
           COUNT(DISTINCT cr.owner_id) AS requests,
           COUNT(DISTINCT r.session_key) AS sessions
    FROM content_refs cr
    JOIN requests r ON r.id = cr.owner_id AND cr.owner_kind = 'request'
    WHERE r.ts >= ? AND r.kind = 'client'
    GROUP BY cr.hash
)`

	err = r.db.QueryRowContext(ctx, q, minRequests, minSessions, w.Since).Scan(&total, &matching)
	if err != nil {
		return 0, 0, fmt.Errorf("content hash counts: %w", err)
	}
	return total, matching, nil
}

// ContentHashHex is the display form of a stored block hash. It exists so the
// one conversion between the blob the store keys on and the hex the UI carries
// in URLs lives with the query that produced it, rather than in whichever
// caller happened to need it first.
func ContentHashHex(hash []byte) string { return hex.EncodeToString(hash) }

// RepeatedBoundsNote is the human-readable statement of the accepted parameter
// ranges. It is used for the 400 body and in the page copy, so the two cannot
// disagree about what the page accepts.
func RepeatedBoundsNote() string {
	return fmt.Sprintf("min_requests %d or more, min_sessions 0 or more, limit 1-%d",
		MinRepeatedRequests, MaxRepeatedLimit)
}

// ParentSession is the session a title-generation (or other non-client
// auxiliary) request belongs to, as inferred after the fact — the request
// itself carries no explicit parent pointer, so this is always a match
// against other rows, never a stored relationship.
type ParentSession struct {
	SessionKey string `json:"session_key"`
	// Matches is how many client requests support this match. For a
	// SessionKeyMatch it is every other client row sharing the same
	// session_key; for a ContentHashMatch it is the count that made
	// GROUP BY win the tie-break, not a total.
	Matches int64 `json:"matches"`
	// Method records which tier produced the match, so a caller (the UI)
	// can say *how* confident the link is instead of presenting both kinds
	// identically. See ParentSessionForTitle's doc comment for the two
	// tiers.
	Method string `json:"method"`
}

const (
	// ParentMatchSessionKey is the exact-match tier: the title request's own
	// session_key is shared by other client requests, because the client
	// sent the same session-affinity header on both. See config's
	// session_affinity.header and Hermes' session_affinity_header.
	ParentMatchSessionKey = "session_key"

	// ParentMatchContentHash is the fallback tier: no session_key overlap,
	// so the match is inferred from shared user-turn content blocks
	// instead (a client resends its history, so an old title's own request
	// text reappears verbatim in the real conversation's later turns).
	ParentMatchContentHash = "content_hash"
)

// ParentSessionForTitle finds the session a request_kind='title' (or other
// non-client) request most likely belongs to.
//
// Two tiers, tried in order:
//
//  1. Session-key match: if the request's own session_key is shared by any
//     client request, that is the parent — exact and free of false
//     positives, but only fires when the client sent the same
//     session-affinity header (e.g. X-Session-Id) on the auxiliary call as
//     on its real turns. Hermes documents doing exactly this.
//  2. Content-hash fallback: join on the request's own user-role content
//     blocks against other requests' user-role blocks, excluding itself and
//     other title requests (two title-gen retries for the same opener would
//     otherwise "match" each other), and pick the session_key with the most
//     supporting requests — a client resends its whole history each turn,
//     so several prior requests in the real session legitimately match.
//
// Returns ok=false when neither tier finds anything — a client that sends no
// session-affinity header and whose title text doesn't verbatim-reappear
// (e.g. it was expanded/wrapped downstream before the real send) is outside
// what this method can prove. That is a distinct state from capture_content
// being off, which the caller must check separately: tier 2 requires content
// capture, but tier 1 does not, so a capture-off deployment can still resolve
// a parent through session_key alone.
func (r *Reader) ParentSessionForTitle(ctx context.Context, requestID int64) (ParentSession, bool, error) {
	const sessionKeyQuery = `
SELECT r2.session_key, COUNT(*) AS n
FROM requests r1
JOIN requests r2 ON r2.session_key = r1.session_key
WHERE r1.id = ?
  AND r1.session_key IS NOT NULL AND r1.session_key != ''
  AND r2.id != r1.id
  AND r2.kind = 'client'
  AND (r2.request_kind IS NULL OR r2.request_kind != 'title')
GROUP BY r2.session_key`

	var sk sql.NullString
	var n int64
	err := r.db.QueryRowContext(ctx, sessionKeyQuery, requestID).Scan(&sk, &n)
	switch {
	case err == nil:
		return ParentSession{SessionKey: sk.String, Matches: n, Method: ParentMatchSessionKey}, true, nil
	case errors.Is(err, sql.ErrNoRows):
		// fall through to tier 2
	default:
		return ParentSession{}, false, fmt.Errorf("parent session by session_key: %w", err)
	}

	const contentHashQuery = `
SELECT r2.session_key, COUNT(*) AS n
FROM content_refs cr1
JOIN content_refs cr2 ON cr2.hash = cr1.hash
JOIN requests r2 ON r2.id = cr2.owner_id
WHERE cr1.owner_kind = 'request' AND cr1.owner_id = ?
  AND cr1.direction = 'request' AND cr1.role = 'user'
  AND cr2.owner_kind = 'request'
  AND cr2.direction = 'request' AND cr2.role = 'user'
  AND cr2.owner_id != ?
  AND r2.kind = 'client'
  AND (r2.request_kind IS NULL OR r2.request_kind != 'title')
GROUP BY r2.session_key
ORDER BY n DESC
LIMIT 1`

	err = r.db.QueryRowContext(ctx, contentHashQuery, requestID, requestID).Scan(&sk, &n)
	switch {
	case err == nil:
		return ParentSession{SessionKey: sk.String, Matches: n, Method: ParentMatchContentHash}, true, nil
	case errors.Is(err, sql.ErrNoRows):
		return ParentSession{}, false, nil
	default:
		return ParentSession{}, false, fmt.Errorf("parent session by content hash: %w", err)
	}
}
