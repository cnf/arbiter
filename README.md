# Arbiter

An LLM proxy/gateway with transparent routing decisions, composable classification axes, and independent observability.

## Building

All development happens in devenv:

```bash
devenv shell
go build -o arbiter ./cmd/arbiter
```

Or use the dev script:

```bash
devenv shell
dev  # runs air (hot reload)
```

## Project Structure

```
arbiter/
├── cmd/
│   └── arbiter/          # main entry point
├── internal/
│   ├── router/           # routing logic
│   ├── guardrail/        # pre/post hooks
│   ├── translator/       # Anthropic ↔ OpenAI format conversion
│   ├── logging/          # structured logging & tracing
│   └── config/           # config loading & hot-reload
├── pkg/
│   └── ...               # public-facing packages if any
├── devenv.nix
├── README.md
└── go.mod
```

## Configuration

(TBD: lanes.yaml shape, config hot-reload details)

## Design Principles

- **Transparency over magic**: every routing decision explains itself in one line
- **External services stay external**: memory, search, MCP hosting → HTTP calls, not embedded
- **Hooks over special cases**: guardrails are composable pre/post hooks, not special logic
- **Composable routing**: independent axes (domain, cost tier, latency, capability) stacked together
