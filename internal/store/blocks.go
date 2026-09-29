package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

// ContentBlock is one stored block as read back for display: its address, its
// kind, and the body when it was captured (binary blocks are hash-only, so
// Body is empty for those while the reference still exists).
type ContentBlock struct {
	Hash      string `json:"hash"`
	Direction string `json:"direction"` // "request" | "response"
	MsgIndex  int64  `json:"msg_index"`
	Position  int64  `json:"position"`
	Role      string `json:"role,omitempty"`
	BlockType string `json:"block_type"`
	Body      string `json:"body,omitempty"`
	Captured  bool   `json:"captured"`

	// GuardrailTouched is true when a pre-guardrail ran on this request and
	// left a distinct byte-for-byte-different capture at this block's
	// (MsgIndex, Position) — the block the client sent differs from the block
	// that actually went upstream. It is cheap to compute (both directions
	// are already fetched by contentFor for every request) so it is set on
	// every block rather than only the system preamble, even though the
	// preamble is by far the common case in practice. The UI renders it as a
	// "guardrail touch" chip; the diff itself is fetched on demand via
	// GuardrailDiff, not eagerly — see #48.
	GuardrailTouched bool `json:"guardrail_touched,omitempty"`
}

// ContentForRequest returns the captured blocks belonging to one request, in
// conversation order, for the detail view, plus whether a distinct
// pre-guardrail capture exists at all (hasGuardrailedVariant) — the caller
// uses that to decide whether an "as sent" / "as guardrailed" toggle is
// worth showing. An unknown id yields an empty slice.
//
// When a pre-guardrail ran, the store holds two versions of the request
// direction: "request" (as the client sent it) and "request_guardrailed" (as
// it went upstream). By default this returns the guardrailed form when one
// was captured — it's what actually reached the model, and is the more
// useful default per #13 — falling back to "request" when no pre-guardrail
// applied (the common case, where the two would be identical anyway and only
// "request" was ever written). Pass showAsSent=true to see the client's
// original text instead; the response direction is unaffected either way.
func (r *Reader) ContentForRequest(ctx context.Context, id int64, showAsSent bool) ([]ContentBlock, bool, error) {
	rows, err := r.contentFor(ctx, "request", id)
	if err != nil {
		return nil, false, err
	}
	hasGuardrailed := false
	for _, b := range rows {
		if b.Direction == "request_guardrailed" {
			hasGuardrailed = true
			break
		}
	}
	return filterRequestDirection(rows, hasGuardrailed, showAsSent), hasGuardrailed, nil
}

// filterRequestDirection picks one request-direction view out of a raw block
// set that may contain both "request" and "request_guardrailed" rows,
// leaving "response" rows untouched. See ContentForRequest for the default
// (guardrailed-if-present) and showAsSent behavior.
//
// The kept rows are relabeled to Direction "request" regardless of which
// physical direction they came from: everything downstream (dedup-across-
// turns, preamble splitting in the session transcript, the "request" vs
// "response" grouping in the templates) only needs to know "this is the
// request side", not which of the two captures produced it. Without this
// relabel, any turn where a pre-guardrail actually ran would silently skip
// those checks — they all match on the literal string "request".
//
// It also marks GuardrailTouched on any kept block whose hash differs from
// its counterpart in the direction being dropped — the block the client sent
// is not byte-identical to what went upstream at that (MsgIndex, Position).
// This runs over data already fetched for every request, so it costs nothing
// extra to check every block rather than special-casing the system preamble,
// even though the preamble is by far the common real-world case (#48).
func filterRequestDirection(blocks []ContentBlock, hasGuardrailed, showAsSent bool) []ContentBlock {
	if !hasGuardrailed {
		// Nothing to filter: either no pre-guardrail ran, or capture never
		// wrote the second form. "request" is the only request-side data
		// there is, regardless of which view was asked for.
		return blocks
	}
	drop := "request"
	if showAsSent {
		drop = "request_guardrailed"
	}
	// otherHash indexes the dropped direction's blocks by position, so the
	// kept direction can tell "same bytes" from "guardrail touched" without a
	// second query.
	type key struct{ msgIndex, position int64 }
	otherHash := make(map[key]string, len(blocks))
	for _, b := range blocks {
		if b.Direction == drop {
			otherHash[key{b.MsgIndex, b.Position}] = b.Hash
		}
	}
	out := make([]ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		if b.Direction == drop {
			continue
		}
		if h, ok := otherHash[key{b.MsgIndex, b.Position}]; ok && h != b.Hash {
			b.GuardrailTouched = true
		}
		if b.Direction == "request_guardrailed" {
			b.Direction = "request"
		}
		out = append(out, b)
	}
	return out
}

