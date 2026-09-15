-- name: InsertRequest :one
INSERT INTO requests (
    trace_id, session_key, client_id, ts, format, provider, model,
    alias_used, routing_rationale, domain, effort, cost_class, confidence,
    input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
    cost_usd, latency_ms, status_code, error, stream, tool_calls_json
) VALUES (
    ?, ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?, ?, ?, ?
)
RETURNING id;

-- name: RecentRequests :many
SELECT id, trace_id, provider, model, cost_usd, latency_ms, status_code, ts
FROM requests
ORDER BY ts DESC
LIMIT ?;