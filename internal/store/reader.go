package store

import (
	"context"
	"database/sql"
	"fmt"
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
