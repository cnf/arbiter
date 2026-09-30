package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
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

// SessionsForContent returns, for one content block, the first request in
// each distinct session that contains it — newest session-activity first.
// This is the drill-down from a repeated block to the traffic it came from:
// a hash tells you nothing about which client sends it, and this answers "how
// many different sessions, and what did each first look like" rather than
// "how many times was it sent" (a client resends its whole history every
// turn, so counting raw references or even raw requests overstates spread —
// see RepeatedContent's own doc comment on why session count, not request
// count, is what drives the page).
//
// A request with no session_key cannot be grouped with anything, so each one
// is its own singleton "session" in the ranking below — it still shows up
// once, not deduplicated away.
//
// Implemented as a window function over the already-narrow set of requests
// that reference this hash (served by idx_content_refs_hash), not over the
// full requests table — ROW_NUMBER() partitioned by session_key (or the
// request's own id, when sessionless) picks the earliest request per
// partition, and the outer query keeps only that row.
func (r *Reader) SessionsForContent(ctx context.Context, hash string, limit int) ([]RequestRow, error) {
	raw, err := decodeHash(hash)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > maxRequestListLimit {
		limit = maxRequestListLimit
	}

	q := `
WITH ranked (id, trace_id, ts, ts_text, session_key, format, provider, model, actual_model,
             alias_used, routing_rationale, domain, effort, cost_class, input_tokens,
             output_tokens, cost_usd, latency_ms, status_code, error, stream, config_epoch,
             kind, request_kind, arrival_ts, arrival_ts_text, rn) AS (
    SELECT` + requestRowColumns + `,
           ROW_NUMBER() OVER (
             PARTITION BY COALESCE(NULLIF(session_key, ''), 'sessionless:' || id)
             ORDER BY arrival_ts ASC, id ASC
           )
    FROM requests
    WHERE kind = 'client' AND id IN (
        SELECT cr.owner_id FROM content_refs cr
        WHERE cr.owner_kind = 'request' AND cr.hash = ?
    )
)
SELECT id, trace_id, ts, ts_text, session_key, format, provider, model, actual_model,
       alias_used, routing_rationale, domain, effort, cost_class, input_tokens,
       output_tokens, cost_usd, latency_ms, status_code, error, stream, config_epoch,
       kind, request_kind, arrival_ts, arrival_ts_text
FROM ranked WHERE rn = 1
ORDER BY arrival_ts DESC, id DESC LIMIT ?`

	rows, err := r.db.QueryContext(ctx, q, raw, limit)
	if err != nil {
		return nil, fmt.Errorf("sessions for content: %w", err)
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
	// from drifting into a different definition of "repeated". The direction
	// filter must also mirror RepeatedContent's for the same reason — a count
	// that includes guardrailed/response rows would disagree with a list that
	// doesn't.
	const q = `
SELECT COUNT(*) AS total,
       COALESCE(SUM(CASE WHEN requests >= ? AND sessions >= ? THEN 1 ELSE 0 END), 0) AS matching
FROM (
    SELECT cr.hash,
           COUNT(DISTINCT cr.owner_id) AS requests,
           COUNT(DISTINCT r.session_key) AS sessions
    FROM content_refs cr
    JOIN requests r ON r.id = cr.owner_id AND cr.owner_kind = 'request'
    WHERE r.ts >= ? AND r.kind = 'client' AND cr.direction = 'request'
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

// DiscoveryState is an operator's seen/ignored mark on one repeated-content
// hash. See schema.sql's comment on discovery_state for why "unseen" is not a
// stored value: it is simply the absence of a row.
const (
	DiscoverySeen    = "seen"
	DiscoveryIgnored = "ignored"
)

// ErrBadDiscoveryState reports a state value that is neither "seen" nor
// "ignored" — the only two states this table ever stores (see schema.sql).
var ErrBadDiscoveryState = errors.New("bad discovery state")

// DiscoveryStates returns the stored seen/ignored mark for each of the given
// hashes, keyed by hex hash. A hash with no row (the common case — most
// blocks are never marked) is simply absent from the map; it is the caller's
// job to treat that as unseen, not this method's, because "unseen" is a
// derived default rather than a value the table stores.
func (r *Reader) DiscoveryStates(ctx context.Context, hexHashes []string) (map[string]DiscoveryMark, error) {
	out := map[string]DiscoveryMark{}
	if len(hexHashes) == 0 {
		return out, nil
	}

	placeholders := make([]string, len(hexHashes))
	args := make([]interface{}, len(hexHashes))
	for i, h := range hexHashes {
		raw, err := decodeHash(h)
		if err != nil {
			return nil, err
		}
		placeholders[i] = "?"
		args[i] = raw
	}

	q := `SELECT hash, state, marked_at_last_seen FROM discovery_state WHERE hash IN (` +
		strings.Join(placeholders, ",") + `)`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("discovery states: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var hash []byte
		var mark DiscoveryMark
		if err := rows.Scan(&hash, &mark.State, &mark.MarkedAtLastSeen); err != nil {
			return nil, fmt.Errorf("scan discovery state: %w", err)
		}
		out[hex.EncodeToString(hash)] = mark
	}
	return out, rows.Err()
}

// DiscoveryMark is one stored discovery_state row's payload (the hash itself
// is the map key DiscoveryStates returns it under).
type DiscoveryMark struct {
	State            string `json:"state"`
	MarkedAtLastSeen string `json:"marked_at_last_seen"`
}

// SetDiscoveryState marks hexHash seen or ignored, capturing lastSeen (the
// block's current RepeatedContent.LastSeen) so a later seen mark can tell
// "nothing new since I looked" from "this reappeared" — see schema.sql's
// comment on discovery_state. It is a single-row upsert run synchronously on
// the reader's own connection: this is an operator action from the admin UI,
// not request-path traffic, so it does not go through the writer's async
// event queue (which is explicitly allowed to drop under load — an operator
// click must not be).
func (r *Reader) SetDiscoveryState(ctx context.Context, hexHash, state, lastSeen string) error {
	if state != DiscoverySeen && state != DiscoveryIgnored {
		return fmt.Errorf("%w: %q", ErrBadDiscoveryState, state)
	}
	raw, err := decodeHash(hexHash)
	if err != nil {
		return err
	}
	const q = `
INSERT INTO discovery_state (hash, state, marked_at_last_seen)
VALUES (?, ?, ?)
ON CONFLICT(hash) DO UPDATE SET
    state               = excluded.state,
    marked_at_last_seen = excluded.marked_at_last_seen`
	if _, err := r.db.ExecContext(ctx, q, raw, state, lastSeen); err != nil {
		return fmt.Errorf("set discovery state for %q: %w", hexHash, err)
	}
	return nil
}

// ClearDiscoveryState removes any seen/ignored mark, returning the hash to
// unseen. Used by the state-cycle endpoint's third click (ignored -> unseen).
func (r *Reader) ClearDiscoveryState(ctx context.Context, hexHash string) error {
	raw, err := decodeHash(hexHash)
	if err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM discovery_state WHERE hash = ?`, raw); err != nil {
		return fmt.Errorf("clear discovery state for %q: %w", hexHash, err)
	}
	return nil
}

