# Arbiter — Requirements

Single-user/household LLM proxy. The person running Arbiter and the person
using the client apps are the same person — this is *not* a multi-tenant
enterprise gateway. Guardrails exist to protect the operator from client apps
and models, not to police other users.

## 1. Routing

- Client selects behavior via the `model` field: either a real model name
  (pass-through) or a **virtual model alias** defined in config.
- A virtual model alias can:
  - force full auto-routing (content-classified, no domain lock),
  - lock a domain/intent (e.g. "coding") and auto-route within it,
  - pin a specific provider/model,
  - pin a model *group* (a named set of candidates to route within).
- Auto-routing must be able to select on **provider-side data**, not just
  request signals — cheapest model, lowest latency, etc. This data source
  starts as **static/declared** (config-inline or a fetched JSON/URL of
  model cost+latency) and should later become **measured/empirical** from
  real traffic — build so that swap doesn't require a rewrite.
- Request-content classification (today's heuristic classifier + signals)
  remains valid, but only as one input *within* a virtual model's routing —
  not the only routing mechanism.
- **Virtual model aliases are a routing primitive, not just a client-facing
  entry point.** Any router (content classifier, cost/latency router,
  future ones) must be able to name a virtual model alias as its target,
  not only a raw provider/model. E.g. a content classifier decides "this is
  an easy search query" → routes to alias `free-search`, where
  `free-search` is itself a latency-optimized model *group*. Aliases must
  be resolvable recursively/by reference from anywhere a provider/model
  target is expected today.

## 2. Insight / Observability

- Priority order: get basic routing right first, but capture enough data
  now that answering later questions doesn't require re-architecting.
- Must capture (persisted, queryable — not just stdout JSON lines):
  request/response metadata, routing decision + rationale + signals used,
  tokens, cost, latency, provider/model actually used, client identity (see
  Auth), and enough session/trajectory linkage to reconstruct "what
  happened across a session" (tool calls, sub-agent/child-request
  attribution where derivable).
- Target questions this data must be able to answer later: did a config
  change save or cost money; which models/providers get used most; what
  trajectory did a session take; what tools were used.
- Build order: capture/schema first; dashboard, cost aggregation, and
  alerting are downstream consumers of the same data, not separate
  logging paths — don't build a second capture mechanism for them later.

## 3. Guardrails

