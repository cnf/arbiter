package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Reader answers questions about recorded requests. It is a thin layer over
// the same sqlite database the writer feeds, opening its own handle so a read
// never contends with the writer's single drain goroutine.
//
// The queries live here as plain Go rather than in a generated layer or a
// separate .sql file. An earlier version generated them with sqlc; that was
// retired after the read side turned out to need json_each(), a table-valued
// function sqlc's sqlite parser cannot resolve (see README's "Event store").
// Five queries do not warrant a codegen pipeline.
type Reader struct {
	db *sql.DB
}

// OpenReader opens the event store at path for reading. It does not create or
// migrate the schema — the writer owns that; a read against a database that
// was never written to simply returns empty results.
func OpenReader(path string) (*Reader, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open event store for reading %q: %w", path, err)
	}
	return &Reader{db: db}, nil
}

// Close releases the reader's database handle.
func (r *Reader) Close() error { return r.db.Close() }

// Window is a time range for a query. Since is the inclusive lower bound;
// Until is the *exclusive* upper bound, and a zero Until means "open ended" —
// every row at or after Since, which is what every caller predating the
// Overview page's compare mode wants and gets without saying so.
//
// The half-open shape [Since, Until) is deliberate: it makes two adjacent
// windows tile without double-counting the row that sits exactly on the
// boundary, which is precisely what compare mode does (an anchor's before-side
// ends where its after-side begins). A closed upper bound would count that row
// twice and quietly inflate both sides of every delta.
//
// Both bounds bind as time.Time. The ts column is TEXT holding Go's
// time.Time.String() layout, and the sqlite driver binds a time.Time to that
// same layout, so string comparison and chronological order agree — verified
// against the live store before Until was added (a bound at a known instant
// partitions the table exactly: the >= and < counts sum to the total).
type Window struct {
	Since time.Time
	Until time.Time
}

// WindowFrom turns an operator-friendly duration ("24h", "7d" handled by the
// caller) into a window ending now. The upper bound is left zero rather than
// set to time.Now(): "now" moves between the call and the query, and an
// open-ended window is both cheaper and exactly what a trailing window means.
func WindowFrom(d time.Duration) Window {
	return Window{Since: time.Now().UTC().Add(-d)}
}

// Bounded reports whether the window has an upper bound at all, so a query
// builder can skip the clause instead of binding a zero time that would match
// nothing.
func (w Window) Bounded() bool { return !w.Until.IsZero() }

// tsClause renders the window's own SQL predicate against a ts column, plus
// the args to bind for it. The column is named by the caller because some
// queries join and must qualify it (`r.ts`) while most do not.
//
// It exists so the half-open convention above is written once. Every query
// that grew an upper bound got it by calling this rather than by hand-editing
// its WHERE, which is what keeps "exclusive upper bound" a property of the
// type instead of a habit each query might break.
func (w Window) tsClause(col string) (string, []interface{}) {
	if !w.Bounded() {
		return col + " >= ?", []interface{}{w.Since}
	}
	return col + " >= ? AND " + col + " < ?", []interface{}{w.Since, w.Until}
}

// OverallStats is the headline over a window.
type OverallStats struct {
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	AvgLatencyMs int64   `json:"avg_latency_ms"`
	Errors       int64   `json:"errors"`
}

// ProviderStats is spend and volume for one provider/model pair.
type ProviderStats struct {
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	AvgLatencyMs int64   `json:"avg_latency_ms"`
}

// EpochStats is spend and volume for one config epoch.
//
// AvgCostUSD is the field that makes epochs comparable: a config that was live
// for two days and one live for two hours will always differ in raw spend, but
// cost-per-request answers "did this change make each call cheaper?".
type EpochStats struct {
	ConfigEpoch  string  `json:"config_epoch"` // "" when the column is NULL (pre-epoch rows)
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	AvgCostUSD   float64 `json:"avg_cost_usd"`
	AvgLatencyMs int64   `json:"avg_latency_ms"`
	FirstSeen    string  `json:"first_seen"`
	LastSeen     string  `json:"last_seen"`
}

