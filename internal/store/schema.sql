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
    difficulty            TEXT,
    cost_class            TEXT,
    confidence            REAL,
    required_capabilities_json TEXT,
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
    hash       BLOB    NOT NULL,
    -- name is the attachment's filename (#22), denormalized here rather than
    -- carried in the hash-keyed `content` table: the same bytes can arrive
    -- under different names, and `content` is addressed by body hash alone.
    -- It is required on the wire for an OpenAI document part
    -- ({type:"file", file:{filename, file_data}}) and Arbiter rebuilds the
    -- outbound body from NormalizedRequest, so a name lost here means the
    -- attachment cannot be re-emitted correctly. Empty for every non-
    -- attachment block, and empty for images (Anthropic/OpenAI images carry
    -- no name).
    name       TEXT
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
-- Content hash rollup (Discovery's counts, kept incrementally)
-- ---------------------------------------------------------------------------
-- RepeatedContent/ContentHashCounts used to aggregate content_refs JOIN
-- requests from scratch on every Discovery page load — two full scans of a
-- table that only ever grows (content is immutable once written), at ~6-7s
-- each on a real deployment. content_hash_stats is that same aggregate kept
-- as a running total instead: an hourly background sweep folds in whatever
-- is new since it last ran (see Reader.RollupContentHashStats), and the page
-- becomes a lookup over at most a few tens of thousands of rows.
--
-- This is deliberately all-time, not windowed: a per-day bucketed rollup
-- would let the page keep an accurate sliding window, but the operator
-- explicitly accepted approximate counts here ("this was sent in 55
-- sessions" is a spot-check, not a number anyone audits) and the drill-down
-- (SessionsForContent) always re-queries exactly. See PICKUP.md.
--
-- requests is a running COUNT(DISTINCT owner_id): safe to add to directly
-- because each content_refs row is written exactly once, ever, and the
-- rollup only ever looks at owner_ids it hasn't processed before.
--
-- sessions cannot be summed the same way — the same session sends the same
-- block on every turn, so a naive +1 per batch would double-count a session
-- that reappears in a later batch. content_hash_sessions below is the
-- dedup memory that makes an incremental session count correct.
CREATE TABLE IF NOT EXISTS content_hash_stats (
    hash        BLOB PRIMARY KEY,
    block_type  TEXT NOT NULL,
    role        TEXT NOT NULL DEFAULT '',
    requests    INTEGER NOT NULL DEFAULT 0,
    sessions    INTEGER NOT NULL DEFAULT 0,
    first_ts    TIMESTAMP,
    last_ts     TIMESTAMP
) WITHOUT ROWID;

-- Which (hash, session_key) pairs have already been counted into
-- content_hash_stats.sessions, so a session resending the same block in a
-- later rollup batch is recognised as already-counted rather than bumping
-- the total again. Pure bookkeeping: nothing reads this table except the
-- rollup itself.
CREATE TABLE IF NOT EXISTS content_hash_sessions (
    hash        BLOB NOT NULL,
    session_key TEXT NOT NULL,
    PRIMARY KEY (hash, session_key)
) WITHOUT ROWID;

-- rollup_state is the high-water mark: the highest requests.id already
-- folded into content_hash_stats. One row (name='content_hash'). Request ids
-- are a safe watermark because a request row and its content_refs commit in
-- the same transaction (see writer.go's writeEvent) — once a request is
-- visible to a reader, its content is already there to aggregate.
CREATE TABLE IF NOT EXISTS rollup_state (
    name          TEXT PRIMARY KEY,
    last_owner_id INTEGER NOT NULL DEFAULT 0
);

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
-- explicitly switches models means it.
--
-- prompt_hash separates prompt FAMILIES sharing one session_key: a client's
-- session header (when sent) groups a whole chat session, but a chat session
-- routinely contains several distinct system prompts (the main thread, a
-- title-generation call, a subagent run) that must not share a pin slot — a
-- title call would otherwise silently overwrite the main thread's target and
-- vice versa. Derived from the client's own pre-guardrail system prompt
-- (pipeline.PromptHash), so it needs no classification and no config: two
-- calls with the same prompt are the same family regardless of what either
-- prompt actually says. Empty string is a valid family (no session header
-- present, or the caller has no prompt to hash) and behaves like any other
-- value. At most one pin per (session_key, prompt_hash) — a pin recorded
-- under a new requested model REPLACES the old one for that family rather
-- than accumulating.
--
-- expires_at is an absolute deadline computed at write time (idle-timeout
-- semantics: a hit refreshes it, an abandoned conversation expires). The
-- deadline is stored rather than a TTL because the reader must be able to
-- reject a stale row without knowing the TTL it was pinned with.
CREATE TABLE IF NOT EXISTS affinity_pins (
    session_key     TEXT NOT NULL,
    prompt_hash     TEXT NOT NULL DEFAULT '',
    requested_model TEXT NOT NULL,
    provider        TEXT NOT NULL,
    model           TEXT NOT NULL,
    expires_at      TIMESTAMP NOT NULL,
    PRIMARY KEY (session_key, prompt_hash)
);
-- Feeds the expiry sweep, which deletes pins whose deadline has passed.
CREATE INDEX IF NOT EXISTS idx_affinity_expires ON affinity_pins(expires_at);