Keep:
- System prompt injection (force/prepend Arbiter's own system prompt).
- Request rate limiting (per-minute/per-day counts).

Add:
- **Cost/budget caps** — stop or warn on $ spend crossing a threshold
  (day/month), distinct from request-count rate limiting.
- **Upstream-mirrored limits** — both budget caps and rate limits should be
  definable *per-provider* to mirror the upstream's own published/observed
  limits (e.g. OpenRouter free-tier: 10 req/min, 1000 req/day), not only as
  a single global cap. Purpose is twofold: prevent Arbiter from hammering
  an upstream into its own 429s, and surface "how much of the upstream's
  limit is used" in the dashboard later — this ties into the Insight
  section's data capture, not just enforcement.
- **Client-injected hidden-prompt stripping** — client apps (opencode,
  Claude Code, etc.) inject their own system/tool prompts the operator
  didn't write and often can't see. Arbiter should be able to detect and
  strip these, and/or just surface them for inspection first. Configurable
  per client-key/route (log-only vs. strip), since detection strategy
  will need per-client tuning.

Explicitly out of scope:
- General content moderation/sanitization of user input — the operator and
  the client user are the same person; this isn't a trust boundary Arbiter
  needs to police.

## 4. Access / Auth

- Deployment: Caddy (or similar) in front, terminating TLS and doing
  `forward_auth`. Arbiter trusts Caddy's forwarded-auth headers.
- Additionally: **per-client/app API keys**, checked by Arbiter itself —
  not a full user-management system. These are explicitly **not a security
  measure** and are not a gate. Their job is:
  (a) **attribution** — "was this the automated script, opencode, or the
  iPhone?" — so usage in logs/insight data is attributed by client app, not
  just by network trust; and
  (b) **per-client shaping** (horizon shaping) — a key may carry
  client-specific overrides: defaults, the model horizon offered to that
  client, guardrail tuning (e.g. the §3 hidden-prompt stripper's per-client
  log-only-vs-strip setting).
  A missing or unknown key is therefore **not rejected** — it degrades to an
  *unattributed* client, unlike a gate. Eventual; not urgent.
- No user accounts, roles, or permissions system.

---

# Divergence Report

## Matches requirements as-is

- **Hub-and-spoke translation** (Anthropic ⇄ Normalized ⇄ OpenAI, incl. SSE)
  — solid, no requirement conflicts with this.
- **429/5xx fallback + cooldown** (`internal/pipeline/pipeline.go`) — fine,
  keep.
- **Hot-reload of `lanes.yaml`** — fine, keep.
- **`system_prompt` guardrail** (`internal/guardrail/guardrail.go`) —
  matches "system prompt injection" requirement, keep as-is.
- **`rate_limit` guardrail mechanism** (counters, per-minute/per-day
  windows) — the counting approach is reusable, but see divergence below:
  it's currently global/process-wide, not per-provider.
- **Structured JSON logging with trace-ID correlation**
  (`internal/logging/logger.go`) — good foundation, but see gaps below
  (stdout-only isn't "queryable").

## Diverges — needs changing

- **Routing is 100% request-signal-driven, with no model-alias layer.**
  `router.SimpleRouter` / `router.PolicyRouter` both decide off classifier
  `Signals` or a static default — there's no concept of the client naming a
  virtual model via the `model` field and that name carrying routing
  policy. This is the core mechanism the requirements need and doesn't
  exist yet.
- **No provider-side data in routing decisions.** `Signals.CostSensitivity`
  (`pkg/types/models.go:23`) is a string label a classifier assigns from
  request text (e.g. "budget") — matched literally in `PolicyCondition`,
  not compared against real provider cost/latency numbers. There is no
  provider cost/latency table anywhere in config or types. "Pick the
  cheapest/fastest provider" cannot be expressed today.
- **`rate_limit` guardrail is a single global cap** (`internal/guardrail/guardrail.go:84`
  `RateLimitGuardrail`) — one counter pair (per-minute/per-day) for the
  whole pipeline, applied pre-routing, before the provider is even known.
  There's no per-provider limit and no concept of mirroring an upstream's
  published limits. Needs to become provider-scoped (checked/incremented
  post-routing, or with per-provider counters keyed by chosen provider).
- **`Usage.CostUSD`** (`pkg/types/response.go:39`) is populated only when
  an upstream reports it directly (e.g. OpenRouter) — no static cost-table
  fallback, so most providers (plain Anthropic/OpenAI) show $0 always.
- **Observability is stdout-only.** `LogRouting`/`LogUpstream`/etc. write
  JSON lines and nothing else — no persistence, no session/trajectory
  concept, no tool-call attribution, no client-identity tagging. Fine as a
  transport, not sufficient as the queryable store the requirements call
  for.

## Missing entirely — needs developing

- **Model alias / virtual-model config layer.** New config section mapping
  alias name → {optional forced domain/intent, routing scope: full-auto /
  specific model / model group / pinned provider}. This is the single
  biggest gap relative to what you described. Must be resolved as a
  first-class routing target — i.e. any router's output (or a
  `PolicyRule.Provider`-equivalent) can itself be an alias name, requiring
  an alias-resolution step after routing, before `tryUpstream`, that can
  itself pick among a group.
- **Cost/latency-aware router type**, fed by a declared provider
  cost/latency table (inline config now, fetchable JSON/URL as an option),
  with a seam to later swap in measured data from captured traffic.
- **Persistent event store** (e.g. sqlite) capturing per-request: routing
  decision/rationale/signals, tokens, cost, latency, client identity,
  session/trajectory linkage, tool calls. This is the foundation the
  dashboard/cost-tracking/alerting features would later read from — build
  once, not per-feature.
- **API key authentication middleware** — nothing currently reads
  `Authorization` or any Caddy forward-auth header; `internal/http/handler.go`
  has no auth check at all. Needs: per-key config, validate against either
  a Caddy-trusted header or an Arbiter-checked key, attach identity to
  trace/log context for attribution.
- **Budget-cap guardrail** — spend-based, distinct from the existing
  request-count `rate_limit` guardrail.
- **Per-provider rate/budget limits that mirror upstream published limits**
  — config to declare e.g. OpenRouter-free's 10/min, 1000/day, and enforce
  it the same way the existing guardrail enforces its own global counters,
  but scoped by provider and feeding the dashboard's "quota used" view.
- **Client-injected-prompt stripper guardrail** — no detection mechanism
  for known clients' hidden system/tool prompts exists; needs a
  strip-vs-log-only mode, configurable per client-key/route.
- **Dashboard/UI** — nothing exists; explicitly deferred until the
  persistent store above lands, per your stated build order.

## Suggested build order (routing-first, per your priority)

1. Model alias layer + wiring it into the router selection (the "auto /
   auto-coding / pinned model / model group" mechanism).
2. Static provider cost/latency table + a cost/latency-aware router mode.
3. Persistent event store capturing routing+usage+session data (schema
   informed by #1–2 so routing decisions land in it from day one).
4. API key auth + client identity threaded into the store.
5. Budget-cap guardrail (consumes the same usage data as #3).
6. Client-injected-prompt stripper guardrail.
7. Dashboard/query tooling over the store from #3 (explicitly last).
