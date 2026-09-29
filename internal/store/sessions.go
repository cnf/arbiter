package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SessionRequest is one turn of a session's trajectory.
type SessionRequest struct {
	ID      int64  `json:"id"`
	TraceID string `json:"trace_id"`
	Ts      string `json:"ts"`

	// ArrivalTs is when the request reached Arbiter — see RequestRow.ArrivalTs
	// and issue #8. This is what the trajectory is ordered by.
	ArrivalTs        string  `json:"arrival_ts,omitempty"`
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	AliasUsed        string  `json:"alias_used,omitempty"`
	RoutingRationale string  `json:"routing_rationale"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	LatencyMs        int64   `json:"latency_ms"`
	StatusCode       int64   `json:"status_code"`
	Error            string  `json:"error,omitempty"`
	Stream           bool    `json:"stream"`
	ToolCalls        string  `json:"tool_calls,omitempty"` // raw JSON array
	ConfigEpoch      string  `json:"config_epoch,omitempty"`
	// Kind is "client" or one of Arbiter's own internal kinds (see
	// store.Event.Kind) — shown on the session view so a classifier call is
	// visible in context, not hidden, just distinguishable.
	Kind string `json:"kind"`
}

// Session returns one conversation's requests in order — its trajectory. An
// unknown session key yields an empty slice, not an error.
func (r *Reader) Session(ctx context.Context, key string, limit int) ([]SessionRequest, error) {
	const q = `
SELECT
    id, trace_id, ts, arrival_ts, provider, model, alias_used, routing_rationale,
    input_tokens, output_tokens, cost_usd, latency_ms, status_code, error, stream,
    tool_calls_json, config_epoch, kind
FROM requests
WHERE session_key = ?
ORDER BY arrival_ts ASC
LIMIT ?`

	rows, err := r.db.QueryContext(ctx, q, key, limit)
	if err != nil {
		return nil, fmt.Errorf("session trajectory: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []SessionRequest{}
	for rows.Next() {
		var (
			s         SessionRequest
			tsRaw     interface{}
			arrivalTs interface{}
			alias     sql.NullString
			tools     sql.NullString
			epoch     sql.NullString
			errTx     sql.NullString
		)
		if err := rows.Scan(&s.ID, &s.TraceID, &tsRaw, &arrivalTs, &s.Provider, &s.Model, &alias,
			&s.RoutingRationale, &s.InputTokens, &s.OutputTokens, &s.CostUSD,
			&s.LatencyMs, &s.StatusCode, &errTx, &s.Stream, &tools, &epoch, &s.Kind); err != nil {
			return nil, fmt.Errorf("scan session row: %w", err)
		}
		s.Ts = formatTime(tsRaw)
		s.ArrivalTs = formatTime(arrivalTs)
		s.AliasUsed = alias.String
		s.Error = errTx.String
		s.ToolCalls = tools.String
		s.ConfigEpoch = epoch.String
		out = append(out, s)
	}
	return out, rows.Err()
}

// SessionClientPage returns one page of a session's client requests in
// conversation order — oldest first — starting at offset.
//
// It is a separate method from Session rather than a flag on it, because the
// two answer different questions and return different shapes. Session returns
// the thinner SessionRequest for the JSON trajectory API and for "where did
// this request sit in its conversation"; this returns the full list projection
// the transcript's inspector reads (domain, effort, cost_class, actual_model,
// request_kind — none of which SessionRequest carries), because the transcript
// page is a browsing view over one conversation and needs the same per-row
// facts the flat requests list shows.
//
// Client rows only, and that is what makes the transcript's own numbering
// work: the page numbers each conversation *turn*, and a classifier call is
// not a turn — it is something a turn did. Its children are attached by
// trace_id afterwards (see SessionChildren), not by sharing a page of rows, so
// a classifier that finished *before* its parent (the documented #8 case) can
// never end up numbered as a turn or stranded on the previous page.
//
// Offset paging rather than a keyset cursor is deliberate here, and safe for
// the same reason it would be wrong on the newest-first requests list: a
// conversation only ever grows at its tail, so a new turn always arrives with
// a greater (ts, id) than everything already stored. New turns land beyond the
// last page a reader has already seen, never behind it, so an offset cannot be
// invalidated by traffic the way it would be on a list ordered newest-first.
func (r *Reader) SessionClientPage(ctx context.Context, key string, limit, offset int) ([]RequestRow, error) {
	if limit <= 0 {
		limit = maxRequestListLimit
	}
	if limit > maxRequestListLimit {
		limit = maxRequestListLimit
	}
	if offset < 0 {
		offset = 0
	}
	// id is the tiebreaker for the same reason ListRequests uses it:
	// arrival_ts has sub-second precision and several turns can share a tick.
	const q = `SELECT` + requestRowColumns + `
FROM requests WHERE session_key = ? AND kind = 'client'
ORDER BY arrival_ts ASC, id ASC
LIMIT ? OFFSET ?`

	rows, err := r.db.QueryContext(ctx, q, key, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("session transcript: %w", err)
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

// SessionClientPageBefore returns up to limit client requests immediately
// before beforeOffset in conversation order — oldest first, same shape as
// SessionClientPage — for the transcript's "load older" direction (#73). It
// also returns the offset the returned rows start at, since the caller (the
// transcript handler) needs it to number turns the same way SessionClientPage's
// own offset argument does — the two must never number rows differently for
// the same underlying query.
//
// beforeOffset is a position in the conversation's client-ordered sequence
// (0-based, the same unit SessionClientPage's offset already uses), not a
// row id: the caller always has "how far into the conversation am I" at
// hand (the lowest Seq currently on the page) and never needs a second way
// to name a position.
//
// This is deliberately just SessionClientPage with an earlier offset and a
// shorter limit, not a new query: the doc comment on SessionClientPage
// already establishes that a plain offset is stable here (a conversation
// only ever grows at its tail), and that holds just as well counting
// backward from an already-known position as it does counting forward from
// the start — the position itself does not move, only what may exist beyond
// it. A DESC-ordered query anchored at the *table's* end and OFFSET back
// from there would not have this property (the offset would shift as the
// conversation grows); computing an earlier ASC window sidesteps that
// entirely.
func (r *Reader) SessionClientPageBefore(ctx context.Context, key string, limit, beforeOffset int) (rows []RequestRow, offset int, err error) {
	if limit <= 0 {
		limit = maxRequestListLimit
	}
	if limit > maxRequestListLimit {
		limit = maxRequestListLimit
	}
	if beforeOffset <= 0 {
		return []RequestRow{}, 0, nil
	}
	offset = beforeOffset - limit
	if offset < 0 {
		offset = 0
	}
	rows, err = r.SessionClientPage(ctx, key, beforeOffset-offset, offset)
	return rows, offset, err
}

// SessionChildren returns the non-client rows (a classifier call today,
// title-gen/subagent later) whose trace_id is one of traceIDs — the internal
// calls that ran inside the given client requests.
//
// Keyed on trace_id rather than on "the rows sharing this page" precisely so
// the child's own timestamp is irrelevant: a classifier call routinely
// finishes before the request that spawned it, which is the whole reason #8
// nests by trace rather than sorting by time. One query for a whole page's
// worth of parents, so attaching children costs a single round trip, not one
// per row.
func (r *Reader) SessionChildren(ctx context.Context, traceIDs []string) ([]RequestRow, error) {
	if len(traceIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(traceIDs))
	args := make([]interface{}, 0, len(traceIDs))
	for i, id := range traceIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := `SELECT` + requestRowColumns + `
FROM requests WHERE kind <> 'client' AND trace_id IN (` + strings.Join(placeholders, ", ") + `)
ORDER BY arrival_ts ASC, id ASC`

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("session children: %w", err)
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

// SessionTurnAt resolves a turn number to its request id: turn numbers are
// 1-based positions in the conversation's client-ordered sequence, so turn N is
// simply the N-th client row. Returning the id rather than the row is what
// lets the caller load the page turn N falls on in one step — it needs to know
// which page that is before it reads any rows.
//
// The offset scan is cheap here in a way it would not be for the flat request
// list: a conversation is bounded and appends only at its tail, so a position
// in it is stable, and the query is an indexed range on one session key.
func (r *Reader) SessionTurnAt(ctx context.Context, key string, turn int) (int64, bool, error) {
	if turn < 1 {
		return 0, false, nil
	}
	const q = `SELECT id FROM requests
WHERE session_key = ? AND kind = 'client'
ORDER BY arrival_ts ASC, id ASC
LIMIT 1 OFFSET ?`

	var id int64
	err := r.db.QueryRowContext(ctx, q, key, turn-1).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("session turn at: %w", err)
	}
	return id, true, nil
}

// SessionTurnForRequest resolves the turn a request id belongs to, for the
// Sessions lane list's "open in transcript" links: a client row's turn is its
// own position; a non-client row (a classifier call, a title-gen call — see
// requests.kind) has no position of its own and resolves through its
// trace_id to the client row that spawned it, exactly as attachTraceChildren
// nests it in the UI. ok is false when the request id does not exist, or
// belongs to a session-less request, or (rare — a client row was never
// written, e.g. a very old pre-#5 rejection) a non-client row whose trace has
// no client row to resolve through.
//
// Returns the session key alongside the turn number because the caller (the
// lane list) has the id but not necessarily the parent session's key at hand
// for a satellite node — SessionHandler needs both to build ?key=&seq=.
func (r *Reader) SessionTurnForRequest(ctx context.Context, id int64) (key string, turn int, ok bool, err error) {
	var (
		session sql.NullString
		kind    string
		traceID string
	)
	err = r.db.QueryRowContext(ctx,
		`SELECT session_key, kind, trace_id FROM requests WHERE id = ?`, id,
	).Scan(&session, &kind, &traceID)
	if err == sql.ErrNoRows {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, fmt.Errorf("session turn for request: %w", err)
	}
	if !session.Valid || session.String == "" {
		return "", 0, false, nil
	}
	key = session.String

	targetID := id
	if kind != "client" {
		if traceID == "" {
			return "", 0, false, nil
		}
		var parentID int64
		err = r.db.QueryRowContext(ctx,
			`SELECT id FROM requests WHERE session_key = ? AND kind = 'client' AND trace_id = ?
LIMIT 1`, key, traceID,
		).Scan(&parentID)
		if err == sql.ErrNoRows {
			return "", 0, false, nil
		}
		if err != nil {
			return "", 0, false, fmt.Errorf("session turn for request (parent lookup): %w", err)
		}
		targetID = parentID
	}

	// The turn is the target client row's rank among the session's client
	// rows ordered (ts ASC, id ASC) — the same ordering SessionClientPage
	// and SessionTurnAt use elsewhere in this file. The ts comparison stays
	// inside a subquery rather than round-tripping the target's ts through
	// Go as a string: modernc.org/sqlite scans a TIMESTAMP column back as
	// RFC3339 but stores it in time.Time's default String() layout, so a
	// value read out and compared back in via `WHERE ts = ?` silently
	// mismatches format even though it's the same instant.
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM requests r2
WHERE r2.session_key = ? AND r2.kind = 'client'
  AND (r2.ts < (SELECT ts FROM requests WHERE id = ?)
       OR (r2.ts = (SELECT ts FROM requests WHERE id = ?) AND r2.id <= ?))`,
		key, targetID, targetID, targetID,
	).Scan(&n); err != nil {
		return "", 0, false, fmt.Errorf("session turn for request (count): %w", err)
	}
	if n == 0 {
		return "", 0, false, nil
	}
	return key, n, true, nil
}

