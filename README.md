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
│   ├── pipeline/         # request lifecycle: normalize -> guardrails -> classify -> route -> upstream
│   ├── router/           # routing logic (simple default/fallback today)
│   ├── classifier/       # intent/capability signals for routing
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

```yaml
providers:
  claude:
    type: "anthropic"                  # anthropic | openai | ollama
    endpoint: "https://api.anthropic.com"
    key: "${ANTHROPIC_API_KEY}"
    models: ["claude-3-opus-20250219"]

routers:
  - name: "primary"
    type: "simple"                     # default -> fallback, no policy yet
    config:
      default_provider: "claude"
      # fallback_provider: "gpt4"

classifiers: []                        # heuristic keyword classifiers
guardrails:
  pre: []                              # system_prompt, rate_limit
  post: []

logging:
  level: "info"
  format: "json"
  output: "stdout"
```

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
