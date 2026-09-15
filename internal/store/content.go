package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cnf/arbiter/pkg/types"
)

// Block is one captured piece of a request or response, already reduced to the
// bytes that get hashed. Body is nil when the content exists but isn't stored
// (binary/image blocks are hash-only by decision), in which case the reference
// is still written and still participates in dedup — only the bytes are
// missing.
type Block struct {
	Kind string // "text" | "tool_use" | "tool_result" | "thinking" | ...
	Body []byte

	// Role is the owning message's role, denormalized onto the reference row so
	// "the system text every client prepends" can be grouped without joining
	// back to anything.
	Role string

	// MsgIndex and Position locate the block in the conversation, so a request
	// can be reassembled in order.
	MsgIndex int
	Position int
}

// Hash is the content address: 32-byte SHA-256 of the canonical body.
func (b Block) Hash() []byte {
	sum := sha256.Sum256(b.Body)
	return sum[:]
}

// CapturedContent is the content of one request/response, split into the
// request and response directions. Either may be empty.
type CapturedContent struct {
	Request  []Block
	Response []Block
}

// Empty reports whether there is nothing to store, so the writer can skip the
// whole content path and keep its transaction free of work in the common case
// where capture is off or produced nothing.
func (c CapturedContent) Empty() bool {
	return len(c.Request) == 0 && len(c.Response) == 0
}

// CaptureRequest reduces a normalized request's messages to hashable blocks.
//
// Must be called on the request as it arrived, BEFORE pre-guardrails: a
// guardrail like system_prompt rewrites SystemPrompt, and capturing after it
// would store Arbiter's own injected prompt as though the client had sent it —
// which is exactly the thing the dedup queries exist to detect.
func CaptureRequest(req *types.NormalizedRequest) []Block {
	var out []Block
	msgIndex := 0

	// The system prompt is kept as its own "message" at index 0 so its text
	// hashes by itself. That is deliberate: a client prepending a constant
	// block of instructions is the canonical thing to detect, and it only
	// groups cleanly if it isn't concatenated with a user turn.
	if req.SystemPrompt != "" {
		out = append(out, Block{
			Kind:     "text",
			Body:     []byte(req.SystemPrompt),
			Role:     "system",
			MsgIndex: 0,
			Position: 0,
		})
		msgIndex = 1
	}

	for i, m := range req.Messages {
		for position, cb := range m.Content {
			kind, body, ok := blockBody(cb)
			if !ok {
				// A block with nothing hashable (an empty text block, say) is
				// skipped rather than stored as a constant empty string that
				// would then dedup against every other empty block, making the
				// "appears everywhere" query useless.
				continue
			}
			out = append(out, Block{
				Kind:     kind,
				Body:     body,
				Role:     m.Role,
				MsgIndex: msgIndex + i,
				// The block's real index within its message, not a running
				// counter over stored blocks: a rebuild then still knows where a
				// skipped or uncaptured block sat.
				Position: position,
			})
		}
	}
	return out
}

// CaptureResponse reduces a non-streaming response's content blocks, mirroring
// CaptureRequest's indexing so both directions reconstruct the same way.
func CaptureResponse(resp *types.NormalizedResponse, role string) []Block {
	if resp == nil {
		return nil
	}
	var out []Block
	for position, cb := range resp.Content {
		kind, body, ok := blockBody(cb)
		if !ok {
			continue
		}
		out = append(out, Block{
			Kind:     kind,
			Body:     body,
			Role:     role,
			MsgIndex: 0,
			Position: position,
		})
	}
	return out
}

// blockBody reduces a ContentBlock to (kind, canonical bytes).
//
// Text and thinking hash over their raw text, not over JSON, so that
// "this exact string appears in every query" is expressible as a single hash
// comparison — the whole point of the dedup queries. Structured blocks hash
// over canonical JSON instead, so two tool calls with the same name and
// arguments dedup regardless of how they were ordered on the wire.
func blockBody(cb types.ContentBlock) (kind string, body []byte, ok bool) {
	switch cb.Type {
	case "text":
		if cb.Text == "" {
			return "", nil, false
		}
		return "text", []byte(cb.Text), true

	case "thinking":
		if cb.Text == "" {
			return "", nil, false
		}
		return "thinking", []byte(cb.Text), true

	case "tool_use":
		canonical, err := json.Marshal(struct {
			ID    string                 `json:"id"`
			Name  string                 `json:"name"`
			Input map[string]interface{} `json:"input"`
		}{cb.ToolUseID, cb.ToolName, cb.ToolInput})
		if err != nil {
			return "", nil, false
		}
		return "tool_use", canonical, true

	case "tool_result":
		canonical, err := json.Marshal(struct {
			ForID   string `json:"for_id"`
			Content string `json:"content"`
			IsError bool   `json:"is_error"`
		}{cb.ToolResultForID, cb.ToolResult, cb.ToolIsError})
		if err != nil {
			return "", nil, false
		}
		return "tool_result", canonical, true

	default:
		// Any other block type (image and friends) is hash-only: the reference
		// is still recorded so the request reconstructs, but the bytes are not
		// stored. Body stays nil, which is what makes the hash of an
		// uncaptured block the hash of its (absent) canonical form.
		return cb.Type, nil, cb.Type != ""
	}
}

