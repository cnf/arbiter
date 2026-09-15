package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Reader answers questions about recorded requests. It is a thin layer over
// the same sqlite database the writer feeds, opening its own handle so a read
// never contends with the writer's single drain goroutine.
//
// The queries live here rather than in queries.sql for two reasons: tool
// usage needs json_each(), which sqlc's sqlite parser cannot resolve, and the
// read aggregates are few enough that generated accessors add more friction
// than they remove (see the note at the end of queries.sql).
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

// Window is a time range for a query. Since is the inclusive lower bound.
type Window struct {
	Since time.Time
}

// WindowFrom turns an operator-friendly duration ("24h", "7d" handled by the
// caller) into a window ending now.
func WindowFrom(d time.Duration) Window {
	return Window{Since: time.Now().UTC().Add(-d)}
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
	Stream           bool    `json:"stream"`
	ToolCalls        string  `json:"tool_calls,omitempty"` // raw JSON array
	ConfigEpoch      string  `json:"config_epoch,omitempty"`
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
	Since          time.Time // inclusive lower bound on ts; zero means all time
	Provider       string
	SessionKey     string // "none" is not special-cased — see ListRequests
	Alias          string
	StatusCode     int
	ErrorsOnly     bool // status_code >= 400
	Limit          int  // clamped to [1, maxRequestListLimit]; 0 means the default
	IncludeContent bool // include prompt/response text — see RequestDetail
}

// RequestRow is one request as it appears in a list: enough to see what
// happened and where it went, without the prompt/response bodies. It is what
// a dashboard's main table renders.
type RequestRow struct {
	ID               int64   `json:"id"`
	TraceID          string  `json:"trace_id"`
	Ts               string  `json:"ts"`
	SessionKey       string  `json:"session_key,omitempty"`
	Format           string  `json:"format"`
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
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

	RequestText  string `json:"request_text,omitempty"`
	ResponseText string `json:"response_text,omitempty"`
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
WHERE ts >= ?`

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
WHERE ts >= ?
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
WHERE ts >= ?
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
    input_tokens, output_tokens, cost_usd, latency_ms, status_code, stream,
    tool_calls_json, config_epoch
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
		)
		if err := rows.Scan(&s.ID, &s.TraceID, &tsRaw, &s.Provider, &s.Model, &alias,
			&s.RoutingRationale, &s.InputTokens, &s.OutputTokens, &s.CostUSD,
			&s.LatencyMs, &s.StatusCode, &s.Stream, &tools, &epoch); err != nil {
			return nil, fmt.Errorf("scan session row: %w", err)
		}
		s.Ts = formatTime(tsRaw)
		s.AliasUsed = alias.String
		s.ToolCalls = tools.String
		s.ConfigEpoch = epoch.String
		out = append(out, s)
	}
	return out, rows.Err()
}

// Tools counts tool-name occurrences over a window. tool_calls_json holds a
// JSON array per row; json_each expands it, so a request that used two tools
// contributes one to each. Rows with no tool calls are excluded by the NOT
// NULL filter — a plain chat turn is not "usage of no tool".
func (r *Reader) Tools(ctx context.Context, w Window) ([]ToolStat, error) {
	const q = `
SELECT json_each.value, COUNT(*)
FROM requests, json_each(requests.tool_calls_json)
WHERE requests.ts >= ? AND requests.tool_calls_json IS NOT NULL
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

// maxRequestListLimit caps a single list query. The default (when the caller
// asks for no limit) is deliberately the cap: a dashboard shows recent rows,
// and truncating to the newest N is a more useful failure than a slow query.
const maxRequestListLimit = 500

// requestRowColumns is the list projection, shared by ListRequests and
// GetRequest so the two cannot drift into returning differently-shaped rows.
const requestRowColumns = `
    id, trace_id, ts, session_key, format, provider, model, alias_used,
    routing_rationale, domain, effort, cost_class, input_tokens, output_tokens,
    cost_usd, latency_ms, status_code, error, stream, config_epoch`

// ListRequests returns requests newest first, narrowed by f.
//
// A note on the session filter: an empty SessionKey means "any", so there is
// no way to ask for the requests that have *no* session key. That case is
// reachable (the affinity derivation declines to produce a key) and a UI may
// eventually want it, but it needs an explicit sentinel or a separate
// boolean; guessing at a magic string now would be worse than not offering it.
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
    confidence, cache_read_tokens, cache_write_tokens, tool_calls_json, client_id
FROM requests WHERE id = ?`

	var (
		d       RequestDetail
		tsRaw   interface{}
		session sql.NullString
		alias   sql.NullString
		domain  sql.NullString
		effort  sql.NullString
		costCl  sql.NullString
		errText sql.NullString
		epoch   sql.NullString
		conf    sql.NullFloat64
		tools   sql.NullString
		client  sql.NullString
	)
	err := r.db.QueryRowContext(ctx, q, id).Scan(
		&d.ID, &d.TraceID, &tsRaw, &session, &d.Format, &d.Provider, &d.Model, &alias,
		&d.RoutingRationale, &domain, &effort, &costCl, &d.InputTokens, &d.OutputTokens,
		&d.CostUSD, &d.LatencyMs, &d.StatusCode, &errText, &d.Stream, &epoch,
		&conf, &d.CacheReadTokens, &d.CacheWriteTokens, &tools, &client)
	if errors.Is(err, sql.ErrNoRows) {
		return RequestDetail{}, false, nil
	}
	if err != nil {
		return RequestDetail{}, false, fmt.Errorf("get request %d: %w", id, err)
	}

	d.Ts = formatTime(tsRaw)
	d.SessionKey = session.String
	d.AliasUsed = alias.String
	d.Domain = domain.String
	d.Effort = effort.String
	d.CostClass = costCl.String
	d.Error = errText.String
	d.ConfigEpoch = epoch.String
	d.Confidence = conf.Float64
	d.ToolCalls = tools.String
	d.ClientID = client.String
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
		alias   sql.NullString
		domain  sql.NullString
		effort  sql.NullString
		costCl  sql.NullString
		errText sql.NullString
		epoch   sql.NullString
	)
	if err := rows.Scan(&s.ID, &s.TraceID, &tsRaw, &session, &s.Format, &s.Provider,
		&s.Model, &alias, &s.RoutingRationale, &domain, &effort, &costCl,
		&s.InputTokens, &s.OutputTokens, &s.CostUSD, &s.LatencyMs, &s.StatusCode,
		&errText, &s.Stream, &epoch); err != nil {
		return RequestRow{}, fmt.Errorf("scan request row: %w", err)
	}
	s.Ts = formatTime(tsRaw)
	s.SessionKey = session.String
	s.AliasUsed = alias.String
	s.Domain = domain.String
	s.Effort = effort.String
	s.CostClass = costCl.String
	s.Error = errText.String
	s.ConfigEpoch = epoch.String
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