// SessionRequest is one turn of a session's trajectory.
type SessionRequest struct {
	ID               int64   `json:"id"`
	TraceID          string  `json:"trace_id"`
	Ts               string  `json:"ts"`
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

// ToolStat is how many times one tool name appeared across a window.
type ToolStat struct {
	Tool string `json:"tool"`
	Uses int64  `json:"uses"`
}

// RequestFilter selects rows for the request list. Every field is optional;
// the zero value means "no restriction". Empty strings and 0 therefore cannot
// be asked for as *values* (a status of 0, a provider named ""), which is
// correct for this store: the recorded provider is never empty and a status
// code of 0 never occurs.
type RequestFilter struct {
	Since      time.Time // inclusive lower bound on ts; zero means all time
	Provider   string
	SessionKey string // "none" is not special-cased — see ListRequests
	Alias      string
	StatusCode int
	ErrorsOnly bool // status_code >= 400
	Limit      int  // clamped to [1, maxRequestListLimit]; 0 means the default

	// Kind filters to an exact requests.kind value ("client", "classifier",
	// ...). Empty means no filter — every other field's zero-value
	// convention, unlike the presentation-layer default of "client only"
	// applied by the HTTP/UI handlers before a filter reaches here (see
	// stats.RequestsHandler and ui.RequestsHandler): this type has no
	// opinion on what "no kind specified" should mean, it just filters or
	// doesn't.
	Kind string

	// RequestKind filters to an exact requests.request_kind value ("title",
	// later "subagent"). Empty means no filter, like every other field here.
	// It is a separate filter from Kind above because the two answer
	// different questions: Kind is who sent the request, RequestKind is what
	// it is.
	RequestKind string

	// SessionKeyless selects the requests that have *no* session key —
	// `session_key IS NULL OR session_key = ''`. It exists because the zero
	// value of SessionKey means "any" (above), so the absent case is otherwise
	// unexpressible. It is reachable in practice: the affinity derivation
	// declines to pin a conversation whose first message is too short, and
	// those requests must still be visible in a list rather than silently
	// grouped under a session they do not belong to.
	SessionKeyless bool

	// BeforeTs/BeforeID are the keyset cursor: return only rows strictly older
	// than this (ts, id) pair under the list's own `ts DESC, id DESC` ordering.
	// Both must be set together; either alone is ignored.
	//
	// Ts is compared as the *stored text*, not as a bound built from a Go
	// time, because the store's ts column is TEXT in a layout SQLite's date
	// functions cannot parse and a fraction-free bound sorts below every row
	// in its own second. The caller therefore echoes back the exact value the
	// list handed it (RequestRow.TsRaw) rather than reformatting a timestamp.
	BeforeTs string
	BeforeID int64

	// IncludeContent is declared and never read. Content is reached through
	// ContentForRequest, which is what both callers do; this field is a
	// leftover from before capture existed and is scheduled for removal.
	IncludeContent bool // include prompt/response text — see RequestDetail
}

// RequestRow is one request as it appears in a list: enough to see what
// happened and where it went, without the prompt/response bodies. It is what
// a dashboard's main table renders.
type RequestRow struct {
	ID      int64  `json:"id"`
	TraceID string `json:"trace_id"`
	Ts      string `json:"ts"`

	// TsRaw is the timestamp exactly as stored, which is what the keyset
	// cursor must carry to compare correctly (see RequestFilter.BeforeTs).
	// Unmarshalled off the wire: it is a handle for the next page, not a
	// second rendering of the same instant.
	TsRaw      string `json:"-"`
	SessionKey string `json:"session_key,omitempty"`
	Format     string `json:"format"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	// ActualModel is the upstream-reported model, present only when it
	// differs from Model (a meta-router alias like OpenRouter's
	// "openrouter/auto" picked something concrete) — see store.Event.ActualModel.
	ActualModel      string  `json:"actual_model,omitempty"`
	AliasUsed        string  `json:"alias_used,omitempty"`
	RoutingRationale string  `json:"routing_rationale"`
	Domain           string  `json:"domain,omitempty"`
	Effort           string  `json:"effort,omitempty"`
	CostClass        string  `json:"cost_class,omitempty"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	LatencyMs        int64   `json:"latency_ms"`
	StatusCode       int64   `json:"status_code"`
	Error            string  `json:"error,omitempty"`
	Stream           bool    `json:"stream"`
	ConfigEpoch      string  `json:"config_epoch,omitempty"`
	// Kind is "client" (real traffic, the default) or one of Arbiter's own
	// internal request kinds ("classifier", and later "subagent") — see
	// store.Event.Kind.
	Kind string `json:"kind"`

	// RequestKind is what the request IS — "title" today, "subagent" later —
	// as opposed to Kind above, which is who sent it. See
	// store.Event.RequestKind.
	RequestKind string `json:"request_kind,omitempty"`
}

// RequestDetail is the full record for one request. Unlike RequestRow it
// carries the low-confidence classification score, the cache token counts,
// the tool calls, and — only when explicitly asked for — the request and
// response payloads.
//
// Prompts and responses are *not stored* by the event store (see the store's
// schema): it records routing, usage and outcome, not content. The two text
// fields are therefore always empty and exist so the shape of "eventually,
// optionally" is visible rather than surprising. Filling them means capturing
// request/response bodies in the schema first, which is a storage and privacy
// decision, not a read-side one.
type RequestDetail struct {
	RequestRow
	Confidence       float64 `json:"confidence"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	ToolCalls        string  `json:"tool_calls,omitempty"` // raw JSON array
	ClientID         string  `json:"client_id,omitempty"`  // NULL until per-client keys land

	// Headers is the inbound request's headers, credential-shaped values
	// already redacted before storage (see store.Event.Headers). Nil when
	// nothing was captured.
	Headers map[string]string `json:"headers,omitempty"`

	RequestText  string `json:"request_text,omitempty"`
	ResponseText string `json:"response_text,omitempty"`

	// Content is the captured request/response blocks, present only when
	// capture was on and something was stored. Omitted otherwise so "capture
	// off" and "nothing captured" are not confused on the wire — the
	// request_text/response_text fields above remain always-empty
	// placeholders from before capture existed and are superseded by this.
	Content []ContentBlock `json:"content,omitempty"`
}

// Overall answers the headline numbers over a window.
func (r *Reader) Overall(ctx context.Context, w Window) (OverallStats, error) {
	const q = `
SELECT
    COUNT(*),
    CAST(COALESCE(SUM(input_tokens), 0)  AS INTEGER),
    CAST(COALESCE(SUM(output_tokens), 0) AS INTEGER),
    COALESCE(SUM(cost_usd), 0),
    CAST(COALESCE(AVG(latency_ms), 0)    AS INTEGER),
    CAST(COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END), 0) AS INTEGER)
