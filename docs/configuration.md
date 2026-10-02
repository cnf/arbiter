# Configuration

`arbiter.yaml` (path configurable via `--config`). Environment variables expand
into values as `${VAR_NAME}`. Unknown fields are a config error, not silently
ignored.

The file is hot-reloaded: edits are picked up without a restart. A reload
rebuilds the whole pipeline (providers, routers, classifiers, guardrails) and
swaps it in atomically; requests already in flight finish on the old config,
and a reload that fails to load, validate, or build is rejected with a logged
error while the previous config keeps serving.

The example below is a complete, loadable config (a test asserts exactly that,
so it cannot rot): every provider it references is defined here. It is the
base every fragment in [docs/routing.md](routing.md) and
[docs/guardrails.md](guardrails.md) is grafted onto for its own test — the
`classifiers:` and `guardrails:` blocks below are the ones those tests
replace.

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
    axis: "domain"                     # domain | difficulty | cost_class | capabilities | tags
    config:
      keywords: { code_generation: ["write", "refactor"] }
  - name: "difficulty"
    type: "heuristic"
    axis: "difficulty"                 # a second instance, same type, own axis
    config:
      keywords: { easy: ["quick"], hard: ["architecture"] }

aliases:
  auto:                                # full auto: force nothing, classify + rules
    force: {}
  coding:
    force: { domain: ["code_generation"] }
  py:
    force: { tags: ["python"] }         # a force-alias can declare tags too
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
        - when: { domain: "code_generation", difficulty: "hard" }
          target: "cheap-claude"       # a rule target may name an alias
        - when: { capabilities: ["vision"] }
          provider: "gpt4"             # ...or a literal provider/model
        - when: { tags: ["python"] }   # freeform operator-owned labels; every
          target: "cheap-claude"       #   listed tag must be present
        - when: { requires_input_modalities: ["image"] }
          provider: "claude"           # skipped unless claude accepts images
        - when: { request_kind: "title" }  # who's asking, not what it's about
          target: "cheap-claude"       # title-gen traffic never needs a big model
        - when: { effort: "high" }     # the client's own reasoning effort
          target: "cheap-claude"       # route a high-effort ask without overriding it
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

Routing (aliases, group selection, policy rules, classifiers) is documented in
**[docs/routing.md](routing.md)**. Session affinity and what survives a
config reload are documented in **[docs/clients.md](clients.md)**. Guardrails
are documented in **[docs/guardrails.md](guardrails.md)**. The event store,
admin surface, and admin UI are documented in
**[docs/observability.md](observability.md)**.
