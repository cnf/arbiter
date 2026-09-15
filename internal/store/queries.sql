-- name: InsertRequest :one
INSERT INTO requests (
    trace_id, session_key, client_id, ts, format, provider, model,
    alias_used, routing_rationale, domain, effort, cost_class, confidence,
    input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
    cost_usd, latency_ms, status_code, error, stream, tool_calls_json,
    config_epoch
) VALUES (
    ?, ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?, ?, ?, ?, ?
)
RETURNING id;

-- name: RecentRequests :many
SELECT id, trace_id, provider, model, cost_usd, latency_ms, status_code, ts, config_epoch
FROM requests
ORDER BY ts DESC
LIMIT ?;

-- NOTE: the Phase 4a read surface (cost/tokens per provider, per config epoch,
-- per session, tool usage) is hand-written in reader.go rather than generated
-- here. Tool usage needs json_each(), a table-valued function sqlc's sqlite
-- parser cannot resolve; and the parser also corrupted the placeholder list of
-- some aggregate statements in this file (dropping or hoisting a trailing `?`),
-- which is not worth fighting for a read path this small.