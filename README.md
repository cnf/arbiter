# Arbiter

An LLM proxy/gateway with transparent routing decisions, composable classification axes, and independent observability.

Arbiter sits between LLM clients (Hermes, opencode, Claude Code, anything that
speaks the OpenAI or Anthropic API) and the providers behind them. It routes
each request wherever you have decided it should go, and it records every
request — including the ones Arbiter itself made — so what happened is
inspectable afterwards rather than reconstructed from logs.

## What Arbiter is for

Its job, in one line: **see what is actually happening.** A proxy that hides its
decisions is worse than no proxy, so nothing here is invisible by design. A
request that failed to route, a request a guardrail refused, a call Arbiter made
to a model to classify something — all of them are requests, and all of them
leave a row you can look at. `?errors` on the request list, the Discovery page,
and a session's turn-by-turn transcript are three views onto the same store.

Concretely, it gives you:

- **Every request recorded, with the reason it was routed that way.** Each row
  carries a one-line rationale (`policy router "policy": domain="code_generation"
  effort="" capabilities=[] cost_class="" -> alias "cheap-claude" -> claude/haiku`),
  so a routing decision never has to be guessed from the response.
- **Routing you write in config, not in code.** Classifiers turn a request into
  independent axes (`domain`, `effort`, `cost_class`, `capabilities`); aliases
  name a target; policy rules match on the axes. Stacked conditions, not a
  bag of `if` statements.
- **One endpoint in front of many providers.** OpenAI-shaped and Anthropic-shaped
  requests both come in; either wire format can go out, to any mix of cloud
  providers and local runtimes.
- **Cross-format translation.** A Hermes conversation (OpenAI wire) can be served
  by Claude (Anthropic wire) and vice versa. Attachments survive the trip.
- **Guardrails as composable hooks.** System-prompt injection, rate limiting, and
  stripping client-injected preamble text are three instances of one mechanism.
- **A spend and latency record.** Cost, token counts, cache reads, and per-request
  latency are stored per request, aggregatable by provider, model, epoch, and
  session.

## What Arbiter deliberately is not

These are design decisions, not a backlog. Re-proposing them is re-litigating
something already settled.

- **Not multi-user.** It is built for one operator: a single-user deployment,
  single-digit concurrency at worst, about one client at a time. Machinery sized
  for multi-tenant traffic — sharding, collision guardrails, per-tenant quotas —
  is deliberately absent.
- **No authentication inside the app.** Access control belongs to the
  deployment: a tailnet plus a reverse proxy doing `forward_auth`. The one
  in-app affordance is a presence-only header check on `/admin/*` so your proxy
  can gate it. The admin surface binds to loopback by default for the same
  reason.
- **Not a router for arbitrary client metadata.** Routing keys on the request
  itself, not on client identity. Session affinity prefers a header the client
  actually sends (`X-Session-Id` by default); when there isn't one, Arbiter
  derives a stable key by hashing the conversation's own prefix — system prompt
  plus the first text-bearing user turn — rather than inventing an identifier.
  When that prefix is too short to be distinctive (a bare `"hi"` and nothing
  else) it declines to pin at all, because merging two unrelated conversations
  is worse than not grouping them.
- **Not an embedded platform.** Memory, search, and MCP hosting stay external
  services reached over HTTP. Arbiter proxies and observes; it does not become
  the thing being observed.