FROM requests
WHERE ts >= ? AND kind = 'client'`

	var s OverallStats
	err := r.db.QueryRowContext(ctx, q, w.Since).Scan(
		&s.Requests, &s.InputTokens, &s.OutputTokens, &s.CostUSD, &s.AvgLatencyMs, &s.Errors)
	if err != nil {
		return OverallStats{}, fmt.Errorf("overall stats: %w", err)
	}
	return s, nil
}

// ByProvider breaks a window's spend down by provider and model, most
// expensive first.
func (r *Reader) ByProvider(ctx context.Context, w Window) ([]ProviderStats, error) {
	const q = `
SELECT
    provider,
    model,
    COUNT(*),
    CAST(COALESCE(SUM(input_tokens), 0)  AS INTEGER),
    CAST(COALESCE(SUM(output_tokens), 0) AS INTEGER),
    COALESCE(SUM(cost_usd), 0),
    CAST(COALESCE(AVG(latency_ms), 0)    AS INTEGER)
FROM requests
WHERE ts >= ? AND kind = 'client'
GROUP BY provider, model
ORDER BY COALESCE(SUM(cost_usd), 0) DESC, COUNT(*) DESC`

	rows, err := r.db.QueryContext(ctx, q, w.Since)
	if err != nil {
		return nil, fmt.Errorf("stats by provider: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Non-nil so an empty result marshals as [] rather than null — to an
	// operator reading the JSON, "no rows" and "query failed" must not look
	// the same.
	out := []ProviderStats{}
	for rows.Next() {
		var s ProviderStats
		if err := rows.Scan(&s.Provider, &s.Model, &s.Requests, &s.InputTokens,
			&s.OutputTokens, &s.CostUSD, &s.AvgLatencyMs); err != nil {
			return nil, fmt.Errorf("scan provider stats: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ByEpoch breaks a window's spend down by config epoch, most recently first.
// This is the query that answers "did the config change save or cost money?".
func (r *Reader) ByEpoch(ctx context.Context, w Window) ([]EpochStats, error) {
	const q = `
