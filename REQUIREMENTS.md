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

> **This is a dated snapshot, not a live status.** It was written at commit
> `5598a2b` to record the gap between these requirements and the code as it
> then stood. Each item below carries a status marker added 2026-09-15 and
> refreshed alongside the Phase 4a commit itself; the original text is kept
> so the reasoning survives. For current status see the project memory's
> changelog, which tracks each phase as it lands.
>
> Markers: **DONE** (gap closed, with the commit that closed it) ·
> **PARTIAL** (partly closed — the remainder is named) · **OPEN** (still
> accurate as written).

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
  (stdout-only isn't "queryable"). **DONE** (write path `90b570d`, read
  surface, Phase 4a) — every completed request is persisted with its routing
  decision, usage, cost and session linkage, and `/admin/stats/*` serves it;
  the JSON log lines remain the transport, not the store.

## Diverges — needs changing

- **Routing is 100% request-signal-driven, with no model-alias layer.**
  `router.SimpleRouter` / `router.PolicyRouter` both decide off classifier
  `Signals` or a static default — there's no concept of the client naming a
  virtual model via the `model` field and that name carrying routing
  policy. This is the core mechanism the requirements need and doesn't
  exist yet. **DONE** (`e0f1ea8`) — `internal/router/alias.go` adds force /
  pinned / group aliases, resolved recursively and usable wherever a
  provider/model target is expected, including as a policy rule target.
- **No provider-side data in routing decisions.** `Signals.CostSensitivity`
  (`pkg/types/models.go:23`) is a string label a classifier assigns from
  request text (e.g. "budget") — matched literally in `PolicyCondition`,
  not compared against real provider cost/latency numbers. There is no
  provider cost/latency table anywhere in config or types. "Pick the
  cheapest/fastest provider" cannot be expressed today. **DONE**
  (`1fbf74b`, catalog loading `a00d6d8`, generator `acbdb91`) —
  `internal/router/cost.go`'s `CostLatencyLookup` + `StaticCatalog`; group
  aliases select by `cheapest_input`/`cheapest_output`/`fastest` off the
  `model_catalog`. The empirical seam is the `CostLatencyLookup` interface
  itself (only `StaticCatalog` implements it so far). Note: a *fetchable
  URL* catalog still does not exist — only inline + a local file.
- **`rate_limit` guardrail is a single global cap** (`internal/guardrail/guardrail.go:84`
  `RateLimitGuardrail`) — one counter pair (per-minute/per-day) for the
  whole pipeline, applied pre-routing, before the provider is even known.
  There's no per-provider limit and no concept of mirroring an upstream's
  published limits. Needs to become provider-scoped (checked/incremented
  post-routing, or with per-provider counters keyed by chosen provider).
  **OPEN** — still one `minuteCount`/`dayCount` pair, still `ApplyPre`.
- **`Usage.CostUSD`** (`pkg/types/response.go:39`) is populated only when
  an upstream reports it directly (e.g. OpenRouter) — no static cost-table
  fallback, so most providers (plain Anthropic/OpenAI) show $0 always.
  **DONE for the store** (`90b570d`) — catalog-derived cost now fills the
  stored `cost_usd` when the upstream reports none; the response body still
  carries only upstream-reported cost.
- **Observability is stdout-only.** `LogRouting`/`LogUpstream`/etc. write
  JSON lines and nothing else — no persistence, no session/trajectory
  concept, no tool-call attribution, no client-identity tagging. Fine as a
  transport, not sufficient as the queryable store the requirements call
  for. **DONE** (schema `a9b3b1b`, write path `90b570d`, read surface,
  Phase 4a) — each completed request is a persisted row (routing rationale,
  tokens, cost, latency, session key, tool calls, config epoch), and the
  `/admin/stats/*` endpoints answer the §2 target questions directly.

## Missing entirely — needs developing

- **Model alias / virtual-model config layer.** New config section mapping
  alias name → {optional forced domain/intent, routing scope: full-auto /
  specific model / model group / pinned provider}. This is the single
  biggest gap relative to what you described. Must be resolved as a
  first-class routing target — i.e. any router's output (or a
  `PolicyRule.Provider`-equivalent) can itself be an alias name, requiring
  an alias-resolution step after routing, before `tryUpstream`, that can
  itself pick among a group. **DONE** (`e0f1ea8`) — see the divergence entry
  above; aliases resolve recursively (depth-limited, cycles rejected at
  load) and a group's unselected members *are* its fallback chain.
- **Cost/latency-aware router type**, fed by a declared provider
  cost/latency table (inline config now, fetchable JSON/URL as an option),
  with a seam to later swap in measured data from captured traffic. **DONE
  except the URL option** (`1fbf74b`/`a00d6d8`/`acbdb91`) — table is inline
  `model_catalog` plus a local `model_catalog_file`; the seam is the
  `CostLatencyLookup` interface. Fetching a catalog over HTTP is not built.
