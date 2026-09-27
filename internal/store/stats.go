package store

import (
	"context"
	"fmt"
)

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
