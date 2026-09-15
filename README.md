# Arbiter

An LLM proxy/gateway with transparent routing decisions, composable classification axes, and independent observability.

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

Run everything through the devenv process manager (`devenv processes up/down`),
which starts fakellm (port 5665) before arbiter (port 8080). Note: builds,
tests, git commits, and anything touching secrets must run inside `devenv
shell`; secrets access requires a reason (`SECRETSPEC_REASON="..."` or
`--reason`).

## Endpoints

| Path                 | Wire format                |
|----------------------|----------------------------|
| `POST /v1/messages`  | Anthropic Messages API     |
| `POST /chat/completions` | OpenAI Chat Completions |
| `GET /models`, `GET /v1/models` | model list        |
| `GET /health`        | liveness                   |

Both chat endpoints accept `stream: true` and respond with SSE in the same
wire format as the request (formats are never mixed). Every response —
streaming or not — carries `X-Arbiter-Trace-Id`; OpenAI-style response IDs are
built from it (`chatcmpl-<trace-id>`), so a response can be correlated with
Arbiter's structured logs by ID alone.

## Project Structure

```
arbiter/
├── cmd/
│   └── arbiter/          # main entry point; wires config -> pipeline
├── internal/
│   ├── http/             # ingress: the two endpoints, SSE flushing, trace IDs
│   ├── pipeline/         # request lifecycle: normalize -> guardrails -> classify -> force -> route -> upstream
│   ├── router/           # routing: policy rules, simple default/fallback, alias resolution
│   ├── classifier/       # per-axis routing signals (domain, effort, cost class, capabilities)
│   ├── guardrail/        # composable pre/post hooks (system prompt, rate limit)
│   ├── translator/       # Anthropic <-> OpenAI <-> Normalized conversions (incl. SSE events)
│   ├── upstream/         # provider HTTP calls, SSE reading, response parsing
│   ├── logging/          # single structured logging path (JSON, trace-correlated)
│   └── config/           # lanes.yaml loading (strict: unknown fields rejected)
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

`lanes.yaml` (path configurable via `--config`). Environment variables expand
into values as `${VAR_NAME}`. Unknown fields are a config error, not silently
ignored.

The file is hot-reloaded: edits are picked up without a restart. A reload
rebuilds the whole pipeline (providers, routers, classifiers, guardrails) and
swaps it in atomically; requests already in flight finish on the old config,
and a reload that fails to load, validate, or build is rejected with a logged
error while the previous config keeps serving.

```yaml
providers:
  claude:
    type: "anthropic"                  # anthropic | openai | ollama
    endpoint: "https://api.anthropic.com"
    key: "${ANTHROPIC_API_KEY}"
    models: ["claude-3-opus-20250219", "claude-3-haiku-20250307"]

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

routers:
  - name: "policy"
    type: "policy"
    config:
      rules:
        - when: { domain: "code_generation", effort: "hard" }
          target: "cheap-claude"       # a rule target may name an alias
        - when: { capabilities: ["vision"] }
          provider: "gpt4"             # ...or a literal provider/model
        - when: {}                     # catch-all
          provider: "claude"
  - name: "primary"
    type: "simple"                     # chained after policy: last-resort default
    config:
      default_provider: "claude"
      # fallback_provider: "gpt4"

guardrails:
  pre: []                              # system_prompt, rate_limit
  post: []

routing:
  fallback_providers: ["gpt4"]         # tried in order on 429/5xx

session_affinity:
  header: "X-Session-Id"               # inbound header carrying a session id
  default_ttl: "5m"                    # idle TTL for a pinned conversation

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

Aliases are client-facing and appear in `/models` alongside provider models
(listed with provider `"alias"`). Any rule `target` may name an alias, and a
group member may itself be another alias; resolution is depth-limited and a
cycle is rejected at config load.

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

The catalog file is inert on write: the config watcher tracks only the config
file itself, so regenerating `catalog.yaml` does not reload anything until you
ask for it.

The file is meant to be produced by a converter that normalizes an external
price list (e.g. LiteLLM's `model_prices_and_context_window.json`) into this
shape — the runtime never parses a foreign schema. That converter is a
separate follow-up.

Classifier note: `axis` is optional on a `heuristic` classifier (defaults to
`domain`, as before this field existed). The legacy `capability_detector` type
always fills `capabilities` and rejects an explicit `axis`. In a policy rule's
`when` clause the older key spellings `intent` (for `domain`) and
`cost_sensitivity` (for `cost_class`) are still accepted, but setting a key and
its replacement on the same rule is an error rather than a silent pick.

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

### Session affinity

Once a request has been routed, Arbiter pins the conversation to whichever
provider/model actually served it, so later turns skip classification and
routing entirely and stay on the same upstream — preserving prompt-cache
reuse and avoiding cost churn from turn-to-turn re-routing. The session key
is the `X-Session-Id` header (configurable via `session_affinity.header`)
when present; otherwise it's a hash of the system prompt plus the first
text-bearing user message. A conversation whose opening carries too little
text to be distinctive (say, a bare "hi") is deliberately *not* pinned:
pinning two unrelated chats together is worse than not pinning at all.

The pin overrides classification for as long as the client keeps requesting
the **same `model` value**. A client that explicitly switches models means
it, so the pin is discarded and routing runs fresh. The pin is recorded from
the route that *actually served* the request, so it follows a fallback to
another provider; a pinned provider currently in 429 cooldown is treated as a
miss (fresh routing runs). Pins are in-memory and idle-expiring (refreshed on
each hit); they reset on restart.

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
