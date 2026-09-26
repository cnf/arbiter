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
- **Deciding where requests go:** **[docs/routing.md](docs/routing.md)** —
  the `model` precedence, group selection, capability-aware rules, and the
  three ways a request gets classified.
- **Every config key:** [Configuration](#configuration)
- **Reading what it recorded:** **[docs/observability.md](docs/observability.md)** —
  the event store, the admin UI, the live tail, grouping, discovery.
- **Controlling what goes through:** **[docs/guardrails.md](docs/guardrails.md)** —
  system-prompt injection, rate limiting, prompt rewriting.

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

See **[docs/observability.md](docs/observability.md#event-store)**.

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

See **[docs/routing.md](docs/routing.md#precedence-how-model-selects-a-route)**.

### Session affinity

Session affinity — how a conversation is pinned to a provider, and the two ways
its key is derived — is documented in
**[docs/clients.md](docs/clients.md#session-affinity)**.

### What survives a config reload

Which runtime state survives a reload or a restart (session pins, rate-limit
counters, provider cooldowns) is documented in
**[docs/clients.md](docs/clients.md#what-survives-a-config-reload)**.

### Group selection strategies

See **[docs/routing.md](docs/routing.md#group-selection-strategies)**.

### Routing on what a model can do

See **[docs/routing.md](docs/routing.md#routing-on-what-a-model-can-do)**.

### LLM-backed classification

See **[docs/routing.md](docs/routing.md#llm-backed-classification)**.

### Decision-model classification (`type: "decisions"`)

See **[docs/routing.md](docs/routing.md#decision-model-classification-type-decisions)**.

### What a classifier reads, and how much of it

See **[docs/routing.md](docs/routing.md#what-a-classifier-reads-and-how-much-of-it)**.

### Matching a request's own text (`match`)

See **[docs/routing.md](docs/routing.md#matching-a-requests-own-text-match)**.

### Structural capability detection (`detect`)

See **[docs/routing.md](docs/routing.md#structural-capability-detection-detect)**.

### Admin surface and access

See **[docs/observability.md](docs/observability.md#admin-surface-and-access)**.

### Admin web UI

See **[docs/observability.md](docs/observability.md#admin-web-ui)**.

## The live tail

See **[docs/observability.md](docs/observability.md#the-live-tail)**.

## Grouping: a run of streamed turns is one line

See **[docs/observability.md](docs/observability.md#grouping-a-run-of-streamed-turns-is-one-line)**.

## Discovery: the blocks that recur

See **[docs/observability.md](docs/observability.md#discovery-the-blocks-that-recur)**.

## Guardrails

Guardrails — prompt injection, rate limiting, and stripping client-injected
preamble text — are documented in **[docs/guardrails.md](docs/guardrails.md)**.

They are one mechanism, not three: composable pre/post hooks, where a new
behaviour is a new type rather than a branch in the pipeline.

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