SELECT
    COALESCE(config_epoch, ''),
    COUNT(*),
    CAST(COALESCE(SUM(input_tokens), 0)  AS INTEGER),
    CAST(COALESCE(SUM(output_tokens), 0) AS INTEGER),
    COALESCE(SUM(cost_usd), 0),
    COALESCE(AVG(cost_usd), 0),
    CAST(COALESCE(AVG(latency_ms), 0)    AS INTEGER),
    MIN(ts),
    MAX(ts)
FROM requests
WHERE ts >= ? AND kind = 'client'
GROUP BY COALESCE(config_epoch, '')
ORDER BY MIN(ts) DESC`

	rows, err := r.db.QueryContext(ctx, q, w.Since)
	if err != nil {
		return nil, fmt.Errorf("stats by epoch: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []EpochStats{}
	for rows.Next() {
		var (
			s              EpochStats
			first, lastRaw interface{}
		)
		if err := rows.Scan(&s.ConfigEpoch, &s.Requests, &s.InputTokens, &s.OutputTokens,
			&s.CostUSD, &s.AvgCostUSD, &s.AvgLatencyMs, &first, &lastRaw); err != nil {
			return nil, fmt.Errorf("scan epoch stats: %w", err)
		}
		s.FirstSeen = formatTime(first)
		s.LastSeen = formatTime(lastRaw)
		out = append(out, s)
	}
	return out, rows.Err()
}

// Session returns one conversation's requests in order — its trajectory. An
// unknown session key yields an empty slice, not an error.
func (r *Reader) Session(ctx context.Context, key string, limit int) ([]SessionRequest, error) {
	const q = `
SELECT
    id, trace_id, ts, provider, model, alias_used, routing_rationale,
    input_tokens, output_tokens, cost_usd, latency_ms, status_code, error, stream,
    tool_calls_json, config_epoch, kind
FROM requests
WHERE session_key = ?
ORDER BY ts ASC
LIMIT ?`

	rows, err := r.db.QueryContext(ctx, q, key, limit)
	if err != nil {
		return nil, fmt.Errorf("session trajectory: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []SessionRequest{}
	for rows.Next() {
		var (
			s     SessionRequest
			tsRaw interface{}
			alias sql.NullString
			tools sql.NullString
			epoch sql.NullString
			errTx sql.NullString
		)
		if err := rows.Scan(&s.ID, &s.TraceID, &tsRaw, &s.Provider, &s.Model, &alias,
			&s.RoutingRationale, &s.InputTokens, &s.OutputTokens, &s.CostUSD,
			&s.LatencyMs, &s.StatusCode, &errTx, &s.Stream, &tools, &epoch, &s.Kind); err != nil {
			return nil, fmt.Errorf("scan session row: %w", err)
		}
		s.Ts = formatTime(tsRaw)
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
	// id is the tiebreaker for the same reason ListRequests uses it: ts has
	// sub-second precision and several turns can share a tick.
	const q = `SELECT` + requestRowColumns + `
FROM requests WHERE session_key = ? AND kind = 'client'
ORDER BY ts ASC, id ASC
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
ORDER BY ts ASC, id ASC`

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
ORDER BY ts ASC, id ASC
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

// SessionFirstClient returns the session's opening client request — the turn
// that introduced the conversation, and therefore the one whose system
// preamble the page's preamble modal inspects. ok is false for a session with
// no client row at all.
func (r *Reader) SessionFirstClient(ctx context.Context, key string) (RequestRow, bool, error) {
	const q = `SELECT` + requestRowColumns + `