// GuardrailDiff returns the before/after text for one request-side block, so
// the UI can render an inline line-level diff on demand — see #48. before is
// the client's original text (Direction "request"), after is what actually
// went upstream (Direction "request_guardrailed"). ok is false when either
// side is missing (no pre-guardrail ran on this request, no block at this
// position, or one side was not captured as text), in which case there is
// nothing to diff.
func (r *Reader) GuardrailDiff(ctx context.Context, id int64, msgIndex, position int64) (before, after string, ok bool, err error) {
	rows, err := r.contentFor(ctx, "request", id)
	if err != nil {
		return "", "", false, err
	}
	var haveBefore, haveAfter bool
	for _, b := range rows {
		if b.MsgIndex != msgIndex || b.Position != position {
			continue
		}
		switch b.Direction {
		case "request":
			before = b.Body
			haveBefore = b.Captured
		case "request_guardrailed":
			after = b.Body
			haveAfter = b.Captured
		}
	}
	return before, after, haveBefore && haveAfter, nil
}

// contentFor is the shared body behind ContentForRequest and (once rejected
// requests get rows) whatever promotes them: the owner is a (kind, id) pair,
// never a bare request id, which is what keeps that promotion a no-op here.
//
// This returns every captured block for the owner, unfiltered — callers like
// the public /admin/request/{id} JSON endpoint (stats.go) and lanePreview
// (sessions.go) need the full set, not just the transcript page's "newest
// message" view. The transcript page's own narrowing (only the newest
// resent message, or any role=system row) is pushed into SQL in
// ContentForRequests below instead, since that batched query has exactly one
// caller — the transcript page — and can safely assume that need.
func (r *Reader) contentFor(ctx context.Context, ownerKind string, ownerID int64) ([]ContentBlock, error) {
	const q = `
SELECT cr.hash, cr.direction, cr.msg_index, cr.position, COALESCE(cr.role, ''),
       cr.block_type, c.body
FROM content_refs cr
LEFT JOIN content c ON c.hash = cr.hash
WHERE cr.owner_kind = ? AND cr.owner_id = ?
ORDER BY cr.direction ASC, cr.msg_index ASC, cr.position ASC`

	rows, err := r.db.QueryContext(ctx, q, ownerKind, ownerID)
	if err != nil {
		return nil, fmt.Errorf("content for %s %d: %w", ownerKind, ownerID, err)
	}
	defer func() { _ = rows.Close() }()

	out := []ContentBlock{}
	for rows.Next() {
		var (
			b        ContentBlock
			hash     []byte
			body     []byte
			bodyNull sql.NullString
		)
		if err := rows.Scan(&hash, &b.Direction, &b.MsgIndex, &b.Position, &b.Role,
			&b.BlockType, &bodyNull); err != nil {
			return nil, fmt.Errorf("scan content block: %w", err)
		}
		// hash is a BLOB; render it as hex so it is usable as a URL segment and
		// comparable in JSON without a byte-array encoding.
		b.Hash = hex.EncodeToString(hash)
		if bodyNull.Valid {
			body = []byte(bodyNull.String)
		}
		b.Body = string(body)
		b.Captured = bodyNull.Valid
		out = append(out, b)
	}
	return out, rows.Err()
}