// UnseenDiscoveryCount reports how many content_hash_stats rows meet
// minSessions and are currently "unseen" — the same three-state derivation
// DiscoveryHandler applies per row (see its own comment in internal/ui):
// a hash is unseen when it carries no discovery_state row, or carries a
// 'seen' mark whose marked_at_last_seen has been overtaken by a newer
// last_ts (the pattern reappeared since the operator looked at it). An
// 'ignored' mark is never unseen, regardless of last_ts.
//
// Requests are deliberately not part of this gate: only minSessions bounds
// it. content_hash_stats.sessions >= 2 already implies requests >= 2 (each
// session contributes at least one request), so a separate request floor
// would filter nothing further here — it would just be a second knob for
// the same idea the session count already covers.
//
// Backs the nav bar's Discovery stat (Handler.base): a glanceable "how many
// patterns need a look" count, so this only counts rows — it never fetches
// the preview/body columns the ledger page itself needs.
func (r *Reader) UnseenDiscoveryCount(ctx context.Context, minSessions int) (int64, error) {
	const q = `
SELECT h.last_ts, d.state, d.marked_at_last_seen
FROM content_hash_stats h
LEFT JOIN discovery_state d ON d.hash = h.hash
WHERE h.sessions >= ?`

	rows, err := r.db.QueryContext(ctx, q, minSessions)
	if err != nil {
		return 0, fmt.Errorf("unseen discovery count: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var n int64
	for rows.Next() {
		var lastRaw interface{}
		var state, marked sql.NullString
		if err := rows.Scan(&lastRaw, &state, &marked); err != nil {
			return 0, fmt.Errorf("scan unseen discovery count: %w", err)
		}
		if !state.Valid {
			n++
			continue
		}
		if state.String == DiscoveryIgnored {
			continue
		}
		// state == DiscoverySeen: unseen again only if the pattern's own
		// last-seen has moved past the moment it was marked seen — both
		// sides are RFC3339 (formatTime's output, and marked_at_last_seen
		// is always captured from a prior formatTime'd LastSeen — see
		// SetDiscoveryState's caller), so a plain string compare orders
		// the same as the timestamps themselves.
		if marked.String < formatTime(lastRaw) {
			n++
		}
	}
	return n, rows.Err()
}

// BlockPosition is where one content hash sits inside one request: the
// (msg_index, position) pair GuardrailDiff needs to find the block's
// before/after text for that specific request.
type BlockPosition struct {
	MsgIndex int64
	Position int64
}

// PositionsForContent returns, for each request id, where the given hash
// first appears in that request's 'request' direction — the coordinates the
// block drill-down needs to link each row into GuardrailDiffHandler. One
// batched query keyed by owner_id rather than a query per row: the drill-down
// can list up to MaxRepeatedLimit requests, and this is the same reasoning
// RequestsForContent already applies to its own subquery.
//
// MIN(msg_index), MIN(position) picks a single position when a hash is
// referenced more than once inside one request (rare — a block resent
// verbatim within its own request) — a display link needs exactly one
// coordinate, and the guardrail-touched state does not vary across
// duplicate positions of the same content within one request.
func (r *Reader) PositionsForContent(ctx context.Context, hash string, ids []int64) (map[int64]BlockPosition, error) {
	out := map[int64]BlockPosition{}
	if len(ids) == 0 {
		return out, nil
	}
	raw, err := decodeHash(hash)
	if err != nil {
		return nil, err
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, raw)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := `
SELECT owner_id, MIN(msg_index), MIN(position)
FROM content_refs
WHERE hash = ? AND direction = 'request' AND owner_kind = 'request'
  AND owner_id IN (` + strings.Join(placeholders, ",") + `)
GROUP BY owner_id`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("positions for content: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id int64
		var p BlockPosition
		if err := rows.Scan(&id, &p.MsgIndex, &p.Position); err != nil {
			return nil, fmt.Errorf("scan block position: %w", err)
		}
		out[id] = p
	}
	return out, rows.Err()
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