FROM requests WHERE session_key = ? AND kind = 'client'
ORDER BY ts ASC, id ASC
LIMIT 1`

	rows, err := r.db.QueryContext(ctx, q, key)
	if err != nil {
		return RequestRow{}, false, fmt.Errorf("session first client: %w", err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		return RequestRow{}, false, rows.Err()
	}
	s, err := scanRequestRow(rows)
	if err != nil {
		return RequestRow{}, false, err
	}
	return s, true, rows.Err()
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

// Tools counts tool-name occurrences over a window. tool_calls_json holds a
// JSON array per row; json_each expands it, so a request that used two tools
// contributes one to each. Rows with no tool calls are excluded by the NOT
// NULL filter — a plain chat turn is not "usage of no tool".
func (r *Reader) Tools(ctx context.Context, w Window) ([]ToolStat, error) {
	const q = `
SELECT json_each.value, COUNT(*)
FROM requests, json_each(requests.tool_calls_json)
WHERE requests.ts >= ? AND requests.tool_calls_json IS NOT NULL AND requests.kind = 'client'
GROUP BY json_each.value
ORDER BY COUNT(*) DESC`

	rows, err := r.db.QueryContext(ctx, q, w.Since)
	if err != nil {
		return nil, fmt.Errorf("tool usage: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []ToolStat{}
	for rows.Next() {
		var s ToolStat
		if err := rows.Scan(&s.Tool, &s.Uses); err != nil {
			return nil, fmt.Errorf("scan tool stat: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
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
    MIN(ts), MAX(ts),
    CAST(COALESCE(SUM(input_tokens), 0)  AS INTEGER),
    CAST(COALESCE(SUM(output_tokens), 0) AS INTEGER),
    COALESCE(SUM(cost_usd), 0),
    CAST(COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END), 0) AS INTEGER),
    COALESCE(group_concat(DISTINCT provider), ''),
    COUNT(DISTINCT provider || '/' || model)
FROM requests
WHERE ts >= ? AND session_key IS NOT NULL AND session_key <> '' AND kind = 'client'
GROUP BY session_key
ORDER BY MAX(ts) DESC
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

// SessionlessRequestCount counts the requests in a window that have no session
// key at all. It is what keeps those requests visible in the sessions index
// rather than silently absent from it.
func (r *Reader) SessionlessRequestCount(ctx context.Context, w Window) (int64, error) {
	const q = `SELECT COUNT(*) FROM requests