// SessionTotals is a session's headline numbers: how many turns, how many of
// them failed, what the whole conversation cost, and how many tokens it moved.
//
// Turns and Errors count client rows only — the conversation's own turns, the
// same unit the transcript list numbers. Cost and Tokens cover every row in
// the session, because a classifier call is real spend and hiding it would
// make the header disagree with the bill.
type SessionTotals struct {
	Turns   int64   `json:"turns"`
	Errors  int64   `json:"errors"`
	Tokens  int64   `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
}

// SessionTotals computes one session's headline numbers. It is deliberately
// not windowed: a transcript is a conversation, and a conversation that
// half-falls outside a window is not half a conversation. (The sessions
// *index* is windowed for the opposite reason — see SessionSummary.)
func (r *Reader) SessionTotals(ctx context.Context, key string) (SessionTotals, error) {
	const q = `
SELECT
    CAST(COALESCE(SUM(CASE WHEN kind = 'client' THEN 1 ELSE 0 END), 0) AS INTEGER),
    CAST(COALESCE(SUM(CASE WHEN kind = 'client' AND status_code >= 400 THEN 1 ELSE 0 END), 0) AS INTEGER),
    CAST(COALESCE(SUM(input_tokens + output_tokens), 0) AS INTEGER),
    COALESCE(SUM(cost_usd), 0)
FROM requests WHERE session_key = ?`

	var t SessionTotals
	if err := r.db.QueryRowContext(ctx, q, key).Scan(&t.Turns, &t.Errors, &t.Tokens, &t.CostUSD); err != nil {
		return SessionTotals{}, fmt.Errorf("session totals: %w", err)
	}
	return t, nil
}

// SessionSummary is one conversation as the sessions index lists it: how many
// turns, how long it spanned, what it cost, and which providers served it.
//
// Turns and the cost/token sums are computed over the query's *window*, not
// over the session's whole life. A conversation straddling the window boundary
// therefore reports fewer turns than it had and a FirstSeen at the window edge,
// which is the right trade for an index (the alternative — a per-session
// subquery over all time — throws away idx_requests_session) but must be said
// out loud by whatever renders it. The transcript view is unbounded, so the two
// can legitimately disagree.
type SessionSummary struct {
	Key          string  `json:"key"`
	Turns        int64   `json:"turns"`
	FirstSeen    string  `json:"first_seen"`
	LastSeen     string  `json:"last_seen"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Errors       int64   `json:"errors"`
	Providers    string  `json:"providers"` // comma-joined distinct providers, as group_concat emits them
	Models       int64   `json:"models"`    // distinct provider/model pairs
}