// ContentForRequests is ContentForRequest for a whole page of requests in one
// round trip: a transcript page previously ran one contentFor query per turn
// (an N+1 that dominated its load time once pages stopped being tiny — #59),
// where this runs the same content_refs scan once for every id in ids and
// buckets the rows in memory, exactly as SessionChildren already batches
// requests. The per-id default form matches ContentForRequest(id, false):
// guardrailed-if-present. Directions are relabeled and GuardrailTouched is
// computed per id the same way filterRequestDirection does, so callers get
// an identical result to calling ContentForRequest once per id.
//
// An id with no captured content at all (capture off, or a kind that was
// never captured) is simply absent from the returned map — the caller's
// existing "len(blocks) == 0" check on a missing map entry still reads as
// zero blocks.
func (r *Reader) ContentForRequests(ctx context.Context, ids []int64) (blocks map[int64][]ContentBlock, hasGuardrailed map[int64]bool, err error) {
	blocks = make(map[int64][]ContentBlock, len(ids))
	hasGuardrailed = make(map[int64]bool, len(ids))
	if len(ids) == 0 {
		return blocks, hasGuardrailed, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	idList := strings.Join(placeholders, ", ")
	// See contentFor for why this pushes "newest message per direction, or
	// role=system" down into SQL rather than fetching every resent row and
	// filtering in Go: same rule, applied per owner_id here instead of a
	// single id.
	q := `
WITH bounds AS (
  SELECT owner_id, direction, MAX(msg_index) AS max_idx
  FROM content_refs
  WHERE owner_kind = 'request' AND owner_id IN (` + idList + `)
  GROUP BY owner_id, direction
),
filtered AS (
  SELECT cr.owner_id, cr.hash, cr.direction, cr.msg_index, cr.position, cr.role, cr.block_type
  FROM content_refs cr
  LEFT JOIN bounds b ON b.owner_id = cr.owner_id AND b.direction = cr.direction
  WHERE cr.owner_kind = 'request' AND cr.owner_id IN (` + idList + `)
    AND (cr.direction = 'response' OR cr.role = 'system' OR cr.msg_index = b.max_idx)
)
SELECT f.owner_id, f.hash, f.direction, f.msg_index, f.position, COALESCE(f.role, ''), f.block_type, c.body
FROM filtered f
LEFT JOIN content c ON c.hash = f.hash
ORDER BY f.owner_id ASC, f.direction ASC, f.msg_index ASC, f.position ASC`

	allArgs := make([]interface{}, 0, len(args)*2)
	allArgs = append(allArgs, args...)
	allArgs = append(allArgs, args...)
	rows, err := r.db.QueryContext(ctx, q, allArgs...)
	if err != nil {
		return nil, nil, fmt.Errorf("content for requests: %w", err)
	}
	defer func() { _ = rows.Close() }()

	raw := make(map[int64][]ContentBlock, len(ids))
	for rows.Next() {
		var (
			ownerID  int64
			b        ContentBlock
			hash     []byte
			body     []byte
			bodyNull sql.NullString
		)
		if err := rows.Scan(&ownerID, &hash, &b.Direction, &b.MsgIndex, &b.Position, &b.Role,
			&b.BlockType, &bodyNull); err != nil {
			return nil, nil, fmt.Errorf("scan content block: %w", err)
		}
		b.Hash = hex.EncodeToString(hash)
		if bodyNull.Valid {
			body = []byte(bodyNull.String)
		}
		b.Body = string(body)
		b.Captured = bodyNull.Valid
		raw[ownerID] = append(raw[ownerID], b)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	for id, rs := range raw {
		hg := false
		for _, b := range rs {
			if b.Direction == "request_guardrailed" {
				hg = true
				break
			}
		}
		hasGuardrailed[id] = hg
		blocks[id] = filterRequestDirection(rs, hg, false)
	}
	return blocks, hasGuardrailed, nil
}
