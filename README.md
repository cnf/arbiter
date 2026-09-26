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
- **Pointing a client at it:** **[docs/clients.md](docs/clients.md)** — endpoints,
  streaming, attachments, session affinity, prompt caching.
- **Deciding where requests go:** **[docs/routing.md](docs/routing.md)** —
  the `model` precedence, group selection, capability-aware rules, and the
  three ways a request gets classified.
- **Every config key:** **[docs/configuration.md](docs/configuration.md)** —
  the complete loadable example, and where each subsystem's docs pick up from
  there.
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
overrides both. Loopback is the default on purpose — see [Admin surface and
access](docs/observability.md#admin-surface-and-access).

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

## Architecture

Hub-and-spoke: every wire format converts to/from a `NormalizedRequest` /
`NormalizedResponse` / `NormalizedStreamEvent` and never directly to another
wire format. Adding a provider wire format means adding one spoke (to/from
Normalized), not an N×N matrix. The HTTP layer knows the caller's format from
which endpoint was hit; routing picks a provider, and the provider's `type`
field selects the outbound wire format (`anthropic`, `openai`, `ollama` — the
last being OpenAI-compatible transport, preserving provider identity).

## Configuration

`arbiter.yaml` (path configurable via `--config`), hot-reloaded, strict on
unknown fields, and documented in full — every key, the complete loadable
example, and where each subsystem's docs pick up from there — in
**[docs/configuration.md](docs/configuration.md)**.

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
