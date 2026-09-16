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
WHERE id IN (
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
    WHERE r.ts >= ?
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