// Note on retention: content is the only thing in this store on a clock. Request
// metadata (routing, cost, epoch) is never expired, because it stays useful for
// months while conversation text does not. One TTL could not express both.

// SweepContent deletes captured content past its TTL and then removes any
// content row no longer referenced. Both statements run in one transaction so a
// crash cannot leave bodies with no references or references with no bodies.
//
// A method on Reader rather than a free function because in production the sweep
// runs on the reader's connection: reclaiming content must not contend with the
// writer's drain goroutine.
//
// The two-step shape is the whole GC design: there is no refcount to keep
// correct, just "delete the old, then delete whatever that orphaned".
// Because a conversation re-references its earlier blocks on every turn, a
// conversation that stops being used fails the TTL on all its blocks at once
// and drops out wholesale — a half-deleted conversation would be useless
// anyway.
func (r *Reader) SweepContent(ctx context.Context, ttl time.Duration) (bodies, refs int64, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin content sweep: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if ttl > 0 {
		cutoff := time.Now().UTC().Add(-ttl)
		// A reference is stale when its request is older than the cutoff. Only
		// rows whose owner still exists are considered; a rejection whose blobs
		// are long gone has nothing left to expire.
		res, err := tx.ExecContext(ctx, `
DELETE FROM content_refs
WHERE hash IN (
    SELECT cr.hash FROM content_refs cr
    WHERE cr.owner_kind = 'request'
      AND EXISTS (
          SELECT 1 FROM requests r
          WHERE r.id = cr.owner_id AND r.ts < ?
      )
)`, cutoff)
		if err != nil {
			return 0, 0, fmt.Errorf("expire content refs: %w", err)
		}
		refs, _ = res.RowsAffected()
	}

	res, err := tx.ExecContext(ctx, `
DELETE FROM content
WHERE hash NOT IN (SELECT hash FROM content_refs)`)
	if err != nil {
		return 0, 0, fmt.Errorf("sweep orphaned content: %w", err)
	}
	bodies, _ = res.RowsAffected()

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit content sweep: %w", err)
	}
	return bodies, refs, nil
}

// ForgetRequests drops the content references belonging to the given request
// ids and then orphans-sweeps, which is what "forget this conversation" is.
// The requests rows themselves are left alone — metadata outlives content.
func (r *Reader) ForgetRequests(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin forget: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM content_refs WHERE owner_kind = 'request' AND owner_id = ?`, id); err != nil {
			return 0, fmt.Errorf("forget request %d: %w", id, err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM content WHERE hash NOT IN (SELECT hash FROM content_refs)`)
	if err != nil {
		return 0, fmt.Errorf("sweep after forget: %w", err)
	}
	n, _ := res.RowsAffected()

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit forget: %w", err)
	}
	return n, nil
}

// writeContent inserts the blocks and their references inside an existing
// transaction. Blocks already present are left as they are: content is
// immutable by construction (the address IS the content), so a repeat insert
// has nothing to update. A block with no stored body still gets its reference,
// which is what keeps hash-only blocks deduping.
func writeContent(ctx context.Context, tx *sql.Tx, ownerKind string, ownerID int64, content CapturedContent) error {
	insertBlock, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO content (hash, kind, body) VALUES (?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare content insert: %w", err)
	}
	defer func() { _ = insertBlock.Close() }()

	insertRef, err := tx.PrepareContext(ctx, `
INSERT INTO content_refs
    (owner_kind, owner_id, direction, msg_index, position, role, block_type, hash)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare content ref insert: %w", err)
	}
	defer func() { _ = insertRef.Close() }()

	write := func(direction string, blocks []Block) error {
		for _, b := range blocks {
			hash := b.Hash()
			if _, err := insertBlock.ExecContext(ctx, hash, b.Kind, b.Body); err != nil {
				return fmt.Errorf("insert content block (%s): %w", b.Kind, err)
			}
			if _, err := insertRef.ExecContext(ctx, ownerKind, ownerID, direction,
				b.MsgIndex, b.Position, b.Role, b.Kind, hash); err != nil {
				return fmt.Errorf("insert content ref (%s): %w", b.Kind, err)
			}
		}
		return nil
	}

	if err := write("request", content.Request); err != nil {
		return err
	}
	return write("response", content.Response)
}