- **Currently out of scope:** `/v1/audio/*` (multipart file uploads — a
  different ingress shape from this pipeline's JSON-in → JSON/SSE-out) and
  `/v1/embeddings`. Neither is called by the clients in use today. These are
  "not now" rather than "never" — they would each be a new ingress shape, not a
  variation on an existing one.

## Not built yet

Real gaps, tracked on the issue board rather than silently dropped. Listed so a
reader does not discover them as surprises.

- **`/v1/responses`** (the OpenAI Responses API). Codex CLI v0.116+ uses it
  *exclusively* and no longer calls `/chat/completions`, so Codex cannot use
  Arbiter as a backend today. HTTP/SSE is sufficient — no WebSocket server is
  needed — but it is a real protocol adapter (its own `input` request shape, its
  own typed event stream, `previous_response_id` continuity), not an alias.
- **Anthropic client-facing parsing of attachments.** Images and documents
  arriving on the Anthropic endpoint are dropped by the translator today. The
  Anthropic *upstream* direction works; this is the inbound client direction.
- **Cache counters on the wire.** The store records cache-read and cache-write
  tokens, but neither outbound usage object carries a cache breakdown back to
  the client, and the inbound Anthropic *streaming* parser does not read the
  cache counters (the non-streaming one does). So a streamed Claude reply is
  recorded with zero cache tokens, and a client cannot see caching working.
- **Empirical cost and latency.** Provider-*reported* cost is captured; latency
  is recorded per request. The interface for an empirical cost/latency lookup
  exists and is the seam a measured implementation would fill.
- **Sub-agent attribution.** Linking a child request to the parent that spawned
  it needs a schema column whose shape isn't known until a multi-agent client is
  instrumented. Deferred rather than half-designed.
- **`score` and `min_confidence`** on the decision-model classifier: the
  scoring primitive and the confidence gate.

## Where to start

- **New to this?** **[docs/getting-started.md](docs/getting-started.md)** — build
  it, give it a provider, send a request, see the row it left.

The rest of the documentation is being lifted out of this file into `docs/` a
section at a time. Until a section has moved, it is still below — this is the
current table of contents, not the target one:

- **Pointing a client at it:** **[docs/clients.md](docs/clients.md)** — endpoints,
  streaming, attachments, session affinity, prompt caching.
- **Deciding where requests go:** [Routing](#routing), [Group selection
  strategies](#group-selection-strategies), [LLM-backed
  classification](#llm-backed-classification), [Decision-model
  classification](#decision-model-classification-type-decisions),
  [Matching a request's own text](#matching-a-requests-own-text-match),
  [Structural capability detection](#structural-capability-detection-detect)
- **Every config key:** [Configuration](#configuration)
- **Reading what it recorded:** [Event store](#event-store), [Admin surface and
  access](#admin-surface-and-access), [The live tail](#the-live-tail),
  [Grouping](#grouping-a-run-of-streamed-turns-is-one-line),
  [Discovery](#discovery-the-blocks-that-recur)
- **Controlling what goes through:** [Guardrails](#guardrails), [Rate
  limiting](#rate-limiting), [Prompt
  rewriting](#prompt-rewriting-client-injected-prompts)

---

## Building

All development happens in devenv (the host is not expected to have a Go
toolchain or C compiler — cgo dependencies need devenv's toolchain):

```bash
devenv shell
go build -o arbiter ./cmd/arbiter
```

Or use the dev script:

```bash
devenv shell
dev  # runs air (hot reload)
```

`devenv shell` offers these scripts: `dev` (air, hot reload), `test`
(`devenv test`), `lint`, `docs` (`go doc -http`), and `mock` (fire the same
streaming request at fakellm and at arbiter, to compare).

Run everything through the devenv process manager (`devenv processes up/down`),
which starts fakellm (port 5665) before arbiter (port 8080). Note: builds,
tests, git commits, and anything touching secrets must run inside `devenv
shell`; secrets access requires a reason (`SECRETSPEC_REASON="..."` or
`--reason`).

Two flags control where arbiter listens: `--bind` (default `127.0.0.1`) and
`--port` (default `8080`), or `--socket <path>` for a unix socket, which
overrides both. Loopback is the default on purpose — see
[Admin surface and access](#admin-surface-and-access).

## Endpoints

The full endpoint surface, the streaming contract, attachments, session
affinity and prompt caching are documented in
**[docs/clients.md](docs/clients.md)**.

The short version: `POST /v1/messages` (Anthropic) and `POST
/chat/completions` (OpenAI), `GET /v1/models`, `GET /health`. Both chat
endpoints stream in the wire format of the request.

## Project Structure

```
arbiter/
├── cmd/
│   ├── arbiter/          # main entry point; wires config -> pipeline
│   └── catalog-convert/  # litellm price list -> model_catalog file
├── internal/
│   ├── http/             # ingress: proxy endpoints, SSE flushing, trace IDs, /admin/* (reload + stats)
│   ├── pipeline/         # request lifecycle: normalize -> guardrails -> classify -> force -> route -> upstream
│   ├── router/           # routing: policy rules, simple default/fallback, alias resolution
│   ├── classifier/       # per-axis routing signals (domain, effort, cost class, capabilities)
│   ├── guardrail/        # composable pre/post hooks (system prompt, rate limit, prompt rewrite)
│   ├── translator/       # Anthropic <-> OpenAI <-> Normalized conversions (incl. SSE events)
│   ├── upstream/         # provider HTTP calls, SSE reading, response parsing
│   ├── logging/          # single structured logging path (JSON, trace-correlated)
│   ├── store/            # sqlite event store: schema, async writer, content store, Reader
│   ├── ui/               # admin web UI: embedded templates + assets, htmx fragments
│   └── config/           # arbiter.yaml loading (strict: unknown fields rejected)
├── pkg/
│   ├── types/            # Normalized* request/response/stream types (the hub)
│   └── errors/           # typed errors with HTTP status mapping
├── devenv.nix
└── go.mod
```

## Event store

`internal/store` holds a sqlite event store — one row per completed request,
the queryable record REQUIREMENTS.md asks for (the JSON log lines are a
transport, not a store). Storage is `modernc.org/sqlite`, a pure-Go driver, so
the build stays CGO-free. All SQL is hand-written Go: `schema.sql` (embedded,
applied at startup) plus `writer.go`'s insert and `reader.go`'s queries.

The write path used to be generated by `sqlc`. **That was retired** — not
because sqlite was wrong, but because the generator could not express the
queries this project actually needs. It left exactly one generated function in
use (`InsertRequest`), while the read side had to be hand-written anyway: tool
usage needs `json_each()`, a table-valued function sqlc's sqlite parser cannot
resolve, and sqlc v1.31.1 additionally corrupts the placeholder list of the
*last* statement in a file (verified positional — it follows whichever query is
last, not any particular SQL). Against that, the generated files were committed
artifacts needing manual regeneration. The one thing sqlc was genuinely
expected to provide — schema validation — costs nothing to lose: sqlite
validates the DDL when the schema is executed at startup, so a malformed
`CREATE TABLE` still fails loudly before serving traffic.

The pipeline records every completed request — success, upstream failure, and
streamed responses alike — through `store.Writer`. `Record` is non-blocking on
the request path: it enqueues onto a buffered channel drained by a single
goroutine that owns the database handle. If the queue is saturated, `Record`
waits briefly (50ms) and then drops the event with a warning rather than
stalling the request; a drop means the writer is not keeping up with volume,
which is a signal to raise the buffer or batch inserts, not a normal outcome.

Every row carries a `config_epoch` — a short hash of the resolved config
that served the request, stamped by the pipeline as it hands the event to the
store. It is the join
key for "did this config change save or cost money?": `/admin/stats/epochs`
groups by it, and because epochs stay live for wildly different lengths of
time it also reports cost *per request*, which is what makes two epochs
comparable despite different raw spend. Provider API keys are redacted before
hashing — rotating a secret must not split the data — but every other
behavior-bearing field changes the hash.

A `store.Reader` answers questions over the same file on its own handle, so a
read never contends with the writer's drain goroutine. Seven queries: five
aggregates (overall stats, spend by provider/model, spend by config epoch, one
session's trajectory, tool-name usage counts), the request list, and one
request in full — plus the content lookups behind `/admin/requests/{id}` and
the recurring-block query behind `/admin/content/repeated` (see "Content
store").

Reader and writer are separate connections to one database file, so both open
through the same DSN, which enables WAL journaling and a 5-second busy
timeout. Under sqlite's default rollback journal, a read that collided with
the writer's transaction failed instantly with SQLITE_BUSY. WAL mode persists
in the database file; the busy timeout is per-connection, which is why the two
share the DSN rather than the reader inheriting it from the writer.

The store is off by default. Set `storage.path` in `arbiter.yaml` to enable it:

```yaml
storage:
  path: "arbiter.db"
```

The writer is opened once at startup and shared across config reloads, so
`storage.path` is fixed for the process lifetime (a reload changing it is
ignored for the store; every other config change still applies). Streams record
the read goroutine's real terminal status rather than assuming 200, so a stream
that dies mid-flight is stored as the failure it was. When an upstream reports
no cost (plain Anthropic/OpenAI — OpenRouter reports a real figure), cost is
computed from the `model_catalog` rates; a model with no catalog row records 0
rather than a fabricated number.

The schema is deliberately denormalized (tool names go in a JSON column rather
than a child table) until real query needs are known. `id` is the rowid, not
`trace_id`: the inbound `X-Trace-Id` is trusted verbatim, so duplicate values
are expected and must not collide. `session_key` is nullable — a request whose
affinity derivation declines to produce a key is still recorded, which happens
only when a request carries neither a session header nor enough text to be
distinctive at all (a bare `"hi"` with no system prompt); see "Session
affinity". `client_id`
stays NULL until per-client API keys land (attribution, not authentication).
`config_epoch` is nullable too — rows written before the column existed (or by
a build with no epoch set) group under the empty string rather than
disappearing.

The content tables sit in the same file and the same write transaction as the
request row, so a request can never be half-stored: its row and its bodies
commit together or not at all. `content_refs.owner_kind` distinguishes
`'request'` from `'rejected'` — the latter now unused by any live write path
(see "Nothing invisible" below) but kept as a schema value so old rows stay
readable.

## Architecture

Hub-and-spoke: every wire format converts to/from a `NormalizedRequest` /
`NormalizedResponse` / `NormalizedStreamEvent` and never directly to another
wire format. Adding a provider wire format means adding one spoke (to/from
Normalized), not an N×N matrix. The HTTP layer knows the caller's format from
which endpoint was hit; routing picks a provider, and the provider's `type`
field selects the outbound wire format (`anthropic`, `openai`, `ollama` — the
last being OpenAI-compatible transport, preserving provider identity).

## Configuration

`arbiter.yaml` (path configurable via `--config`). Environment variables expand
into values as `${VAR_NAME}`. Unknown fields are a config error, not silently
ignored.

The file is hot-reloaded: edits are picked up without a restart. A reload
rebuilds the whole pipeline (providers, routers, classifiers, guardrails) and
swaps it in atomically; requests already in flight finish on the old config,
and a reload that fails to load, validate, or build is rejected with a logged
error while the previous config keeps serving.

The example below is a complete, loadable config (a test asserts exactly that,
so it cannot rot): every provider it references is defined here.

```yaml
providers:
  claude:
    type: "anthropic"                  # anthropic | openai | ollama
    endpoint: "https://api.anthropic.com"
    key: "${ANTHROPIC_API_KEY}"
    models: ["claude-3-opus-20250219", "claude-3-haiku-20250307"]
  gpt4:
    type: "openai"
    endpoint: "https://api.openai.com/v1"
    key: "${OPENAI_API_KEY}"
    models: ["gpt-4o"]
  litellm:
    type: "openai"
    endpoint: "${LITELLM_URL}"
    key: "${LITELLM_API_KEY}"
    models: ["openrouter/free"]
  local:
    type: "ollama"                     # OpenAI-compatible transport, own identity
    endpoint: "http://localhost:11434/v1"
    models: ["llama2"]

classifiers:
  - name: "domain"
    type: "heuristic"
    axis: "domain"                     # domain | effort | cost_class | capabilities
    config:
      keywords: { code_generation: ["write", "refactor"] }
  - name: "effort"
    type: "heuristic"
    axis: "effort"                     # a second instance, same type, own axis
    config:
      keywords: { easy: ["quick"], hard: ["architecture"] }

aliases:
  auto:                                # full auto: force nothing, classify + rules
    force: {}
  coding:
    force: { domain: ["code_generation"] }
  cheap-claude:                        # pinned: one concrete provider/model
    type: "pinned"
    provider: "claude"
    model: "claude-3-haiku-20250307"
  free-search:                         # group: ordered candidates + fallback chain
    type: "group"
    select: "random"                   # random | cheapest_input | cheapest_output | fastest
    members:
      - { provider: "litellm", model: "openrouter/free" }
      - { provider: "local", model: "llama2" }

model_catalog:                         # feeds the cost/latency select strategies
  - provider: "litellm"
    model: "openrouter/free"
    input_cost_per_mtok: 0
    output_cost_per_mtok: 0
    latency_ms_p50: 2500
  - provider: "claude"
    model: "claude-3-haiku-20250307"
    input_cost_per_mtok: 0.25
    output_cost_per_mtok: 1.25
    latency_ms_p50: 900
    input_modalities: ["text", "image"]   # what the model accepts
    max_input_tokens: 200000
    metadata:                             # free-form, forwarded to /models
      function_calling: true

routers:
  - name: "policy"
    type: "policy"
    config:
      rules:
        - when: { domain: "code_generation", effort: "hard" }
          target: "cheap-claude"       # a rule target may name an alias
        - when: { capabilities: ["vision"] }
          provider: "gpt4"             # ...or a literal provider/model
        - when: { requires_input_modalities: ["image"] }
          provider: "claude"           # skipped unless claude accepts images
        - when: { request_kind: "title" }  # who's asking, not what it's about
          target: "cheap-claude"       # title-gen traffic never needs a big model
        - when: {}                     # catch-all
          provider: "claude"
        - when: { domain: "unmatched" }  # or refuse instead of degrading:
          target:                        #   target: {stop: {error, message}}
            stop:
              error: 406
              message: "requests in this domain are not supported"
  - name: "primary"
    type: "simple"                     # chained after policy: last-resort default
    config:
      default_provider: "claude"
      # fallback_provider: "gpt4"

guardrails:
  pre: []                              # system_prompt, rate_limit, prompt_rewrite
  post: []

routing:
  fallback_providers: ["gpt4"]         # tried in order on 429/5xx

session_affinity:
  header: "X-Session-Id"               # inbound header carrying a session id
  default_ttl: "25h"                   # idle TTL for a pinned conversation

storage:                               # omit the whole block for no persistence
  path: "arbiter.db"
  capture_content: true                # store prompt/response bodies (see below)
  content_ttl: "72h"                   # empty means never expire

logging:
  level: "info"
  format: "json"
  output: "stdout"
```

### Routing

The `model` field a client sends selects how a request is routed, in this
precedence order:

1. **A real model name.** If it exactly matches a configured provider's
   declared `models`, the request goes straight to that provider/model.
2. **A session-affinity pin**, if the conversation is already pinned and the
   client is still requesting the same `model` value.
3. **A pinned or group alias.** A client naming a `pinned` alias routes to it
   directly; naming a `group` alias selects a member (its unselected members
   become that route's fallback chain, tried before the global
   `routing.fallback_providers`).
4. **Classify + rules.** Signals are classified per axis (each `heuristic`
   classifier fills the one axis it declares, merged per axis so a
   high-confidence domain match can't starve effort), a **force alias** named
   by the client overrides only the axes it declares (`coding` sets domain but
   leaves effort to classify), then the first matching policy rule wins. A
   policy router errors when nothing matches, so chain a `simple` router after
   it (or write a catch-all rule) to degrade instead of failing.

A rule's `when` clause can also match `request_kind` — a request's **kind**
(`"title"`, later `"subagent"`) rather than what it's about. It is not a
classification axis (no confidence, no force-alias target — see
`types.Signals.RequestKind`), but it is still a legitimate thing to route on:
a title-generation call is identified by the `request-kind` classifier
(see "Matching a request's own text" below) and a rule like
`when: { request_kind: "title" }` sends it to a cheap/fast alias instead of
whatever model the client happened to name.

A rule's target is exactly one of: a named alias (`target: "…"`), a literal
provider/model (`provider:`/`model:`), or a **terminal refusal**. A refusal rule
matches like any other and then stops the request outright instead of routing
it:

```yaml
- when: { domain: "unmatched" }
  target:
    stop:
      error: 406
      message: "requests in this domain are not supported"
```

`error` must be a valid HTTP status code and `message` non-empty; the client
gets exactly that status with the message as the error body, and the refusal is
recorded like any other rejection. A `stop` rule short-circuits the whole
router chain — a later fallback router can't override an explicit refusal — and
is most useful in a catch-all position (`when: {}`) to refuse everything that
no earlier rule covers.

Aliases are client-facing and appear in `/models` alongside provider models
(listed with provider `"alias"`). Any rule `target` may name an alias, and a
group member may itself be another alias; resolution is depth-limited and a
cycle is rejected at config load.

### Session affinity

Session affinity — how a conversation is pinned to a provider, and the two ways
its key is derived — is documented in
**[docs/clients.md](docs/clients.md#session-affinity)**.

### What survives a config reload

Which runtime state survives a reload or a restart (session pins, rate-limit
counters, provider cooldowns) is documented in
**[docs/clients.md](docs/clients.md#what-survives-a-config-reload)**.

### Group selection strategies

A `group` alias's `select:` decides which member becomes the primary:

| `select` | Picks |
| --- | --- |
| `random` (default) | a random member |
| `cheapest_input` | lowest `input_cost_per_mtok` |
| `cheapest_output` | lowest `output_cost_per_mtok` |
| `fastest` | lowest `latency_ms_p50` |

The cost/latency strategies read the `model_catalog` block — static figures,
keyed by provider+model, in USD per million tokens and milliseconds. Members
with no catalog entry are treated as *unknown cost* and rank last rather than
erroring; if no member has an entry (or no catalog is configured), selection
falls back to the first-listed member, deterministically, so a missing row is
visible as a routing decision instead of being masked by randomness. Ties
break the same way. The unselected members remain the fallback chain.

The catalog can be written inline, pulled from a generated file, or both.
`model_catalog_file:` names a second catalog (same `model_catalog:` shape),
resolved relative to the config file's directory:

```yaml
model_catalog:                     # hand-managed; wins on conflict
  - provider: "claude"
    model: "claude-3-haiku-20250307"
    input_cost_per_mtok: 0.30
model_catalog_file: "catalog.yaml" # generated; supplies the defaults
```

Merging is **row-wise**: if both sources declare the same provider/model, the
inline row replaces the file's row entirely — fields are never mixed between
the two. That's deliberate because `0` is a meaningful cost (a free model), so
a field-by-field override could not tell "unset" from "free". Duplicate rows
*within* one source are a config error.

#### Capabilities

A row also carries what the model **can do**, which `/v1/models` advertises and
policy rules can route on:

| Field | Meaning |
|---|---|
| `input_modalities` | What the model accepts: `text`, `image`, `file` |
| `max_input_tokens` | Input context window |
| `max_output_tokens` | Output limit |
| `metadata` | Free-form; forwarded to `/v1/models` verbatim |

**Absence means unknown, not false.** If a row omits `input_modalities`, or has
no row at all, `/v1/models` omits the field entirely rather than sending an
empty list — "we have no information about this model" and "this model accepts
nothing" are different claims, and a client reading an empty list would believe
the second. For the same reason an alias advertises no capabilities: it resolves
to a target at request time, so what it accepts depends on where it lands.

`catalog-convert` fills these from LiteLLM's price list, which carries
`supports_vision`, `supports_pdf_input`, `max_input_tokens` and friends for
roughly two-thirds of its chat models. The rest simply have no capability data,
and their fields stay absent. `metadata` collects the flags that have no
normalized home (`function_calling`, `reasoning`, `prompt_caching`,
`computer_use`, …) so a client can read them without Arbiter having to
interpret them — nothing in Arbiter consults `metadata` for any decision.

The normalized vocabulary is Arbiter's own rather than LiteLLM's: several of its
flags collapse into one modality (`supports_vision` or `supports_image_input` →
`image`; `supports_pdf_input` → `file`). Mapping happens in the converter, so a
second upstream source can be added later without changing what consumes it.

The catalog file is inert on write: the config watcher tracks only the config
file itself, so regenerating `catalog.yaml` does not reload anything until you
ask for it.

The file is produced by `cmd/catalog-convert`, which normalizes one or more
upstream files into this shape — the runtime never parses a foreign schema
itself. Upstreams differ in how they key models and in what units they quote
costs, so the converter needs a mapping file saying which files to read and
which provider key each Arbiter provider's models are filed under:

```bash
devenv shell
go run ./cmd/catalog-convert -mapping mapping.yaml -config arbiter.yaml -out catalog.yaml
```

`-mapping` is required (copy `mapping.example.yaml` and edit). The mapping's
`sources:` list names the upstream files in **precedence order**, either as bare
paths (the kind is sniffed from the file's content) or as
`{path: ..., kind: ...}` when you want to be explicit:

```yaml
sources:
  - litellm-prices.json          # first wins
  - models-dev-api.json
```

Three kinds are supported:

| kind | File | Key shape | Cost unit |
|---|---|---|---|
| `litellm` | LiteLLM's `model_prices_and_context_window.json` | flat model name + `litellm_provider` | **per token** (scaled to per-million) |
| `modelsdev-api` | models.dev `api.json` | nested `provider → models` | **per million tokens** (used as-is) |
| `modelsdev-models` | models.dev `models.json` | flat `<vendor>/<model>` | none (that file carries no cost) |

**The cost unit differs per kind, and that is the one thing most worth getting
right** — running models.dev's per-million figures through the litellm
conversion would inflate every cost a millionfold, silently. Each reader owns
its own unit.

**First source wins**, and there is no per-field merge: a model already emitted
is never overridden by a later source. Precedence is the list's order and nothing
else — a rule you can predict without reading the implementation. Each provider
may name its own `source:`; omitting it uses the first, so mapping files written
before `sources:` existed keep working. A `source:` naming an unlisted file is an
error, never a silent fallback — a typo there would build the catalog from the
wrong file.

The provider-key field depends on the source kind: `litellm_provider` for a
`litellm` source, `models_dev_provider` for a `modelsdev-api` source, and neither
for the flat `modelsdev-models` shape (that file has no provider dimension — its
keys are already `<vendor>/<model>`, which is exactly how a prefixing aggregator
names its models). The two fields are separate rather than one because they name
different namespaces: a `litellm` value used against a models.dev source would
silently match nothing.

`-config` (optional) points at `arbiter.yaml` and limits generation to the
providers it actually declares — skipping (with a reason on stderr) any mapping
entry with no match there; omit it and every provider the mapping file lists is
used instead. Latency is in none of these sources, so the mapping supplies it —
a per-provider default with per-model overrides. Rows are emitted in provider
order with each provider's rows sorted by model, so regenerating the same inputs
produces the same file. The write is **atomic** (temp file, fsync, rename), so a
crash mid-write cannot leave a truncated catalog — which matters because
regeneration is meant to run on a schedule against a live config.

#### Namespacing the emitted model name

Arbiter joins a catalog row to a declared model on the **exact** string, so a
provider whose models are declared under a namespace needs that namespace on
the emitted row — otherwise the rows are written but never read, and nothing
reports a problem. Two mapping fields fix opposite mismatches:

| Field | Direction | Use when |
|---|---|---|
| `key_prefix` | strips from the LiteLLM key | the upstream key is longer than the declared name (`openrouter/anthropic/claude-3.5-sonnet`) |
| `namespace` | adds to the emitted name | the upstream key is bare but the declared name is namespaced (`claude/claude-sonnet-5`) |

Both may be set; `key_prefix` is applied first. This is the common case for a
self-hosted or omniroute-style proxy that fronts another vendor's API under a
prefix. Matching stays **exact** on both sides — there is no fuzzy fallback,
deliberately, because a near-miss would silently attach one model's cost and
capabilities to another.

#### Coverage reporting

Regenerating can succeed while the models that matter get nothing, because the
join is exact and a naming mismatch is silent. `-coverage` reports, per
provider, which declared models actually got a row:

```bash
go run ./cmd/catalog-convert -mapping mapping.yaml -config arbiter.yaml \
  -coverage json -coverage-out report.json \
  model_prices_and_context_window.json
```

```
claude: 3/3 matched
openrouter: 2/8 matched (6 manual: @preset/comp, @preset/deepseek-flash, ...)
total: 19 declared, 5 matched, 14 manual, 0 unexpected
```

`-coverage` is `text` (default), `json`, or `none`, and requires `-config`
(that is what declares the models). `-coverage-out` writes the report to its
own file, which a cron wants: the report is the last thing on stderr otherwise,
sharing it with the skip lines.

Three states, and the third is the point: **matched**, **manual** (declared
under `manual:` in the mapping file as expected to have no upstream data — your
own presets, a source not wired up yet), and **unexpected** (no data, not
declared as manual). Only *unexpected* is a failure. Without that split the
`-strict` gate would be red on every run for permanently-unmatchable models,
and a gate that always fails is one nobody reads.

`-strict` therefore fails on an undecodable source entry, on any unexpected
coverage miss, and — when no `-config` is given, so there is no coverage to
check — on any skip. A provider-level skip with `-config` present is
informational: the provider is simply out of scope, and the coverage report
already shows the consequence for its models.

**The generated catalog is a superset of what `arbiter.yaml` declares, by
design** — a row naming a provider/model this config doesn't (yet) list is
inert, not an error: `model_catalog_file:` rows are checked leniently (an
undeclared row is silently dropped) precisely because they're expected to
cover more than any one config uses, while the hand-written inline
`model_catalog:` block keeps strict validation (an undeclared row there is
still a config-load error — a typo worth catching). Adding a model to
`arbiter.yaml` therefore needs no catalog-convert change at all; the next
regeneration already has it.

A rejected conversion is the safe failure: the generated catalog is inert on
write, so nothing changes in the running config until you call
`POST /admin/reload`.

### Routing on what a model can do

A rule may demand that its **target** accept certain input modalities:

```yaml
- when: { requires_input_modalities: ["image"] }
  provider: "claude"
```

This is deliberately a different key from `capabilities`, which matches the
*request's* own requirements. The two vocabularies are different things:
`capabilities` says what a request **needs** (`vision`, `tool_use`,
`long_context`), while `requires_input_modalities` says what a model
**accepts** (`text`, `image`, `file`). Folding them together would silently
change the meaning of every existing `capabilities` rule.

A rule whose target cannot satisfy the requirement is **skipped**, and matching
continues to the next rule. That is what makes a chain read as "send image
traffic to the vision model, everything else here" without the operator writing
the negative case:

```yaml
rules:
  - when: { requires_input_modalities: ["image"] }
    provider: "vision-model"     # used only if it accepts images
  - when: {}                     # catch-all
    provider: "text-model"
```

Modalities come from the cost/latency catalog (`input_modalities` on a
`model_catalog` row). A model with **no catalog row, or a row that states
nothing about modalities, does not satisfy the requirement** — unknown is not
permission, and routing image traffic to a model whose support is simply
unstated is the guess this exists to prevent. The same applies when no catalog
is configured at all: an unverifiable guard never passes.

If every rule is skipped the router reports its usual no-match error, so a
router chained after it (a `simple` router, say) still takes over.

### LLM-backed classification

A `type: "llm"` classifier asks an upstream model to classify a request
instead of guessing from keywords — real semantic understanding, at the cost
of a real upstream call:

```yaml
classifiers:
  - name: "domain-heuristic"
    type: "heuristic"
    config:
      keywords: { code_generation: ["write", "refactor"] }
  - name: "domain-llm"
    type: "llm"
    axis: "domain"                      # only "domain" is built today
    only_if_unset: true                 # skip unless the heuristic left domain empty
    config:
      alias: "cheap-classifier"         # routes the classification call — any pinned or group alias
                                       # (or model: "provider/model" — address a declared model directly)
      labels:                           # bare names, or name -> rubric description
        code_generation: >-
          the user wants code written, modified, refactored, or reviewed —
          an implementation task with a concrete code artifact as the answer.
        reasoning: >-
          the user wants something explained, analyzed or debugged — an
          answer in prose, with no code artifact as the deliverable.
        chat: "greeting or small talk with no artifact expected."
        none: "none of the other categories apply."
      escape: "none"                    # this label's verdict fills no axis
      instructions: "Pick the category that best describes the request."  # optional
      fallback: "domain-heuristic"      # another classifier, declared anywhere in this list
      timeout: "5s"                     # optional, defaults to 10s
```

`labels` accepts either shape and both may be mixed freely in one list: a plain
list of names (`labels: ["code_generation", "chat"]`, unchanged from before
rubrics existed) or a map of name to description. The description is where a
category's *boundary* lives — a bare name tells the model nothing about where
`code_generation` ends and `reasoning` begins — so a rubric is the single
biggest lever on classification accuracy.

The prompt is assembled from three parts, in this order: the framing sentence
(`instructions`, or a fixed default), the label list with its descriptions, and
the reply contract. Only the first two are configurable — the contract
("reply with the single matching word and nothing else") is always appended
last, because a single-word reply is what makes a half-parsed answer impossible
and a configurable reply format would let a rubric edit break the parser. Labels
are sorted into the prompt, so the same config always produces byte-identical
prompt text and one call's prompt can be diffed against the next. (That is for
comparability only: a prompt this short is below every provider's minimum
cacheable length, so it is never cached either way.)

`escape` names the label meaning "no category fits" and **fills no axis at
all** — the axis is left empty, so a policy router's `when: {domain: ...}` rules
simply don't match and a chained router takes over. That is the point: without
an escape label, "nothing fits" can only be expressed as a wrong category or an
off-list reply, and an off-list reply counts as a *failure* that falls back to
the heuristic — so the model's honest uncertainty is indistinguishable from a
broken call. The verdict is still recorded as a successful call with confidence
1.0; it just carries no value. A bare `none` reply is also accepted as escape
whenever an escape label is configured.

`alias` is resolved exactly the way a client-named alias is — a group alias's
member selection and its unselected siblings (tried in order on failure) work
the same way here as they do for real traffic. The alternative `model:`
(`"provider/model"`) addresses a declared model directly, with no alias at all —
the form a classifier uses when it is itself reached as a fallback from another
classifier, where a client-facing alias would be wrong. Exactly one of `alias`
or `model` must be set. `fallback` names another
configured classifier (built first regardless of declaration order) that
takes over completely whenever the LLM call fails outright: a timeout, an
upstream error, or a reply that doesn't match any configured label exactly
(never guessed at — the same "surface ambiguity, don't guess" rule
`literalModelRoute` follows). `Classify` therefore never returns an error
itself; `MergedClassifier` aborts its *entire* merge on any sub-classifier
error, so a bubbled failure here would silently kill every other axis being
classified alongside it, not just this one — the fallback exists precisely so
that never happens. A `fallback` naming another `"llm"` classifier is
rejected at config load — no chained LLM fallbacks.

`only_if_unset` is the *other* way a model call is avoided: where `fallback`
handles a model call that *failed*, `only_if_unset` prevents the call from
being *made* when it would be redundant. Declared at classifier level (not
inside `config:`), it skips the classifier entirely — no upstream call, no
cost, no recorded attempt — unless its axis is still empty after every
classifier declared before it. The pattern it encodes is "cheap first, model
only if needed": declare a heuristic before the llm classifier, and the model
call fires only when the heuristic left the axis empty. An escape/"other"
verdict fills no axis, so it counts as empty and the gated classifier still
runs. The skip is per axis: a gated `domain` classifier still runs when only
the `effort` axis was filled elsewhere. A decisions classifier, whose one call
answers several axes, is skipped only when *every* axis it would fill is
already set — any unanswered axis justifies the call. Only model-backed types
may set it; a heuristic that never makes an upstream call has nothing to gate.

Every call — success or failure — is recorded as its own request row, tagged
`kind: "classifier"` (see Admin surface and access): visible on the request detail page and its
triggering session's trajectory, excluded from the default request list and
every cost/latency aggregate. Its `routing_rationale` carries both the verdict
and a preview of the text that produced it (`LLM classifier replied
"code_generation" — input "please fix this bug..."`), because the rationale is
what the list shows before anyone loads captured content — a verdict alone
cannot distinguish a clear message the model misjudged from a rubric that failed
to describe the category. The preview is cut at 120 bytes on a rune boundary,
so a multi-byte character is never split into invalid UTF-8; the full text is in
the captured content. This makes the store double as a training-data source for
a future locally-trained classifier: `domain` and the classified text are both
already captured, for free, as a side effect of routing.

With `storage.capture_content` on, the classifier's own row carries what it
**saw**: the text it classified, plus the prompt it was given as a system block.
That is what makes a wrong verdict debuggable — the recorded reply shows the
decision, not the evidence, so without the input there is no way to tell a model
that misjudged a clear message from a rubric that failed to describe the
category. It costs nothing extra to store: the input is byte-identical to a
block of the client's own request that capture already holds, so it addresses to
the same content row and adds one reference, not a second body — which also
makes the classifier row joinable to the request that triggered it. The prompt
block is constant for a given config, so it addresses to a single row forever.
Both ride the one `capture_content` switch; there is no separate switch for
derived calls.

Note that a classifier row is a `requests` row, so the content queries that join
`content_refs` to `requests` (repeated content, the per-hash drill-down) filter
to `kind = 'client'` — otherwise every classifier call would count as a second
request "containing" the text it was asked to classify, and a client's
boilerplate preamble would read as twice as widespread as it is.

Session affinity does the caching here for free: once a session is pinned,
`classify()` never runs again for that conversation, so an LLM classifier
call happens once per session, not once per turn — no separate mechanism
needed. A session with no derivable key (no header, too-short opener)
classifies fresh every turn, same as any other classifier. Pins are persisted,
so a config reload no longer re-classifies a pinned conversation: the pin
survives it (see the Session affinity section under Configuration).

### Decision-model classification (`type: "decisions"`)

A `type: "decisions"` classifier asks a **decision model** typed questions
about a request instead of asking a chat model to emit one word. It is a third
type beside `heuristic` and `llm`, not a replacement for either: the two
model-backed types send the same input, so they are directly comparable on real
traffic.

It exists because the LLM classifier's two load-bearing hacks have no
equivalent here. There is no reply to parse — a `choice` question is
constrained to the options the config defines, so an off-list answer is
impossible rather than a *failed call* that falls back to the heuristic — and
there is no reply contract to protect. What it adds beyond that is a real
probability per answer, which the LLM classifier can only ever set to `1.0`.

**Every question is asked in ONE upstream call**, and each answer carries its
own probability. That is what makes a decision model fit Arbiter's multi-axis
`Signals`: several axes, one request. Each question declares its own `axis`
(there is no classifier-level `axis:`), and two questions may not fill the same
axis — they would race for it with nothing to break the tie.

Because one call answers several axes, a single "how sure was this classifier"
number would be a lie: a 0.98 domain pick would carry a 0.61 cost_class verdict
at 0.98 and beat a legitimate classifier on that axis. So a decisions classifier
reports **per-axis confidence** (`Signals.AxisConfidence`), and the merge picks
each axis's winner on that axis's own number. `Signals.Confidence` stays the
highest per-axis value — the most certain thing the call concluded.

A partially-answered call is not a failed call: an unanswered or off-list
question is dropped (and recorded on the call's row) while the axes that were
answered stand, since each axis is resolved independently.

```yaml
providers:
  claude:
    type: "anthropic"
    endpoint: "https://api.anthropic.com"
    models: ["claude-3-haiku-20250307"]
  openrouter-decisions:                   # same vendor, a different API surface
    type: "decisions"
    endpoint: "https://openrouter.ai/api/alpha/decisions"   # the COMPLETE URL
    key: "${OPENROUTER_API_KEY}"
    models: ["~typesafe/jev-latest"]
aliases:
  jev:
    type: "pinned"
    provider: "openrouter-decisions"
    model: "~typesafe/jev-latest"
routers:
  - name: "primary"
    type: "simple"
    config:
      default_provider: "claude"
classifiers:
  - name: "domain-heuristic"
    type: "heuristic"
    config: { keywords: { code_generation: ["write", "refactor"] } }
  - name: "routing-decisions"
    type: "decisions"
    only_if_unset: true              # skip unless every axis it fills is still empty
    config:
      alias: "jev"                        # routes the decision call (or model: "or-decisions/~typesafe/jev-latest")
      questions:                          # ALL asked in ONE call
        domain:
          axis: "domain"                  # which Signals axis it fills
          type: "choice"                  # the only primitive built so far
          labels:                         # bare names, or name -> rubric
            code_generation: "wants code written, modified or reviewed."
            reasoning: "wants something explained or debugged."
            chat: "small talk with no artifact expected."
            none: "none of the other categories apply."
          escape: "none"                  # sent as the `other` option
          instructions: "Pick the category that best describes the request."
        cost_class:
          axis: "cost_class"
          type: "choice"
          labels:
            budget: "a cheap model is fine."
            quality_first: "spend more for a better answer."
          instructions: "How much is this request worth spending on?"
      fallback: "domain-heuristic"        # may name an `llm` classifier too
      timeout: "5s"                       # optional, defaults to 10s
```

**A `type: "decisions"` provider's `endpoint` is the complete URL**, and nothing
is appended to it. Every other provider type treats `endpoint` as an API root
and the transport adds its own suffix (`/v1/messages`, `/chat/completions`).
This one is deliberately different because the decisions path is known to be
unstable — OpenRouter serves it at an unversioned `/api/alpha/decisions`, and
TypeSafe's own API is `/v1/systemone` — so keeping the whole URL in config makes
a vendor path change a config edit rather than a code change. Auth is the
bearer-token convention, with the provider's own `headers` applied last like
every other type.

The target — an `alias` or a `model:` — must resolve to a provider of type
`decisions`; a pinned alias naming an ordinary chat provider is a
**config-load error**, because the alternative is sending a
`state`/`questions` body to `/chat/completions` and failing at request
time.

**`escape` is sent under its own name.** It is already one of the `labels`, so
the operator's own wording is what the model sees — declare
`none: "none of the other categories apply."` and that is the option sent.
Choosing it fills **no axis at all**, so a policy router's `when: {domain: ...}`
rules simply don't match and a chained router takes over — the same semantics the
`llm` classifier's escape label has.

`other: "none of the above apply"` is added **only when no `escape` is
configured**: without some way to say "none of these fit", a decision model is
forced into the closest listed option, which is exactly the failure an escape
label prevents. In that case a literal `other` reply is the escape verdict. When
an `escape` label *is* configured, a stray `other` is **not** treated as escape —
the operator named their own option, and accepting a synonym would make the
axis's emptiness depend on which word the model happened to pick.

**`fallback` may name an `llm` classifier here**, unlike an `llm` classifier's
own fallback. The no-chained-LLM rule exists to bound chains; `decisions → llm`
is depth 1 and terminates, because the `llm` classifier's fallback must still be
a non-model classifier. It is also the useful direction — a chat model asked the
same question is exactly the escalation a failed decision call wants.

**The stored rationale names every axis with its probability**, not just the
winner: `decisions classifier answered domain="code_generation" (0.92),
cost_class="budget" (0.61)`. The probability is the entire reason for using a
decision model, and it is what makes a low-confidence verdict readable as
*uncertain* rather than simply wrong. An escape verdict is named explicitly
(`domain="none of the options fit"`) rather than rendering as an empty string,
so "the model said nothing fits" never reads like "we failed to fill the axis".
Note that `confidence` is **how peaked the distribution is on the answer, not a
calibrated probability that the answer is correct** — it is usable as a relative
act-vs-escalate gate, and nothing more is claimed for it here.

Every call is recorded as its own `kind="classifier"` row, like the `llm`
classifier's, with the same captured-content behaviour — so the row's
`routing_rationale` and the captured prompt/input are the evidence trail for a
verdict that came from a distribution rather than from text.

### What a classifier reads, and how much of it

Every classifier reads the **first user turn that actually has text**
(`types.FirstUserText`). Three things follow from that, and each was a defect
when the rule was different:

- **It is not the last user turn.** An agentic client sends turns whose only
  content is a `tool_result` block, and a turn like that has no text at all. A
  classifier reading the last user turn therefore classified the empty string on
  most turns of a tool-using conversation — and a model asked to classify
  nothing answers anyway, at high confidence. Twelve identical "none of the
  options fit" verdicts at ~0.8 confidence, all on one session, is what that
  looked like in the store.
- **Nothing to classify means no call.** An empty input is not a no-op: it costs
  money and returns a verdict indistinguishable from a real one. The call is
  skipped instead, and the absence stays visible as an absence.
- **The input is capped.** `max_input_chars` bounds the text sent, in
  characters — not tokens, because the only estimator here is the documented
  char/4 heuristic and a token cap would imply a precision nothing has. The cap
  keeps the head, drops the tail, and marks the cut with `…`.

```yaml
classifiers:
  - name: "routing-decisions"
    type: "decisions"
    config:
      alias: "classifier"
      max_input_chars: 4096     # optional; default 8192, negative = unlimited
      questions: ...
```

The default applies when the field is absent. A negative value is the explicit
opt-out; `0` is rejected at load, because it would read as "unset" and quietly
take the default while the operator believed they had set a cap.

### Matching a request's own text (`match`)

A classifier's input is one user message. Some requests are identified by text
that lives somewhere else: a title generator's system prompt carries its
signature, and for such a request the user message is the conversation being
titled, which looks like ordinary chat. The identifying text was not merely
unmatched, it was invisible by construction.

`match` searches the request's own text instead, on **any** classifier type. This
is the shipped `request-kind` classifier, and the three patterns are the **real**
prompts, read out of the live store rather than invented:

```yaml
classifiers:
  - name: "request-kind"
    type: "heuristic"
    config:
      match:                            # OR-ed; any hit wins
        # Hermes
        - mode: "regex"
          pattern: "(?i)^\\s*You name chat sessions\\."
        # opencode
        - mode: "regex"
          pattern: "(?i)^\\s*You are a title generator\\."
        # Claude Code CLI — buried in the system prompt, so not anchored at the start
        - mode: "regex"
          pattern: "(?i)Generate a concise, sentence-case title"
      kind: "title"                     # the request kind a hit records
      decisive: true                    # optional — see below
```

**The three signatures, verbatim from the store** (each is the opening of that
client's system prompt):

| client | signature | where it sits |
|---|---|---|
| Hermes | `You name chat sessions. Given the user's opening message, write a title...` | start of the block |
| opencode | `You are a title generator. You output ONLY a thread title. Nothing else.` | start of the block |
| Claude Code CLI | `Generate a concise, sentence-case title (3-7 words) that captures the main topic...` | ~165 bytes in |

**A `prefix` pattern must be at the START of the text, which is why these use
`regex`.** Two of the three could be `prefix`, but Claude's cannot: its system
prompt opens with an `x-anthropic-billing-header: ...` line and
`You are Claude Code, Anthropic's official CLI for Claude.`, and the title
instruction sits behind both. Anchoring the pattern at the start would never
match, silently. `regex` is used for all three so the list has one mode.

`regex` is case-sensitive, unlike `exact`/`prefix`, so a case-insensitive match
needs an explicit `(?i)`.

**Write patterns against the real prompt, and test them against the corpus.** A
loose pattern is not a near miss here, it is a silent disaster: `(?i)title`
matches **50 of the 79 distinct system prompts** in the live store, because every
Hermes agent prompt contains the phrase `title-generation grouping in the UI is
deferred` in its own memory text. Meanwhile `(?i)title generation` matches
**zero**. Adjacent-looking patterns land anywhere from 0 to 50, and both
extremes are invisible in a UI that shows no reason a match did not happen.

The way to know which is which is to read the distinct system prompts out of the
store and run the candidates against them:

```sql
-- The whole inventory is small: 79 distinct prompts at the time of writing.
SELECT length(cast(body AS TEXT)), cast(body AS TEXT)
  FROM content
 WHERE hash IN (SELECT hash FROM content_refs WHERE role='system' AND owner_kind='request');
```

`internal/config/title_signature_test.go` pins this down: it loads the shipped
`arbiter.yaml` through the real parser, compares the patterns to the intended
regexes (a single backslash inside YAML double quotes loads without error and
only fails as a pattern that never matches), and runs them against the three
prompts plus the Hermes agent prompt they must not match.

`mode` and `where` mean exactly what they mean on the `prompt_rewrite`
guardrail, and the implementation is literally the same code
(`types.TextMatcher`): `exact` and `prefix` ignore case and surrounding
whitespace, because the same preamble is re-serialized differently by different
clients and wire formats; `regex` is used verbatim. A bare string is accepted
as one `prefix` pattern.

**`where: ["system"]` searches the prompt as the CLIENT sent it**, not as the
guardrails left it. This matters because a `system_prompt` guardrail with
`override: false` *prepends* its own text, and pre-guardrails run before
classification — so a `prefix` match against the assembled prompt would be
looking for a signature behind Arbiter's own preamble, and would fail silently
in a way that looks exactly like a wrong pattern. The request carries the
pre-guardrail text in `ClientSystemPrompt` for this reason, and matching reads
that. `where: ["messages"]` needs no equivalent: no guardrail rewrites messages
by default, and the ones that can (`prompt_rewrite` with `where: ["messages"]`)
run in the same phase, so a signature in a message is matched as sent.

One consequence worth knowing: a `system_prompt` guardrail that *strips* the
client's signature cannot break a match, because the matcher sees the text
before stripping. That is the intended direction — the signature was in the
request, which is the thing being classified.

**A hit is a certainty, not a guess**, so it reports confidence `1.0` and the
keywords are not consulted — a request that provably *is* a title generation is
not a candidate for keyword voting.

**`decisive: true` ends classification.** `MergedClassifier` consults
classifiers in the order they are declared, and a decisive **hit** stops the
merge, so classifiers behind it never run — no keyword pass, no model call. A
decisive **miss** falls through exactly like any other miss, which is the
property that matters: one title-gen rule must not switch off classification
for every request that is not a title generation.

This is a classifier capability rather than a guardrail because a guardrail
structurally cannot do it: `ApplyPre` returns a request and an error with no
channel for routing signals, and pre-guardrails run *before* `classify`, so a
guardrail emitting a signal would be emitting into a phase that already
happened.

**Brittleness, honestly.** A pattern list is only as good as the patterns in it:
a client whose signature you have never seen is a false negative until its
pattern is added. Three things mitigate that — the OR-list makes each client
variation a one-line edit, `regex` covers the ones that differ cosmetically,
and a decisions classifier is the backstop for what neither catches.

### Structural capability detection (`detect`)

`capability_detector` gained request-shape predicates alongside its keywords:

```yaml
classifiers:
  - name: "capability"
    type: "capability_detector"
    config:
      detect: ["tool_use", "attachment"]   # proven from the request
      long_context_tokens: 100000          # required if long_context is detected
      keywords:                            # still works, for what shape cannot prove
        vision: ["screenshot"]
```

| `detect` value | Proven by |
|---|---|
| `tool_use` | the request carries tools |
| `attachment` | a content block is an attachment (image, PDF, document) |
| `long_context` | `EstimatedTokens` at or over `long_context_tokens` |

These are **facts about the request**, not inferences from its words: whether a
request carries an attachment is countable, whereas the keyword detector was
guessing from the word "image". A structural hit is therefore certain
(confidence `1.0`) and is unioned with whatever keywords also matched — a
request can need vision and tool_use at once.

`attachment` is deliberately distinct from `vision`. `vision` is the
pre-existing guess from text, kept unchanged so existing rules keep meaning what
they meant; `attachment` is the certainty. A rule may match either.

`long_context` is the one needing a threshold, because "long" is a policy
choice rather than a fact — configuring `detect: ["long_context"]` without
`long_context_tokens` is a **config-load error**, since it could otherwise never
fire and "never fired" is indistinguishable from "the request was short".

### Admin surface and access

`POST /admin/reload` re-reads the config and the `model_catalog_file` on
demand. It calls exactly the same `reload()` the file watcher does, so a
regenerated `catalog.yaml` — inert on write by design — is picked up by
calling it. A rejected reload returns 500 and leaves the running config
serving; the response body says so.

Arbiter implements **no authentication**. Access control is Caddy's
(`forward_auth`) and the network's. The only in-app affordance is a
presence-only gate:

```yaml
admin:
  forward_auth_header: "X-Forwarded-User"
```

When set, a request to `/admin/*` without that header gets **401**; when unset,
`/admin/*` is ungated (a development convenience). Arbiter checks only that
the header is *present* — it cannot verify the proxy set it — so this is sound
only while Arbiter is unreachable except through that proxy.

The `/admin/stats*` endpoints are read-only and need the event store enabled
(`storage.path` set); without it they return **503** rather than an empty
result, so "store disabled" and "no traffic yet" stay distinguishable. Each
takes an optional `?since=<duration>` (e.g. `?since=24h`), defaulting to the
last 7 days, and `/admin/stats/session` takes the session key as `?key=`.

`/admin/requests` is the same data one row at a time — the aggregates above
cannot answer "what just happened", so this returns the requests themselves,
newest first. `?since`, `?provider`, `?session`, `?alias`, `?status=<code>`,
`?errors` (presence only: status ≥ 400), `?no_session` (presence only: only the
requests with no session key), `?kind=<kind>` (see below) and `?limit=<n>`
(default and maximum 500) narrow it; a malformed `status` or `limit` is a
**400**, not a silently ignored filter. Ordering is `ts DESC, id DESC` — the id tiebreaker matters
because rows written within one timestamp tick would otherwise come back in an
arbitrary order. `/admin/requests/{id}` takes the `id` the list returns and
adds the fields a list row omits (confidence, cache token counts, tool calls,
inbound headers); `404` for an unknown id, `400` for a malformed one.

This is the **JSON** surface, and it is the only one. The browser UI under
`/admin/ui/` reads the same rows through `internal/ui`'s own handlers (see
below); the request-list *page* it used to have was deleted in the newui
rebuild, so `/admin/ui/requests` is a 404 — and `flatLineHref`
(`internal/ui/requests.go`) still builds links to it, which is a real defect on
the Sessions page's "open flat list" affordance, not a doc problem. **Tracked as
#56.**

`?no_session` exists because an empty `?session=` means "any", so the requests
with *no* session key (those whose affinity derivation declined to pin them)
needed their own flag.

Every row carries a `kind`: `"client"` for real traffic (the default and, so
far, the only kind that exists in practice — `"classifier"` lands with the
LLM-backed domain classifier), and later `"subagent"` will reuse
the same column rather than each inventing their own flag. A non-client
request is never hidden from the store or the detail/session views — the
point is debuggability, not opacity — but it *is* excluded by default from
`/admin/requests` (omit `?kind=` for client-only, pass `?kind=all` to see
everything, or `?kind=<kind>` for an exact match) and unconditionally from
every aggregate (`/admin/stats*` and the sessions index): a
classifier call's own tokens/cost/latency must not skew numbers meant to
describe what a client actually asked for. The per-session trajectory
(`/admin/ui/session?key=`) and a single request's own detail view are the
exception — they show every kind, tagged, because a classifier call is most
useful to see *in the context of the turn it informed*.

`kind` says **who sent** the request. A separate `request_kind` column says
**what the request is** — `"title"` for a client's title-generation call, later
`"subagent"`. The two are independent: a title request is client traffic
(`kind="client"`) that is *identifiable* as a title request. It is filled by a
classifier's `match:` block via `kind:`, not by an axis, because "what is this
request about" has no meaningful answer for a title generator — the conversation
being titled is its payload, not its subject. That is also why it does not go
in `domain`: an axis is a *routing* input, contested by confidence and
overridable by a force-alias, and a request's kind is a fact no rule should
match on.

`?request_kind=<kind>` filters the list by it (free text, since the set of
kinds is open), and the axes column renders it in its own colour so it is never
mistaken for a domain the router matched on. **Why it exists:** a request that
names a concrete model used to be indistinguishable from ordinary traffic on its
row — same `model` value, empty axes, and the generic `explicit model "…" ->
provider "…"` rationale — so a Hermes title-generation request could be sitting
in the list and still unreadable. Requests that name a concrete model are now
classified **when they are not yet part of a session** (see below), which is
what fills this column.

A concrete model still routes exactly where the client asked: classification on
that path is for the record, never for the route, and `ExplicitModel` semantics
are unchanged. It costs at most one classifier call per session, because a pin
recorded after the first successful call means later turns skip it. A
title-gen request is the exception that keeps classifying — its session key is
derived from the conversation text, which changes on every call, so it is never
part of a session and every one of its requests is a first request. That is
correct rather than unfortunate: it is the only way to know it is a title
request. Declared first and `decisive: true`, a `kind:` signature also skips
every model-backed classifier behind it, so an identified request pays for no
classification call at all.

**A title request is nested into the lanes view under the session it named.**
The two-tier resolution (`ParentSessionForTitle`) is described in "Admin web UI"
above; what belongs here is the rendering rule. A resolved title line becomes a
`Children` entry on its parent lane, the same rendering `attachTraceChildren`
uses to nest a classifier call under the request that spawned it — and, since
the lane is oldest-first within its own timeline while the title call is
chronologically later than what it names, it sits immediately after the turn it
titled. A title line that cannot be placed still renders — no request is ever
hidden — with a tag explaining why: resolved but the parent session has no row in
the window, resolved-search genuinely found nothing, or (distinctly) the
content-hash tier could not run at all because `capture_content` is off. The
three read differently on purpose: with capture off, every title line would
otherwise look identically "unmatched", which reads as the feature being broken
rather than as an expected consequence of a storage setting.

Every request's inbound headers are captured and shown on its detail page —
`User-Agent` is what tells two otherwise-identical requests apart by client.
Anything credential-shaped (`Authorization`, `Cookie`, any header with `token`,
`secret`, `api-key`, or `password` in its name) is masked to `[REDACTED]`
before it ever reaches the store; this is a single-operator tool (see
REQUIREMENTS.md), so the bar is not persisting secrets into the db file, not
hiding headers from the person running Arbiter.

`model` is what Arbiter asked the upstream for — the routed model. A
meta-router alias (OpenRouter's `openrouter/auto` being the motivating case)
can pick something else entirely and say so in its own response body; when
the upstream's reported model differs from what was requested, that's
captured as `actual_model` and shown on both the request list and detail
pages (`→ <model>` inline, and a labelled tag on the detail page). It's `null`
in the common case — a plain provider that just serves the model it was
asked for — so the column stays sparse rather than duplicating `model` on
every row. The client already sees the real model too, in its own response
body's `model` field (unaffected by this — Arbiter never rewrites it); this
is what makes that same information visible in Arbiter's own admin surface
without reading raw responses.

`status_code` records what the *client* was told, so a request whose upstream
could not be reached at all — no response, no status — is stored as **502**, not
0. That matters for `?errors` and for the error counts in `/admin/stats`: both
test `status_code >= 400`, so a 0 was invisible to them and indistinguishable
from an unfinished request. Rows written before this rule keep their 0; it is a
go-forward fix, not a migration.

Request and response *bodies* are not stored by default. Set
`storage.capture_content: true` to store them — content-addressed and
deduplicated (below) — and `/admin/requests/{id}` then carries a `content`
array of the request/response blocks in conversation order. The
`request_text`/`response_text` fields on that response predate capture and are
always empty; `content` is the field that carries bodies.

### Admin web UI

`internal/ui` serves a read-only browser over the same event store, under the
same gate. It is HTML rather than JSON because the questions REQUIREMENTS §2
asks — what happened, to which conversation, at what cost — are answerable by
reading rows, not by summing them.

**The UI is a greenfield rebuild, not a port** (see `design/REDESIGN.md` and
`DESIGN.md`). Every page template and asset from the previous generation was
deleted rather than adapted, and the pages are landing one at a time; the four
that exist are `/admin/ui/overview` (landing), `/admin/ui/sessions`,
`/admin/ui/session?key=` and `/admin/ui/content/repeated` (+ its block
drill-down). Anything the old UI did that is not on that list is not
temporarily missing — it was removed on purpose and will be rebuilt fresh if it
is wanted.

It is a separate package from `internal/http` because it shares none of that
package's concerns (wire formats, SSE flushing, the request hot path) and brings
its own embedded assets. Its dependencies are exactly `StatsHandler`'s: a
`*store.Reader` and a logger. Nothing is fetched at runtime — `htmx` and our CSS
are embedded in the binary (`internal/ui/static/`, see `THIRD_PARTY.md`), so the
UI needs no network of its own and no JS build step.

Templates are parsed **one set per page** (`internal/ui/ui.go`'s `pageFiles`),
because Go's `html/template` cannot redefine a block name within a single set:
a shared `{{define "content"}}` is only reachable that way. Parsing happens at
construction, so a broken template fails at process start rather than on the
first request to that page. Adding a page means adding its name to `pageFiles`.

Three behaviours worth knowing, because they are deliberate and look like bugs
otherwise:

- **A disabled store renders an explanatory page, not a 503.** The JSON surface
  returns 503 for `/admin/stats*` (correct for a scripted client), but a browser
  landing on a JSON error body is a dead end. The page says which setting is
  missing. Fragment requests — the htmx swaps — do get a 503, because they are
  swapped into a page that already explains itself.
- **The asset tree is behind the gate too**, so an unauthenticated peer cannot
  enumerate it. The consequence: with `forward_auth_header` set, a browser
  pointed directly at loopback gets a 401 on the CSS and sees an unstyled page.
  Through Caddy it is fine — the proxy injects the header on every request,
  assets included.
- **`/admin/ui/content/repeated/state` is the only write under `/admin/ui/`.**
  Every other route there is a read, and the rebuild has kept that line. It is
  registered with its own `Methods("POST")` the same way `/admin/reload` is, so
  a fronting proxy's `forward_auth` gate can allow every GET under `/admin/ui/`
  while still denying this one specifically, if it chooses to draw that line.

`/admin/ui/sessions` and `/admin/ui/session?key=` are the conversation view.
The store records one row per *request*, but a conversation is the unit a client
actually has, and the transcript is what makes the captured content readable.
The index renders one *lane* per conversation — a header line (session id,
counts, first-message preview, cost) above a full-width timeline of that
session's requests, with collapsed streamed runs drawn as stack nodes. Clicking
a node opens a detail panel; the timeline compresses its own nodes
(`laneTimeline.js`) so a 20-request session fits the lane width instead of
scrolling sideways.

Two properties are worth knowing because they are deliberate:

- **The index is windowed, the transcript is not.** Session turns and cost are
  aggregated over the index's window, so a conversation straddling the window
  edge reads short there and its `first seen` is the window edge. That is the
  trade for keeping `idx_requests_session` instead of a per-session subquery over
  all time; the index says so rather than implying those are the session's totals,
  and the transcript is the unbounded view, so the two can legitimately disagree.
- **The transcript shows what each turn *added*, not what it re-sent.** A client
  re-sends its whole history every turn, so rendering each turn's payload repeats
  the same text once per turn — a 3-turn conversation shipped its 11.7k-character
  system prompt three times. Each turn therefore shows its own stats (with a link
  that opens exactly that request), the system preamble as a separate collapsible
  field on the turn that introduced it, and the content the turn introduced.
  Everything re-sent becomes one line naming the turn that first showed that
  text. The match is by content hash, so "already sent" means the same bytes, not
  something similar; and the replayed bodies are not rendered at all, since a
  collapsed `<details>` still ships its contents to the browser.

Session keys are opaque and can be 64 hex characters, so both screens show them
truncated to 10 with the full value in the tooltip and in every link. The
truncation is display-only — a shortened key in an href would fetch the wrong
conversation.

**The grouping rule survives the rebuild** and now lives in the lanes view.
Rows are folded by `foldRequestLines` (`internal/ui/requests.go`): one line per
distinct thing that happened, keyed on session plus routing facts (provider,
model, alias, status, request kind), with **only streamed runs folding**. A
non-streamed request never joins a line with another. Two consequences that are
deliberate rather than incidental: there is **no time window** (the gap between
two turns is a tuning knob with no correct value, so identity binds a line, not
proximity), and **a burst of one row per different session is not folded at
all** — an upstream outage writes one failed row per conversation, which is
genuinely 31 different things happening, not a repeat.

Classifier calls are nested under the request that spawned them
(`attachTraceChildren`), and a resolved title-generation request is nested under
the conversation it named (`attachTitleChildren`). A line that cannot be placed
still renders — no request is ever hidden — with a tag explaining why, because
the three failure modes (no parent row on this page, resolved-search found
nothing, and the content-hash tier could not run because `capture_content` is
off) read identically as "unmatched" and mean different things.

**A title request is tied back to the session it named**, both in the store and
in the admin UI, because on its own a title-gen row is an orphan — its own
session key is derived from the conversation text and matches nothing else, so
nothing links it to the conversation it titled without help.
`Reader.ParentSessionForTitle` (`internal/store/discovery.go`) resolves the link
in two tiers, tried in order:

1. **Session-affinity header.** When `session_affinity.header` is configured
   (see below) and the client sends it on both the title call and the real
   turns — Hermes does this by design, calling it `session_affinity_header` on
   its own side, and sends it on title-generation calls specifically — the
   title request's own `session_key` *is* the parent's, an exact indexed
   lookup.
2. **Content-hash fallback**, tried only when the header match finds nothing:
   joins on shared `role="user"` content-ref hashes against other requests,
   excluding other title requests (so two title-gen retries of the same
   opener never link to each other), and returns the best match by row count.
   This tier needs `storage.capture_content: true` — there is nothing to hash
   without it.

**`/admin/ui/overview`** answers two questions: where do requests actually go,
and did the last config change make that better or worse.

The page is a **routing-flow diagram** — aliases on the left, the models they
reached on the right, ribbon width proportional to request share — above a KPI
strip (requests, sessions, cost, cost per 1M tokens, cache hit, error rate).
Clicking any node opens a drawer with that node's rate/efficiency numbers, a
cache-hit gauge, and the individual routes behind it.

`?since=` takes a Go duration for the trailing window (default `24h`).

**Compare mode is entered by choosing an anchor, not by a separate control.**
There is no `?mode=` parameter: `?anchor=` (an RFC3339 timestamp) means compare,
and its absence means one window. The earlier shape — a Single/Compare button
pair beside the anchor select — let the two controls contradict each other, so
picking a change in single mode silently did nothing and clicking Compare
submitted without one. Deriving the mode from the anchor removes the contradiction
structurally.

The anchor comes from either of two inputs, and **the free-form one wins when both
are set** (`?anchor_at=YYYY-MM-DDTHH:MM`, a real `datetime-local` picker, for a
moment that is not a config change — a deploy, an upstream incident, "when I
noticed it got slow"): the config-change select keeps its previous value on
submit, so preferring it would make the picker look dead.

`?span=` decides what the two windows are:

- `to_now` (default) — the anchor to now, against an equal span before it.
- `fixed` — one `since`-long window on each side of the anchor.

With `span=to_now` the duration is genuinely unused, because both sides are
measured from the anchor. The control is therefore **disabled and labelled "set
by the change"** rather than left looking live, and its value is carried in a
hidden field so switching span back does not lose it. A filter that silently
stops applying is indistinguishable from a broken one — that was a real report,
not a hypothetical.

An unparseable parameter is a 400 naming it, never a silent fallback — this page
exists to attribute a change to a cause, so quietly answering a different
question is worse than an error.

Both compared windows are named in the toolbar with their real bounds, built from
the windows actually queried, so the label cannot drift from the data below it.

Four things about its numbers are deliberate, and are also the reasons the page
looks the way it does:

- **Cost per 1M tokens, never cost per request.** Request sizes on this traffic
  vary by orders of magnitude, so a per-request average tracks how big the calls
  happened to be rather than how expensive a route is. The per-token rate is also
  what makes compare mode meaningful: a config change that halves the price while
  traffic doubles shows up as a raw cost *increase*, and only the rate answers
  "did this help?".
- **Cache hit is measured against cacheable tokens only** —
  `cache_read / (cache_read + input)`. Only prompt tokens can be cached, so
  folding output tokens into the denominator would dilute a well-cached route
  with volume that was never eligible. A route that moved no prompt tokens shows
  `—`, not `0%`: "nothing was cacheable" and "every read missed" look identical
  as a number and mean opposite things.
- **Estimated and metered cost are never summed into one labelled total.** The
  claude figures are API-equivalent pricing for comparison — the actual billing is
  a flat monthly plan with a rolling quota — while openrouter's are really
  metered. The KPI cell says `(partly est.)` and the page footnote spells it out.
- **A delta's colour comes from the metric, not the sign.** `store.DirectionOf`
  holds the per-metric direction, so a cache-hit rise is green, a cost rise is
  red, and request volume moving gets no colour at all: more traffic is neither
  good nor bad news.

The **config-change picker** is built from `requests.config_epoch`, so the
anchors offered are real changes rather than guessed timestamps. Arbiter reloads
on config file change, so a single editing session lands in the store as a burst
of epochs seconds apart; `store.ConfigEpochs` collapses each burst to the config
that actually served traffic, which is why an entry can read
`settled after 3 saves`. Free-form anchors still work — the list is a
convenience, not the only way in.

**The diagram's geometry is computed in Go** (`internal/ui/sankey.go`) and
emitted as SVG, not laid out in the browser: it is the one piece of real
arithmetic on the page, so it belongs somewhere `go test` can reach. Two limits
in it come from live data rather than taste — the route cap
(`store.MaxRoutingEdges`) folds everything past the busiest dozen into one
`other` band, and a node too short for its label draws a bar only, with its
identity in the hover title and the drawer. Both exist because a real 7-day
window has 23 routes whose long tail otherwise renders as overlapping labels.

There is no charting library: uPlot went with the old pivot-table Overview it
was vendored for.

Provider notes:

- `type` selects the wire format/transport, independent of the provider's name
  — a `type: openai` provider covers OpenAI, OpenRouter, LiteLLM, and
  Ollama's `/v1` alike.
- `endpoint` is normalized (trailing `/` stripped) at load time.
- Per-provider `timeout` defaults to 60s if unset. On non-streaming requests
  it is a total deadline. On streams it is an *idle* timeout instead: it
  bounds how long the upstream may go silent, and each event received resets
  it, so a long generation is never cut just for taking a while. Set it to `0`
  to disable the idle watchdog entirely.
- Per-provider `retry_max` bounds how many times a 5xx from that provider is
  retried before falling through to the fallback list. It defaults to `0`,
  which means *no* retries — set it explicitly to enable them.
- Optional per-provider `cache_ttl` overrides `session_affinity.default_ttl`
  for conversations pinned to that provider (e.g. to match its prompt-cache
  expiry).


That is why Arbiter **binds loopback by default** (`--bind`, default
`127.0.0.1`) and can bind a unix socket instead (`--socket /path`, overriding
`--bind`/`--port`). Anything that can reach the listener directly can forge the
gate header, so the binding — plus tailnet membership — is the actual control,
exactly the "further control via `reverse_proxy` config" the deployment
assumes. Set `--bind 0.0.0.0` only when something else (Caddy, a tailnet ACL)
is enforcing reachability.

## The live tail

**The tail is broken and unmounted; both halves are tracked as #57.** Two
independent defects, either of which would stop it working:

1. **It returns 200 with an empty `html` for every row.** `renderTailRow`
   (`internal/ui/live.go`) renders each row through the `req-line` partial,
   which was deleted with the old requests page — and the error is swallowed by
   design (one bad row must not take out the response), so the endpoint reports
   success while producing nothing. Verified live: ids, group keys and cursor are
   correct; every `html` is `""`.
2. **No page mounts it.** `live.js` activates only on a `[data-tail-src]`
   element and nothing in the template tree renders one; the deleted requests
   page was its only mount point.

So the endpoint, the cursor and the group-placement logic are all tested and
unchanged, but nothing observes it and it could not render a row if it did.
Where it belongs instead (the Sessions lanes view, and what that costs) is
scoped in #57.

What it does, for when it is restored: while the control is on and the tab is
visible, the page polls the endpoint and prepends new requests as they arrive. It
reports how many arrived and the time of the last poll, which is the point of
watching one — a tail that has silently stopped looks exactly like a store with
no traffic.

Three things about it are deliberate and each was wrong in a first version:

- **A poll returns rows newest-first**, and the client prepends them. The list is
  `ts DESC`, so a new row belongs at the top; returning batches oldest-first and
  appending them put a request from one second ago below rows hours older than it.
  "Rows after a cursor" reads like a forward walk, and that reading is wrong here.
- **The cursor is an opaque base64 token inside a JSON response**, not a data
  attribute. It is the stored `ts` text, which contains `+0000 UTC`; putting that
  in an attribute and reading it back with `getAttribute` round-trips it through
  HTML escaping, so the value that returns is not byte-for-byte the value sent —
  and a cursor that differs by one character compares wrongly against the column.
  This is the same class of silent failure as `strftime` over `ts`.
- **It is hand-written JavaScript, not `hx-trigger="every 5s"`.** A declarative
  trigger cannot skip a hidden tab, cannot append instead of swapping the table
  under the reader, cannot stop after repeated failures, and cannot say how many
  rows arrived. The last of those is the main reason to look at a tail at all.
  (#57 notes that these same three reasons apply to whichever shape the rebuilt
  tail takes.)

A poll is capped (50 rows) and returns the newest rows that fit, so a burst larger
than that in one interval is truncated — reported as such rather than shown as a
quiet period. The cap is bounded on purpose: an unbounded query is not a tail.

The tail was only ever offered on the newest page. On a later page the newest row
is not on screen, so "everything after what you are showing" would mean starting
the view from the middle of history; the control was simply absent there.

**The tail follows the table's mode.** In the grouped list a polled row is placed
by its group: one that matches a line already on screen bumps that line's count
and moves it to the top, and one that matches nothing becomes a line of its own.
The server sends each row's group with the row, computed by the same function
that folded the page, so the two cannot disagree about what a group is. In Flat
mode every row was prepended as before. The mode travelled on the tail's own
query string, so a flat list got a flat tail. (`?flat=1` was the requests page's
own escape hatch and went with that page; the group-placement logic in `live.js`
is still there for the rebuild to reuse — note it targets `.reqrow` list rows,
which the lanes view does not have, so #57 prices that reuse.)

## Grouping: a run of streamed turns is one line

The folding rule is described under "Admin web UI" above, where it now lives
(the lanes view). Kept here are the two data points that motivated it, since they
are the argument against ever adding a time threshold:

A streamed conversation writes a row per turn, so a working session filled the
old list with near-identical rows — measured on the live store, the newest 100
rows held **four** distinct lines (96 streamed turns across two sessions, plus
four classifier calls), and the events worth seeing were scrolled off by the
repeats.

**No time window, ever.** The gap between two turns is a tuning knob with no
correct value — measured merge counts climb smoothly with it (92 at 2s, 718 at
5s, 1721 at 15s, 2356 at 30s) — so the design has no threshold to mis-set. What
binds a line is identity, not proximity. The same measurement is why adding one
later would be wrong rather than merely unnecessary: **a burst of one row per
*different* session is not folded at all**, and that is the requirement doing the
work rather than a happy accident. An upstream outage writes one failed row per
conversation; measured, the 2026-09-17 burst was 31 sessions in one minute,
nothing to fold. A time-window rule would have merged 1,152 cross-session pairs
at 30s — that is, most of what it folded would have been different
conversations.

**Unpinned requests never fold into each other.** A request with no session key
is not in a conversation, so two of them have no demonstrated relationship and
no claim of repetition holds. Their keys carry the row id, which makes each its
own line.

## Discovery: the blocks that recur

The content-addressed design exists so that "find the text that appears in every
query" is a query rather than a guess: the same block is stored once and
referenced everywhere it appears, so recurrence is a `GROUP BY` on
`content_refs` with no need to know the text in advance. The discovery page is
that query with its parameters exposed.

**`min_sessions` is the control that makes it useful**, and it defaults to 2
rather than the JSON endpoint's 0. A block repeated within one conversation is the
ordinary shape of a multi-turn chat — every turn re-sends the system prompt — so
at 0 the list is topped by something that is not a finding. The number that
answers "is some client injecting this?" is how many *distinct sessions* carry
the block. 0 is still accepted, and is the right setting for "what is being
re-sent most".

**The page states two numbers the thresholds can hide.** `COUNT(DISTINCT
session_key)` ignores NULL, so requests with no session key contribute nothing to
`min_sessions` — on a store where most traffic is unpinned, a genuinely
widespread block can still fail the threshold, and the result reads as an absence
of boilerplate rather than as a consequence of the filter. The sessionless count
and the window's total distinct blocks are both shown for that reason: an empty
list should say "the thresholds removed them", not "there are none".

Two details of the query are worth knowing:

- **A content hash is a 32-byte blob in `content_refs.hash` and a 64-character
  hex string in a URL.** These are different values. Binding the hex form
  directly compares text against a blob — it matches nothing, and the result is
  indistinguishable from a block that is simply absent, which is the same class
  of silent failure as `strftime` over the `ts` column. `decodeHash` refuses
  anything that is not a valid 32-byte hex form rather than allowing it.
- **The drill-down cannot join `content_refs`.** A join multiplies a request once
  per matching *reference*, so a block re-sent in three messages of one
  conversation would be listed three times. It uses `id IN (subquery)`, which
  also lets it share `requestRowColumns` and `scanRequestRow` with the request
  list so the two cannot drift.

### Content store

Capture is content-addressed at **message-block** granularity: every block is
hashed and the body stored once, with a reference row recording which request it
belonged to, in which direction, and at which position. That granularity is the
whole point — a conversation re-sends its earlier turns on every request, so
hash-the-whole-request would store turn 1's text 28 times, while hashing each
block makes turn 28 cost one new body plus references to blocks already there.
A block whose bytes are deliberately not kept (images and other binary content —
hash only, no file) still gets its reference and still deduplicates.

Capture runs **before** pre-guardrails, so what is stored is the request as the
client sent it. A `system_prompt` guardrail rewrites `NormalizedRequest`; if
capture ran after it, the store would hold Arbiter's own injected text as though
the client had sent it.

The system prompt is kept as its own block rather than folded into the first
user turn, and text blocks are hashed over their raw text rather than JSON. Both
choices exist so one specific question is answerable:

**`GET /admin/content/repeated`** returns blocks seen across multiple requests,
with how many requests and how many distinct sessions contain each. That is the
post-hoc detection mechanism for client-injected boilerplate: the preamble a
client prepends to every request shows up as a block spanning many sessions,
without anyone having to know the prompt in advance. `min_sessions` is what
separates it from mere repetition — a block in every request of one long
conversation is unremarkable; the same block across many sessions is the
client's own text.

All parameters are optional: `since` (default `7d`), `min_requests` (default 2),
`min_sessions` (default 0 — the **discovery page** defaults it to 2, which is
the useful setting), and `limit` (default 50, max 200). A malformed value is a
**400**, not a silently ignored filter. The page at `/admin/ui/content/repeated`
is the same query with the thresholds exposed as controls.

Retention is two independent clocks, and only one of them applies here. Captured
bodies expire on `storage.content_ttl` (a Go duration — `72h`, not `3d`);
**empty or `0` means never expire**, so retention is opt-in and an upgrade cannot
silently start deleting data. Request metadata is *not* on a clock: routing,
cost and epoch data stay useful for months while conversation text does not.
Reclaiming is a two-step sweep — delete references whose request is older than
the TTL, then delete any body no longer referenced — run on startup and hourly
thereafter, on its own connection so it never contends with the writer. There is
no reference count to keep correct; that is deliberate, because refcounts are
where content-addressed collectors historically go wrong.

Requests Arbiter refuses — a pre-guardrail rejection, a routing failure, or an
operator's `stop` rule (see "Routing rules" below) — get a **real row in
`requests`**, the same as any other client request: `status_code` and `error`
set from the same mapping (`pkg/errors.StatusFor`) that decides what the
client itself is told, `kind` stays `"client"`, and captured content attaches
the normal way under `owner_kind = 'request'`. "If it happened, it should be
shown" (#5) — a request Arbiter refused is not a separate, invisible class of
traffic, so it appears in the requests list, `/admin/stats`, and every cost
aggregate exactly like a served request. Cost is `0` when nothing reached a
provider (the common case — a guardrail or routing refusal happens before any
upstream call); a failure *after* an upstream call already went through the
upstream-failure path, which records the real spend. A refusal this early has
no resolved `provider`/`model` — those columns are blank, which reads as
"never got that far" rather than a defect. (A translation failure — the
payload didn't even parse — has no conversation to record at all, so it gets
neither a row nor stored content; there is nothing to show.)

Classifier note: `axis` is optional on a `heuristic` classifier (defaults to
`domain`, as before this field existed). The legacy `capability_detector` type
always fills `capabilities` and rejects an explicit `axis`. In a policy rule's
`when` clause the older key spellings `intent` (for `domain`) and
`cost_sensitivity` (for `cost_class`) are still accepted, but setting a key and
its replacement on the same rule is an error rather than a silent pick.


## Guardrails

Guardrails are pre/post hooks that run on every request: pre-hooks on the
`NormalizedRequest` before routing and the upstream call, post-hooks on the
`NormalizedResponse` after it. Everything from prompt injection to rate
limiting is a guardrail — there is no special-cased path for any of it, and a
new behaviour is a new type rather than a branch in the pipeline.

```yaml
guardrails:
  pre:
    - name: "baseline"
      type: "system_prompt"            # inject/override a system prompt
      config:
        prompt: "You are a helpful assistant."
        override: false                # false prepends; true replaces
    - name: "rl"
      type: "rate_limit"
      config:
        per_minute: 60                 # 0 means unlimited
        per_day: 1000
  post: []
```

Types, in the order you are likely to reach for them:

| `type` | What it does |
|---|---|
| `system_prompt` | prepends (or with `override: true`, replaces) the system prompt |
| `rate_limit` | per-minute / per-day request caps |
| `prompt_rewrite` | matches client-injected prompt text and strips, replaces or blocks it (see Prompt rewriting) |

### System prompt injection

`system_prompt` is the oldest guardrail and the one that runs first in the
common case. With `override: false` — the default, and the useful one — the
configured prompt is **prepended** ahead of whatever the client sent, so
Arbiter's baseline instructions always apply while the client's own are still
there. With `override: true` the client's system prompt is discarded entirely,
which is occasionally what you want for a fixed-purpose deployment and is
otherwise a good way to break a coding agent.

It has one load-bearing interaction: capture runs **before** pre-guardrails, so
what the store holds is the request as the client sent it, and the session key
is hashed before this guardrail runs. Otherwise Arbiter's own injected text
would be stored as though the client had sent it, and every live pin would be
invalidated the moment this prompt was edited. Both are covered under
Content store and Session affinity.

### Rate limiting

`rate_limit` caps how many requests Arbiter makes, per minute and per day.
`0` means unlimited, and a `0` window is not even queried.

The counters are **seeded from the event store** when one is configured, which
is what makes a cap mean anything: a purely in-memory counter is thrown away
every time the pipeline is rebuilt, so a per-day cap of 1000 became 1000 again
after each config save. Seeding also carries a cap across a restart, which is
closer to what the number is for than the original behaviour was. With no store
configured the caps count only what this process has seen — the behaviour
before seeding existed, and the correct degradation.

Counting deliberately includes Arbiter's **own internal requests** (classifier
and title-generation calls). Those are real upstream requests, and a cap that
mirrors an upstream's own published limit has to see them; the spend
aggregates filter them out for the opposite reason.

The store is queried once, lazily, on first use, and requests since are counted
locally — so the hot path carries no query, at the cost of the count being
approximate if another process wrote to the same store (which cannot happen:
Arbiter is one binary per deployment). A store error degrades to local counting
rather than failing every request; under-counting is the safer failure for a
limit.

`Reader.CountSinceByProvider` exists for a per-provider cap that mirrors an
upstream's published limit, and its index is in place, but no config surface
exposes it yet.

## Prompt rewriting (client-injected prompts)

Client apps (opencode, Claude Code, …) prepend their own system prompts — prompts
you didn't write and often can't see. They're re-sent on every request, so they
inflate token counts, defeat prompt caching across clients, and can silently
override your own instructions. The `prompt_rewrite` guardrail matches and
strips, replaces, or blocks that text.

```yaml
guardrails:
  pre:
    - name: "strip-opencode-preamble"
      type: "prompt_rewrite"
      config:
        match: "You are opencode, the best coding agent"
        mode: "prefix"          # exact | prefix | regex
        action: "strip"         # strip | replace | block | strip_paragraph
        where: ["system"]       # system | messages | all
```

**Matching is on the text, not on a client identity.** A per-client key would let
you write "this client injects X"; matching X directly is simpler and survives a
client renaming itself. Per-client attribution is a separate concern (see the
Access section) and isn't needed here.

| `mode` | Matches when |
|---|---|
| `exact` | the searched text **is** the pattern (ignoring case and surrounding whitespace) |
| `prefix` | the searched text **starts with** the pattern (default) |
| `regex` | a Go regular expression matches anywhere |

| `action` | Effect |
|---|---|
| `strip` | removes the matched span, leaving surrounding text |
| `replace` | substitutes `replacement:` for the matched span |
| `block` | refuses the request with `block_status:` (default 403) |
| `strip_paragraph` | removes the **whole paragraph** containing the match |

`strip_paragraph` exists because a pattern matching part of a sentence leaves a
dangling fragment under plain `strip`. The smallest unit removed is one
paragraph, split on `paragraph_boundary:` (default newline), and the seam is
tidied so removing a middle paragraph doesn't leave a blank-line crater.

`where` selects what's searched: `system` (default), `messages`, or `all`. Only
**text** blocks are rewritten — a `tool_use` or `attachment` block isn't free
text, and rewriting it would corrupt the request.

`exact` and `prefix` are case-insensitive and ignore leading/trailing whitespace,
because different clients and wire formats re-serialize the same preamble
differently and a case difference isn't a different prompt. `regex` is used
verbatim — write `(?i)` yourself if you want case-insensitivity. **Watch the
character class**: `\w` excludes `-`, so `you are \w+` does not match
`claude-code`; use `[\w-]+`.

A typo'd `mode`, `action`, or `where` is a **config-load error**, not a silent
no-op — a guardrail that quietly rewrites nothing would leave you believing
injected text is stripped while every request still carries it.

**To find what to strip**, `/admin/content/repeated` (and the discovery page)
lists blocks appearing across many requests and sessions — the client's own
preamble shows up there, with a preview, before you write a rule for it.

## Testing

```bash
devenv shell
test   # go test -race ./...
```

For live end-to-end checks, `devenv processes up` runs a fake LLM
([fakellm](https://github.com/1dg618/fakellm), config at
`support/fakellm.yaml`) alongside arbiter, and the `mock` script fires the
same streaming request at both to compare:

```bash
devenv shell
mock
```

## Design Principles

- **Transparency over magic**: every routing decision explains itself in one line
- **External services stay external**: memory, search, MCP hosting → HTTP calls, not embedded
- **Hooks over special cases**: guardrails are composable pre/post hooks, not special logic
- **Composable routing**: independent axes (domain, cost tier, latency, capability) stacked together