- **Persistent event store** (e.g. sqlite) capturing per-request: routing
  decision/rationale/signals, tokens, cost, latency, client identity,
  session/trajectory linkage, tool calls. This is the foundation the
  dashboard/cost-tracking/alerting features would later read from — build
  once, not per-feature. **DONE** (schema `a9b3b1b`, write path `90b570d`,
  read surface, Phase 4a) — the schema carries all of the above plus the
  `config_epoch` join key, the writer records every completed request with
  catalog-derived cost, and `/admin/stats/*` serves the aggregates. Sub-agent
  / child-request attribution is deliberately *not* in the schema.
- **API key authentication middleware** — nothing currently reads
  `Authorization` or any Caddy forward-auth header; `internal/http/handler.go`
  has no auth check at all. Needs: per-key config, validate against either
  a Caddy-trusted header or an Arbiter-checked key, attach identity to
  trace/log context for attribution. **OPEN, and reframed (2026-09-15):
  these are attribution + per-client shaping, explicitly NOT authentication
  and not a gate** — a missing or unknown key must not be rejected, it
  degrades to *unattributed*. See §4. (The admin surface's
  `forward_auth_header` is a separate thing and *is* a presence-checked
  gate; do not model client keys on it.)
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

Status as of 2026-09-15: **1, 2 and 3 are done; 4 is reframed (attribution,
not auth); 7 is partial (query surface exists, UI does not).** Nothing beyond
step 3 + the 4a query surface is currently greenlit.