WHERE ts >= ? AND (session_key IS NULL OR session_key = '') AND kind = 'client'`

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
// A store with no affinity_pins rows (session affinity never pinned anything,
// or nothing is currently live) reports 0, not an error — an empty table is
// a legitimate steady state, not a broken query.
func (r *Reader) ActiveSessionCount(ctx context.Context) (int64, error) {
	const q = `SELECT COUNT(*) FROM affinity_pins WHERE expires_at >= ?`

	var n int64
	if err := r.db.QueryRowContext(ctx, q, time.Now().UTC()).Scan(&n); err != nil {
		return 0, fmt.Errorf("active session count: %w", err)
	}
	return n, nil
}

// SessionPinExpiry returns every session's pin expiry, for pins that expired
// no earlier than since — i.e. still live, or expired but recently enough to
// be within a caller-chosen grace window. It is the one query that answers
// both "is this session still active" (expires_at in the result is >= now)
// and "is this session still within its post-expiry grace window" (present
// in the result at all), without a second round trip for each question.
//
// This is the Sessions lane list's data source for both the "hot" dot and
// the "live only" filter: a lane whose key is absent from the result (its
// pin expired earlier than since, or it was never pinned) drops out of the
// filtered view entirely, while one still present but past its own expiry
// lingers — see sessions.go's liveGraceWindow for why a session does not
// vanish from the list the instant its pin expires.
//
// A store with no matching affinity_pins rows returns an empty, non-nil map
// — the caller does not need to special-case "nothing is live" separately
// from "the query failed".
func (r *Reader) SessionPinExpiry(ctx context.Context, since time.Time) (map[string]time.Time, error) {
	const q = `SELECT session_key, expires_at FROM affinity_pins WHERE expires_at >= ?`

	rows, err := r.db.QueryContext(ctx, q, since.UTC())
	if err != nil {
		return nil, fmt.Errorf("session pin expiry: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]time.Time{}
	for rows.Next() {
		var key string
		var expiresAt time.Time
		if err := rows.Scan(&key, &expiresAt); err != nil {
			return nil, fmt.Errorf("scan session pin expiry: %w", err)
		}
		out[key] = expiresAt
	}
	return out, rows.Err()
}

// MaxSessionListLimit caps the sessions index. Higher than the request list's
// cap because one row is a whole conversation, so a page of them is still a
// readable overview. Exported because the caller has to know what it asked for
// to tell a full page from a truncated one.
const MaxSessionListLimit = 1000

// maxRequestListLimit caps a single list query. The default (when the caller
// asks for no limit) is deliberately the cap: a dashboard shows recent rows,
// and truncating to the newest N is a more useful failure than a slow query.
const maxRequestListLimit = 500

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

// requestRowColumns is the list projection, shared by ListRequests and
// GetRequest so the two cannot drift into returning differently-shaped rows.
const requestRowColumns = `
    id, trace_id, ts, CAST(ts AS TEXT), session_key, format, provider, model, actual_model, alias_used,
    routing_rationale, domain, effort, cost_class, input_tokens, output_tokens,
    cost_usd, latency_ms, status_code, error, stream, config_epoch, kind, request_kind`

// ListRequests returns requests newest first, narrowed by f.
//
// A note on the session filter: an empty SessionKey means "any" — see
// RequestFilter.SessionKeyless for the absent case, which is expressed as its
// own boolean rather than as a magic string.
func (r *Reader) ListRequests(ctx context.Context, f RequestFilter) ([]RequestRow, error) {
	where := []string{"1 = 1"}
	args := []interface{}{}

	if !f.Since.IsZero() {
		where = append(where, "ts >= ?")
		args = append(args, f.Since)
	}
	if f.Provider != "" {
		where = append(where, "provider = ?")
		args = append(args, f.Provider)
	}
	if f.SessionKey != "" {
		where = append(where, "session_key = ?")
		args = append(args, f.SessionKey)
	}
	if f.SessionKeyless {
		where = append(where, "(session_key IS NULL OR session_key = '')")
	}
	if f.Alias != "" {
		where = append(where, "alias_used = ?")
		args = append(args, f.Alias)
	}
	if f.StatusCode != 0 {
		where = append(where, "status_code = ?")
		args = append(args, f.StatusCode)
	}
	if f.ErrorsOnly {
		where = append(where, "status_code >= 400")
	}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
	}
	if f.RequestKind != "" {
		where = append(where, "request_kind = ?")
		args = append(args, f.RequestKind)
	}
	// Keyset continuation. The row-value comparison matches the ordering below
	// exactly, which is what makes paging stable: an id-only cursor would skip
	// or repeat rows whenever ts is not monotonic in id (a backfill, an
	// import, a clock step). Both halves of the cursor are required; a
	// half-set cursor is ignored rather than silently mis-paging.
	if f.BeforeTs != "" && f.BeforeID > 0 {
		where = append(where, "(ts, id) < (?, ?)")
		args = append(args, f.BeforeTs, f.BeforeID)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = maxRequestListLimit
	}
	if limit > maxRequestListLimit {
		limit = maxRequestListLimit
	}

	// id as the tiebreaker matters: ts has sub-second precision, and rows
	// written within the same tick would otherwise come back in an arbitrary
	// order, making paging and "what just happened" both unreliable.
	q := "SELECT" + requestRowColumns + " FROM requests WHERE " +
		strings.Join(where, " AND ") + " ORDER BY ts DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list requests: %w", err)
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

// GetRequest returns one request by id. ok=false means no such row, which the
// caller reports as 404 — an unknown id is a normal outcome, not an error.
func (r *Reader) GetRequest(ctx context.Context, id int64) (RequestDetail, bool, error) {
	const q = `SELECT` + requestRowColumns + `,
    confidence, cache_read_tokens, cache_write_tokens, tool_calls_json, client_id, headers_json