// Sessions lists conversations over a window, most recently active first.
//
// Requests with no session key are excluded, not grouped: they are not one
// conversation, and lumping them together would produce a fake session whose
// transcript is unrelated requests. They are not hidden either — the index
// carries their count as its own row (SessionlessRequestCount), because a
// conversation whose opening turn was too short to pin records a NULL key on
// every turn and would otherwise vanish from the only view built for reading
// conversations.
func (r *Reader) Sessions(ctx context.Context, w Window, limit int) ([]SessionSummary, error) {
	if limit <= 0 || limit > MaxSessionListLimit {
		limit = MaxSessionListLimit
	}
	// group_concat(DISTINCT x) cannot take a custom separator in sqlite; the
	// default comma is what Providers carries.
	const q = `
SELECT session_key,
    COUNT(*),
    MIN(arrival_ts), MAX(arrival_ts),
    CAST(COALESCE(SUM(input_tokens), 0)  AS INTEGER),
    CAST(COALESCE(SUM(output_tokens), 0) AS INTEGER),
    COALESCE(SUM(cost_usd), 0),
    CAST(COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END), 0) AS INTEGER),
    COALESCE(group_concat(DISTINCT provider), ''),
    COUNT(DISTINCT provider || '/' || model)
FROM requests
WHERE arrival_ts >= ? AND session_key IS NOT NULL AND session_key <> '' AND kind = 'client'
GROUP BY session_key
ORDER BY MAX(arrival_ts) DESC
LIMIT ?`

	rows, err := r.db.QueryContext(ctx, q, w.Since, limit)
	if err != nil {
		return nil, fmt.Errorf("sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []SessionSummary{}
	for rows.Next() {
		var (
			s              SessionSummary
			first, lastRaw interface{}
		)
		if err := rows.Scan(&s.Key, &s.Turns, &first, &lastRaw, &s.InputTokens,
			&s.OutputTokens, &s.CostUSD, &s.Errors, &s.Providers, &s.Models); err != nil {
			return nil, fmt.Errorf("scan session summary: %w", err)
		}
		s.FirstSeen = formatTime(first)
		s.LastSeen = formatTime(lastRaw)
		out = append(out, s)
	}
	return out, rows.Err()
}

// SessionSummaryFor is Sessions' per-row aggregate for exactly one session
// key, over the same window shape (ts >= since, kind = 'client').
//
// It exists for the live-tail poller (see ui.SessionsTailHandler): Sessions
// itself is a bulk, windowed group-by over every conversation, which is the
// right query for a full page load but the wrong one for "refresh this one
// lane whose pin just moved" — running the whole index every 5 seconds to
// pick one row back out of it would scale with total session count instead
// of with the (small, single-digit) number of currently-pinned sessions a
// poll actually needs. ok is false when the session has no client rows in
// the window at all (a pin can outlive the window that produced it, or name
// a session whose only rows are non-client and therefore excluded here, same
// as Sessions).
func (r *Reader) SessionSummaryFor(ctx context.Context, key string, since time.Time) (SessionSummary, bool, error) {
	const q = `
SELECT session_key,
    COUNT(*),
    MIN(arrival_ts), MAX(arrival_ts),
    CAST(COALESCE(SUM(input_tokens), 0)  AS INTEGER),
    CAST(COALESCE(SUM(output_tokens), 0) AS INTEGER),
    COALESCE(SUM(cost_usd), 0),
    CAST(COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END), 0) AS INTEGER),
    COALESCE(group_concat(DISTINCT provider), ''),
    COUNT(DISTINCT provider || '/' || model)
FROM requests
WHERE arrival_ts >= ? AND session_key = ? AND kind = 'client'
GROUP BY session_key`

	var (
		s              SessionSummary
		first, lastRaw interface{}
	)
	err := r.db.QueryRowContext(ctx, q, since, key).Scan(&s.Key, &s.Turns, &first, &lastRaw,
		&s.InputTokens, &s.OutputTokens, &s.CostUSD, &s.Errors, &s.Providers, &s.Models)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionSummary{}, false, nil
	}
	if err != nil {
		return SessionSummary{}, false, fmt.Errorf("session summary for %q: %w", key, err)
	}
	s.FirstSeen = formatTime(first)
	s.LastSeen = formatTime(lastRaw)
	return s, true, nil
}

// SessionlessRequestCount counts the requests in a window that have no session
// key at all. It is what keeps those requests visible in the sessions index
// rather than silently absent from it.
func (r *Reader) SessionlessRequestCount(ctx context.Context, w Window) (int64, error) {
	const q = `SELECT COUNT(*) FROM requests
WHERE arrival_ts >= ? AND (session_key IS NULL OR session_key = '') AND kind = 'client'`

	var n int64
	if err := r.db.QueryRowContext(ctx, q, w.Since).Scan(&n); err != nil {
		return 0, fmt.Errorf("sessionless request count: %w", err)
	}
	return n, nil
}

// ActiveSessionCount counts sessions with a live affinity pin — i.e. still
// inside the TTL that keeps them routed to the same provider/model for
// prompt-cache reuse (see pipeline.affinityStore, affinity_pins.expires_at).
// It backs the nav bar's "N active" Sessions stat (see Handler.base): "active"
// means "still within its cache TTL", not merely "had a request recently" —
// a session can easily see no traffic for hours and still be a real ongoing
// conversation, while an expired pin means the next turn (if there is one)
// re-routes from scratch anyway. This is the one nav-item number that is a
// real query rather than the header's other "fake live" literals
// (design/REDESIGN.md §8 item 5), per an explicit user callout that the
// sessions count specifically should stop being a placeholder.
//
// COUNT(DISTINCT session_key), not COUNT(*): affinity_pins can hold several
// rows per session_key since prompt_hash separates prompt families (the main
// thread, a title call, a subagent run) sharing one session — a plain
// COUNT(*) would count one session multiple times.
//
// A store with no affinity_pins rows (session affinity never pinned anything,
// or nothing is currently live) reports 0, not an error — an empty table is
// a legitimate steady state, not a broken query.
func (r *Reader) ActiveSessionCount(ctx context.Context) (int64, error) {
	const q = `SELECT COUNT(DISTINCT session_key) FROM affinity_pins WHERE expires_at >= ?`

	var n int64
	if err := r.db.QueryRowContext(ctx, q, time.Now().UTC()).Scan(&n); err != nil {
		return 0, fmt.Errorf("active session count: %w", err)
	}
	return n, nil
}

// SessionPinExpiry returns every session's LATEST pin expiry, for sessions
// with at least one pin that expired no earlier than since — i.e. still live,
// or expired but recently enough to be within a caller-chosen grace window.
// It is the one query that answers both "is this session still active"
// (expires_at in the result is >= now) and "is this session still within its
// post-expiry grace window" (present in the result at all), without a second
// round trip for each question.
//
// This is the Sessions lane list's data source for both the "hot" dot and
// the "live only" filter: a lane whose key is absent from the result (its
// pin expired earlier than since, or it was never pinned) drops out of the
// filtered view entirely, while one still present but past its own expiry
// lingers — see sessions.go's liveGraceWindow for why a session does not
// vanish from the list the instant its pin expires.
//
// MAX(expires_at) grouped by session_key, not a per-row map: a session can
// hold several pins (one per prompt family — main thread, title calls,
// subagent runs), and the lane must read as live while ANY of them still is,
// not whichever row happened to be scanned last.
//
// A store with no matching affinity_pins rows returns an empty, non-nil map
// — the caller does not need to special-case "nothing is live" separately
// from "the query failed".
func (r *Reader) SessionPinExpiry(ctx context.Context, since time.Time) (map[string]time.Time, error) {
	const q = `
SELECT session_key, MAX(expires_at)
FROM affinity_pins
WHERE expires_at >= ?
GROUP BY session_key`

	rows, err := r.db.QueryContext(ctx, q, since.UTC())
	if err != nil {
		return nil, fmt.Errorf("session pin expiry: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]time.Time{}
	for rows.Next() {
		var key, rawExpiresAt string
		if err := rows.Scan(&key, &rawExpiresAt); err != nil {
			return nil, fmt.Errorf("scan session pin expiry: %w", err)
		}
		expiresAt, ok := ParseStoredTime(rawExpiresAt)
		if !ok {
			continue
		}
		out[key] = expiresAt
	}
	return out, rows.Err()
}