> **REORDERED 2026-09-15 (user): #7 — the dashboard/query tooling — is now the
> active work item, ahead of #4/#5/#6.** Rationale: the store now has real
> traffic flowing through it and per-config-epoch cost data, so an interface
> over it is what makes the captured data answer the §2 questions in practice
> rather than in principle. Its write-side prerequisite (schema + write path +
> read aggregates) is exactly the part already done, so this is a UI + read-API
> task, not a data task. The numbering below is left as-is so the commit
> references in this file stay meaningful; the note overrides the order.
>
> §2's "dashboard, cost aggregation, and alerting are downstream consumers of
> the same data, not separate logging paths" is unchanged and still satisfied —
> "downstream" meant *dependency* order, not scheduling priority.
>
> Expected split (proposal, not decided): **7a** read API completion — a
> request **list** endpoint (filters: since, provider, status, session, alias)
> and single-request detail, curl-usable on its own, which is a prerequisite
> the UI cannot be built without; **7b** the page itself; optionally **7c** an
> admin gate value check (`forward_auth_header` is presence-only today) if the
> page is to be gated by a specific header value rather than by Caddy's
> cookie session.
>
> **7a BUILT, COMMITTED as `c7c1f48`** (2026-09-15): `GET /admin/requests`
> (filters `since`, `provider`, `session`, `alias`, `status`, `errors`,
> `limit`; newest first, `ts DESC, id DESC`) and `GET /admin/requests/{id}`
> (the full row: confidence, cache token counts, tool calls). Both GET-only
> under the existing `/admin/*` gate; query and response shapes are documented
> in `README.md`'s admin section; live-verified end to end against fakellm.
>
> **Content capture BUILT, COMMITTED as `7b9e971`** (2026-09-15) — the other
> prerequisite 7b was waiting on, done as its own change before any UI work:
> prompt/response bodies are now stored content-addressed at message-block
> granularity (`storage.capture_content`, off by default;
> `storage.content_ttl`, empty = never expire). This is what makes the detail
> view able to show a conversation at all, and it also *answers* §3's
> client-injected-prompt-stripper requirement's hardest question — how you'd
> detect injected text — via `GET /admin/content/repeated` (blocks recurring
> across requests, with distinct-session counts). See `README.md`'s "Content
> store" section. Two facts that still constrain 7b: there is deliberately
> **no way to filter for requests with no session key** (the zero value means
> "any"), and tool-call arguments from streams are stored hash-only rather
> than reassembled.
>
> **7b planned 2026-09-16** (plan at `~/.claude/plans/1-lets-go-with-snoopy-dawn.md`,
> reviewed against the code before any of it was built — three of its claims about
> the store's `ts` column were wrong and are corrected in place). Split into five
> separately shippable phases: 7b-1 shell + request list/detail, 7b-2 sessions +
> transcript, 7b-3a pivot table, 7b-3b chart, 7b-4 discovery, 7b-5 live tail.
>
> **7b-1 BUILT, COMMITTED** — `internal/ui` (embedded templates + assets, htmx
> for in-page swaps only), the request list with the 7a filters as a real form,
> keyset paging, and the request detail page with captured content loaded lazily.
> Two things landed with it because the page needed them: `?no_session` on
> `/admin/requests` (the filter `reader.go`'s own doc comment said was missing),
> and the keyset cursor (`RequestFilter.BeforeTs`/`BeforeID`, rendered as one
> opaque `?after=` token). Live-verified against fakellm in a scratch config.
>
> Along the way the UI exposed a **pre-existing bug worth noting**: a request
> whose upstream could not be reached was stored with `status_code = 0`, so
> `/admin/stats`' error counts and `?errors` — both `status_code >= 400` — were
> blind to the most common failure mode, and such rows read as "not finished"
> rather than "failed". Now recorded as 502, matching the status the client was
> already given. Not a migration; older rows keep their 0.
>
> **7b-2 BUILT, COMMITTED** — `/admin/ui/sessions` (one row per conversation:
> turns, span, providers, cost, errors) and `/admin/ui/session?key=…`, the
> transcript. Requests with no session key get their own count on the index
> rather than being grouped into a fake session or dropped.
>
> The transcript renders **the conversation's last state, not its replay**: a
> client re-sends its whole history every turn, so each turn shows only what it
> *added*, its own stats with a link to open exactly that request, and its
> system preamble as a separate collapsible field. What it re-sent is one line
> naming the turn that first showed it. The split is by content hash, so a
> "replay" is provably the same bytes — and the replayed bodies are not rendered
> at all, since a collapsed `<details>` still ships its contents and would leave
> the page exactly as large. A three-turn conversation went from shipping its
> 11.7k-character system prompt three times to once.
>
> Session keys are shown truncated on both screens, full value in the tooltip and
> in every link.
>
> **7b-3a BUILT, COMMITTED** — `/admin/ui/overview`, an adjustable pivot:
> window × group-by dimension × rank-by metric, driving one ranked table with
> headline numbers above it. Every row carries every metric (requests, cost, cost
> per request, tokens, avg latency, errors, error rate); the metric picks the
> ordering. Dimension and metric are map keys, never user text in SQL, and an
> unknown axis is a 400 that lists the valid ones.
>
> A dimension whose window has only one value is *explained* rather than
> presented as a finding — on this deployment domain/effort/alias are all empty
> because the traffic names a concrete model, which routes before classification
> runs.
>
> **Next: 7b-3b (chart + uPlot)** — deliberately conditional. The test is whether
> the table proves insufficient in use; if it does not, 7b-3b should be dropped
> rather than built for completeness. Then **7b-4** (discovery) and **7b-5**
> (live tail). Subagent and title-generation grouping in the transcript are
> blocked on the parent/child linkage gap above — Arbiter does not capture that
> relationship at all.

1. ~~Model alias layer + wiring it into the router selection (the "auto /
   auto-coding / pinned model / model group" mechanism).~~ **DONE** —
   `e0f1ea8`.
2. ~~Static provider cost/latency table + a cost/latency-aware router mode.~~ —
   **DONE** — `1fbf74b` + `a00d6d8` + `acbdb91` (URL-fetched catalog not
   built; considered optional).
3. Persistent event store capturing routing+usage+session data (schema
   informed by #1–2 so routing decisions land in it from day one). — **3a
   DONE** (`a9b3b1b`, schema + generated queries); **3b DONE** (`90b570d`,
   write path incl. catalog-derived cost); **read surface DONE** (Phase 4a,
   config-epoch join key + `/admin/stats/*`).
4. API key auth + client identity threaded into the store. — not started;
   reframed as attribution + per-client shaping, not auth (see §4).
5. Budget-cap guardrail (consumes the same usage data as #3). — not started.
   **Next candidate, and the largest remaining requirement-shaped gap**: it
   and item 6 are the only §3 "Add" items untouched.
6. Client-injected-prompt stripper guardrail. — not started.
7. Dashboard/query tooling over the store from #3 (explicitly last). —
   **PARTIAL** (Phase 4a) — the HTTP query surface exists (`/admin/stats/*`:
   overall, provider/model, per-epoch, session trajectory, tools); a UI or
   dashboard over it does not.
   **→ NOW FIRST (reordered 2026-09-15, see the note above).** The remaining
   work is a request-list/detail read API plus the page; the aggregate
   surface exists but a UI needs per-request rows in a way curl-shaped
   aggregates don't provide.

### Known gaps, recorded so they aren't mistaken for done

- **Sub-agent / child-request attribution** (§2, "where derivable") is not in
  the schema. It needs a parent-request/trace linkage column whose shape isn't
  known until a multi-agent client is instrumented. Deliberately deferred, not
  silently dropped.
- **A fetchable-URL catalog** (§1's "config-inline or a fetched JSON/URL") is
  not built: the catalog is inline plus a local `model_catalog_file`, produced
  by a standalone converter. `CostLatencyLookup` is the seam a fetcher would
  implement, and that seam is already exercised by two implementations'
  worth of call sites.
- **Empirical (measured) cost/latency** (§1's "later become
  measured/empirical") is not built. Provider-*reported* cost is captured
  (OpenRouter; plain Anthropic/OpenAI report nothing) and latency is recorded
  per request, so the data to back an empirical `CostLatencyLookup` is now
  being collected — the interface is the seam, and swapping it needs no
  caller changes.
- **Cache-read/cache-write token pricing** is not modelled in the stored
  cost, so a stored `cost_usd` is not exact for a prompt-cached call.