FROM requests WHERE id = ?`

	var (
		d       RequestDetail
		tsRaw   interface{}
		session sql.NullString
		actual  sql.NullString
		alias   sql.NullString
		domain  sql.NullString
		effort  sql.NullString
		costCl  sql.NullString
		errText sql.NullString
		epoch   sql.NullString
		reqKind sql.NullString
		conf    sql.NullFloat64
		tools   sql.NullString
		client  sql.NullString
		headers sql.NullString
	)
	err := r.db.QueryRowContext(ctx, q, id).Scan(
		&d.ID, &d.TraceID, &tsRaw, &d.TsRaw, &session, &d.Format, &d.Provider, &d.Model, &actual, &alias,
		&d.RoutingRationale, &domain, &effort, &costCl, &d.InputTokens, &d.OutputTokens,
		&d.CostUSD, &d.LatencyMs, &d.StatusCode, &errText, &d.Stream, &epoch, &d.Kind, &reqKind,
		&conf, &d.CacheReadTokens, &d.CacheWriteTokens, &tools, &client, &headers)
	if errors.Is(err, sql.ErrNoRows) {
		return RequestDetail{}, false, nil
	}
	if err != nil {
		return RequestDetail{}, false, fmt.Errorf("get request %d: %w", id, err)
	}

	d.Ts = formatTime(tsRaw)
	d.SessionKey = session.String
	d.ActualModel = actual.String
	d.AliasUsed = alias.String
	d.Domain = domain.String
	d.Effort = effort.String
	d.CostClass = costCl.String
	d.Error = errText.String
	d.ConfigEpoch = epoch.String
	d.RequestKind = reqKind.String
	d.Confidence = conf.Float64
	d.ToolCalls = tools.String
	d.ClientID = client.String
	if headers.Valid {
		// A malformed headers_json (there shouldn't be one — it's only ever
		// written by headersJSON) degrades to "no headers shown" rather than
		// failing the whole detail lookup.
		_ = json.Unmarshal([]byte(headers.String), &d.Headers)
	}
	// RequestText/ResponseText stay empty: content is not captured. See the
	// type's doc comment.
	return d, true, nil
}

// scanRequestRow reads the shared list projection. The nullable columns come
// back as sql.Null* and are flattened to "" in the JSON — a NULL session key
// and an empty-string one are not distinguished on the wire, matching how the
// aggregate queries already behave.
func scanRequestRow(rows *sql.Rows) (RequestRow, error) {
	var (
		s       RequestRow
		tsRaw   interface{}
		session sql.NullString
		actual  sql.NullString
		alias   sql.NullString
		domain  sql.NullString
		effort  sql.NullString
		costCl  sql.NullString
		errText sql.NullString
		epoch   sql.NullString
		reqKind sql.NullString
	)
	if err := rows.Scan(&s.ID, &s.TraceID, &tsRaw, &s.TsRaw, &session, &s.Format, &s.Provider,
		&s.Model, &actual, &alias, &s.RoutingRationale, &domain, &effort, &costCl,
		&s.InputTokens, &s.OutputTokens, &s.CostUSD, &s.LatencyMs, &s.StatusCode,
		&errText, &s.Stream, &epoch, &s.Kind, &reqKind); err != nil {
		return RequestRow{}, fmt.Errorf("scan request row: %w", err)
	}
	s.Ts = formatTime(tsRaw)
	s.SessionKey = session.String
	s.ActualModel = actual.String
	s.AliasUsed = alias.String
	s.Domain = domain.String
	s.Effort = effort.String
	s.CostClass = costCl.String
	s.Error = errText.String
	s.ConfigEpoch = epoch.String
	s.RequestKind = reqKind.String
	return s, nil
}

// formatTime renders a sqlite timestamp, which the driver may hand back as a
// time.Time, a string (TEXT column), or nil. RFC3339 in UTC is what the JSON
// surface wants either way.
func formatTime(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	case string:
		// MIN()/MAX() over a TIMESTAMP column lose the driver's usual
		// time.Time conversion and come back as time.Time.String()'s own
		// layout instead of the driver's normal "2006-01-02 15:04:05.999999999-07:00".
		layouts := []string{
			"2006-01-02 15:04:05.999999999 -0700 MST",
			"2006-01-02 15:04:05.999999999-07:00",
			time.RFC3339,
		}
		for _, layout := range layouts {
			if parsed, err := time.Parse(layout, t); err == nil {
				return parsed.UTC().Format(time.RFC3339)
			}
		}
		return t
	default:
		return fmt.Sprint(t)
	}
}
