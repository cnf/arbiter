package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
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
