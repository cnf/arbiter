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
--
-- arrival_ts is when the request reached Arbiter (Execute's own entry),
-- distinct from ts (when the request finished and this row was written).
-- The two differ whenever a request takes any real time upstream — a
-- classifier call started by a parent request routinely WRITES its row
-- before the parent does, because it finishes first (see SessionChildren's
-- doc comment, and issue #8). arrival_ts exists so causal order (what
-- triggered what) can be recovered directly instead of inferred from
-- trace_id nesting. NULL for rows written before the column existed.
CREATE TABLE IF NOT EXISTS requests (
    id                    INTEGER PRIMARY KEY,
    trace_id              TEXT NOT NULL,
    session_key           TEXT,
    client_id             TEXT,
    ts                    TIMESTAMP NOT NULL,
    arrival_ts            TIMESTAMP,
    format                TEXT NOT NULL,       -- "anthropic" | "openai"
    provider              TEXT NOT NULL,
    model                 TEXT NOT NULL,
    actual_model          TEXT,                -- upstream-reported model, when it differs from `model` (e.g. openrouter/auto)
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
    headers_json          TEXT,                -- JSON object of inbound headers, credentials redacted
    kind                  TEXT NOT NULL DEFAULT 'client', -- who sent it: "client" (real traffic) | "classifier"
    request_kind          TEXT                 -- what it IS: "title" | future: "subagent". Distinct from `kind` above, which says who sent it
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
-- owner_kind/owner_id rather than a plain request_id foreign key: historically
-- this let a request Arbiter refused (a pre-guardrail rejection, a routing
-- failure) store content under owner_kind='rejected' with no requests row to
-- point at. #5 changed that — a refused request now gets a real requests row
-- like any other client request (see pipeline.recordFailed), so every live
-- write uses owner_kind='request'. The column stays untyped and 'rejected'
-- remains a legal historical value so any pre-#5 database keeps reading.
CREATE TABLE IF NOT EXISTS content_refs (
    owner_kind TEXT    NOT NULL,   -- "request" (also, historically, "rejected" — see above)
    owner_id   INTEGER NOT NULL,   -- requests.id
    direction  TEXT    NOT NULL,   -- "request" (as sent) | "request_guardrailed" (as it went upstream, when a pre-guardrail ran) | "response"
    msg_index  INTEGER NOT NULL,   -- position of the message in the conversation
    position   INTEGER NOT NULL,   -- position of the block within the message
    role       TEXT,               -- the message's role, denormalized for grouping
    block_type TEXT    NOT NULL,   -- the block's own type, for filtering
    hash       BLOB    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_content_refs_owner ON content_refs(owner_kind, owner_id, direction, msg_index, position);
CREATE INDEX IF NOT EXISTS idx_content_refs_hash ON content_refs(hash);
-- Covers RepeatedContent/ContentHashCounts (internal/store/reader.go,
-- discovery.go): both group content_refs by hash, filtered to
-- owner_kind='request' AND direction='request', joining owner_id to
-- requests. Without this index SQLite drives off idx_content_refs_owner
-- (owner_kind first) and, worse, MIN(block_type)/MIN(role) in
-- RepeatedContent force a rowid lookback into the base table for every
-- matching row. Measured on a 1.1GB production DB (~4M direction='request'
-- refs, 30-day window): this index (with block_type/role included, making
-- it fully covering) took RepeatedContent from ~16s to ~4.4s and
-- ContentHashCounts from ~7s to ~3.8s.
CREATE INDEX IF NOT EXISTS idx_content_refs_repeated ON content_refs(owner_kind, direction, hash, owner_id, block_type, role);

-- ---------------------------------------------------------------------------
-- Discovery state
-- ---------------------------------------------------------------------------
-- Operator-set seen/ignored marks on a repeated content block, keyed by the
-- same hash content_refs/content use. No row means "unseen" — the default and
-- by far the common state, so this table only ever holds the blocks an
-- operator actually touched rather than one row per distinct hash.
--
-- state is 'seen' or 'ignored', never 'unseen' — see above. marked_at_last_seen
-- is the block's RepeatedContent.LastSeen value at the moment it was marked,
-- captured so a 'seen' mark can tell "nothing new since I looked" from "this
-- reappeared after I saw it": if the live last_seen advances past this value,
-- the UI reports the block as unseen again despite the stored row. 'ignored'
-- rows are not re-evaluated this way — an operator who ignores a pattern (e.g.
-- known boilerplate) means it permanently, not "until it's sent once more".
CREATE TABLE IF NOT EXISTS discovery_state (
    hash                BLOB PRIMARY KEY,
    state               TEXT NOT NULL,  -- 'seen' | 'ignored'
    marked_at_last_seen TEXT NOT NULL
);

-- ---------------------------------------------------------------------------
-- Session-affinity pins
-- ---------------------------------------------------------------------------
-- Which provider/model last served a session, so later turns of the same
-- conversation land on the same upstream (preserving prompt-cache reuse) rather
-- than being re-routed from scratch.
--
-- Persisted rather than in-memory because a config reload rebuilds the whole
-- pipeline, and pins living inside it died on every reload — so editing any
-- unrelated setting silently re-routed every live conversation and broke its
-- prompt cache. Restarts lost them too. Session pinning is load-bearing enough
-- (it is what keeps a conversation coherent and cheap) that it must outlive
-- both.
--
-- requested_model is the client's `model` value at pin time: a pin only applies
-- while the client keeps asking for that same model, because a client that
-- explicitly switches models means it. At most one pin per session key — a pin
-- recorded under a new requested model replaces the old one rather than
-- accumulating.
--
-- expires_at is an absolute deadline computed at write time (idle-timeout
-- semantics: a hit refreshes it, an abandoned conversation expires). The
-- deadline is stored rather than a TTL because the reader must be able to
-- reject a stale row without knowing the TTL it was pinned with.
CREATE TABLE IF NOT EXISTS affinity_pins (
    session_key     TEXT PRIMARY KEY,
    requested_model TEXT NOT NULL,
    provider        TEXT NOT NULL,
    model           TEXT NOT NULL,
    expires_at      TIMESTAMP NOT NULL
);
-- Feeds the expiry sweep, which deletes pins whose deadline has passed.
CREATE INDEX IF NOT EXISTS idx_affinity_expires ON affinity_pins(expires_at);