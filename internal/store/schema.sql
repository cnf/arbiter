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
--
-- config_epoch is the hash of the resolved config that served this request
-- (Config.Epoch): the join key for "did this config change save or cost
-- money?". NULL for rows written before the column existed.
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
    tool_calls_json       TEXT,                -- JSON array of tool names used
    config_epoch          TEXT,
    headers_json          TEXT                 -- JSON object of inbound headers, credentials redacted
);

CREATE INDEX IF NOT EXISTS idx_requests_trace ON requests(trace_id);
CREATE INDEX IF NOT EXISTS idx_requests_session ON requests(session_key, ts);
CREATE INDEX IF NOT EXISTS idx_requests_ts ON requests(ts);
-- Feeds "usage per provider per window" (future upstream-mirrored limits,
-- dashboard). Cheap to add now, avoids a table scan later.
CREATE INDEX IF NOT EXISTS idx_requests_provider_ts ON requests(provider, ts);
-- Feeds the per-epoch cost comparison — the whole point of config_epoch.
CREATE INDEX IF NOT EXISTS idx_requests_epoch_ts ON requests(config_epoch, ts);

-- ---------------------------------------------------------------------------
-- Content store (prompt/response bodies)
-- ---------------------------------------------------------------------------
-- Content addressed by hash, at message-BLOCK granularity, and deliberately in
-- a separate table from requests: a back-and-forth conversation re-sends every
-- earlier block on each turn, so storing content on the request row would
-- duplicate turn N-1's text N times over. Hashing blocks makes turn N cost one
-- new row plus references to blocks already stored.
--
-- WITHOUT ROWID with the hash as the primary key stores the body inline in the
-- B-tree and makes the key the row itself — no rowid indirection and no second
-- copy of the hash.
--
-- body is the block's canonical bytes: the text itself for a text block (so
-- the hash is over the text, which is what makes "the same injected prompt
-- appears in every query" a usable query), and canonical JSON for structured
-- blocks (tool_use/tool_result), so their dedup is structural. body is NULL
-- when the content was not captured — binary/image blocks are hash-only by
-- decision, so the reference still exists and still dedups, but the file is
-- not stored.
--
-- No refcount column by design: garbage collection is an orphan sweep over
-- content_refs (see content.go), because refcounts are where content-addressed
-- collectors historically go wrong.
CREATE TABLE IF NOT EXISTS content (
    hash  BLOB PRIMARY KEY,     -- 32-byte SHA-256 of body
    kind  TEXT NOT NULL,        -- "text" | "tool_use" | "tool_result" | "thinking" | "image" | ...
    body  BLOB
) WITHOUT ROWID;

-- Which blocks belonged to which request, in order.
--
-- owner_kind/owner_id rather than a plain request_id foreign key: a request
-- that Arbiter rejects (normalize failure, a pre-guardrail rejection, a
-- routing failure) never gets a requests row, but its content is still worth
-- storing — "why was this rejected" is a first-class question for a router.
-- Those rows use owner_kind='rejected'; promoting them later is an INSERT plus
-- an UPDATE of owner_kind/owner_id, with no change to these queries.
CREATE TABLE IF NOT EXISTS content_refs (
    owner_kind TEXT    NOT NULL,   -- "request" | "rejected"
    owner_id   INTEGER NOT NULL,   -- requests.id, or the rejection's rowid
    direction  TEXT    NOT NULL,   -- "request" | "response"
    msg_index  INTEGER NOT NULL,   -- position of the message in the conversation
    position   INTEGER NOT NULL,   -- position of the block within the message
    role       TEXT,               -- the message's role, denormalized for grouping
    block_type TEXT    NOT NULL,   -- the block's own type, for filtering
    hash       BLOB    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_content_refs_owner ON content_refs(owner_kind, owner_id, direction, msg_index, position);
CREATE INDEX IF NOT EXISTS idx_content_refs_hash ON content_refs(hash);