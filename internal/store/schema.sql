-- One row per completed request. Deliberately denormalized: extracted tool
-- names live in a JSON column rather than a child table, because the real
-- query needs aren't known yet (REQUIREMENTS.md: capture first, don't
-- over-build for hypothetical queries).
--
-- id is the rowid, NOT trace_id. trace_id is trusted verbatim from the
-- inbound X-Trace-Id header, so duplicates are expected (client retries, a
-- proxy reusing an id); as a primary key that would be a failed INSERT.
--
-- session_key is nullable: the affinity derivation can decline to produce a
-- key, and those requests must still be recorded.
--
-- client_id stays NULL until per-client API keys land (attribution, not auth).
CREATE TABLE IF NOT EXISTS requests (
    id                    INTEGER PRIMARY KEY,
    trace_id              TEXT NOT NULL,
    session_key           TEXT,
    client_id             TEXT,
    ts                    TIMESTAMP NOT NULL,
    format                TEXT NOT NULL,       -- "anthropic" | "openai"
    provider              TEXT NOT NULL,
    model                 TEXT NOT NULL,
    alias_used            TEXT,                -- NULL if req.Model was literal
    routing_rationale     TEXT NOT NULL,
    domain                TEXT,
    effort                TEXT,
    cost_class            TEXT,
    confidence            REAL,
    input_tokens          INTEGER NOT NULL DEFAULT 0,
    output_tokens         INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens    INTEGER NOT NULL DEFAULT 0,
    cost_usd              REAL NOT NULL DEFAULT 0,
    latency_ms            INTEGER NOT NULL,
    status_code           INTEGER NOT NULL,
    error                 TEXT,
    stream                BOOLEAN NOT NULL,
    tool_calls_json       TEXT                 -- JSON array of tool names used
);

CREATE INDEX IF NOT EXISTS idx_requests_trace ON requests(trace_id);
CREATE INDEX IF NOT EXISTS idx_requests_session ON requests(session_key, ts);
CREATE INDEX IF NOT EXISTS idx_requests_ts ON requests(ts);
-- Feeds "usage per provider per window" (future upstream-mirrored limits,
-- dashboard). Cheap to add now, avoids a table scan later.
CREATE INDEX IF NOT EXISTS idx_requests_provider_ts ON requests(provider, ts);