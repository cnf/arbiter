# Routing

Deciding where a request goes: the precedence a `model` field is resolved
through, how a `group` alias picks a member, capability-aware rules, and the
three ways a request gets classified (heuristic keywords, an LLM call, a
decision model) when nothing else already settled it.

Session affinity — how a conversation is pinned to a provider once a route is
picked — is documented in
**[docs/clients.md](clients.md#session-affinity)**, not here: it's a
client-facing property (what a client can rely on across turns), while this
document is about how a route is chosen in the first place.

## Precedence: how `model` selects a route

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
   high-confidence domain match can't starve difficulty), a **force alias** named
   by the client overrides only the axes it declares (`coding` sets domain but
   leaves difficulty to classify), then the first matching policy rule wins. A
   policy router errors when nothing matches, so chain a `simple` router after
   it (or write a catch-all rule) to degrade instead of failing.

A complete, loadable config showing all three alias shapes (`force`,
`pinned`, `group`) side by side is in
**[docs/examples/router-alias-shapes.yaml](examples/router-alias-shapes.yaml)**.

Any alias — pinned, group or force — may also declare `request_kind:` (see
[Aliases](#aliases) below), which stamps what a request naming it IS onto its
stored row at every path the alias decides the route, including the
session-affinity pin that serves turn 2+ of the same conversation.

A rule's `when` clause can also match `request_kind` — a request's **kind**
(`"title"`, later `"subagent"`) rather than what it's about. It is not a
classification axis (no confidence, no force-alias target — see
`types.Signals.RequestKind`), but it is still a legitimate thing to route on:
a title-generation call is identified by the `request-kind` classifier
(see "Matching a request's own text" below) and a rule like
`when: { request_kind: "title" }` sends it to a cheap/fast alias instead of
whatever model the client happened to name.

A rule's `when` clause can also match `effort` — the reasoning-effort knob the
**client itself sent**, matched verbatim against `types.Signals.ClientEffort`.
The wire spelling depends on the format the client spoke: Anthropic clients
send it as `output_config.effort`, OpenAI clients as `reasoning_effort`. Both
are read into the same signal, so a rule matches regardless of which format
the request arrived in. Like `request_kind` it is a request fact rather than
a classification axis (no confidence, no classifier fills it, no
force-alias targets it), and it is stamped on every path before routing, so a
rule like `when: { effort: "high" }` can send a request that asked for high
effort to the strong model *without* having to override what the client asked
for. Note the value is matched by equality against whatever vocabulary the
client uses (Anthropic's `low`/`medium`/`high`, another client's longer scale)
— there is no cross-vocabulary ordering, only an exact string match.

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

A complete, loadable policy-router config — the full `when` vocabulary
(`domain`, `difficulty`, `tags`, `capabilities`, `requires_input_modalities`,
`request_kind`, `effort`), an alias target, a literal provider/model target, a
`stop` refusal, and a `simple` router chained as the default — is in
**[docs/examples/router-policy-rules.yaml](examples/router-policy-rules.yaml)**.

Aliases are client-facing and appear in `/models` alongside provider models
(listed with provider `"alias"`). Any rule `target` may name an alias, and a
group member may itself be another alias; resolution is depth-limited and a
cycle is rejected at config load.

### Declaring what an alias's traffic IS (`request_kind`)

An alias can say what a request naming it IS — and every request routed
through it is then labelled on its row. The canonical shape is a **force**
alias: the alias declares identity, and the *rules* decide where the traffic
goes. The alias does not route.

```yaml
aliases:
  subagent:
    force: {}                    # force nothing; axes classify as usual
    request_kind: "subagent"     # what a request naming this alias IS
```

A complete, loadable config (alias + a policy rule routing on the declared
kind) is in
**[docs/examples/router-alias-request-kind.yaml](examples/router-alias-request-kind.yaml)**.

A request naming `subagent` falls through to classify + rules exactly like a
force alias always does — its axes classify normally, and a rule like
`when: { request_kind: "subagent" }` sends it wherever subagent traffic
belongs. Keeping the destination in the rules is the point: identity is the
alias's job, routing is the router's.

`request_kind` is declared on a force alias, and it is stamped at every path
that force-alias traffic travels:

- the force-alias path (turn 1), where the alias-declared kind **overrides**
  whatever a kind-only matcher classified: the operator pointed this alias at
  this traffic, and the two sources must not disagree on the row;
- the session-affinity pin, which serves **turn 2 onward** of the same
  conversation — the pin is recorded under the client's model string, which
  for alias traffic IS the alias name, so the stamp survives every turn even
  though classification never runs on the pin path.

The three alias shapes are **exclusive**: a pinned/group alias is a
*destination* — it says where the request goes — and a `request_kind` is
*metadata*, so declaring both is rejected at config load (see
`internal/config/aliases.go`). If an alias must both be a destination and
label its traffic, that is two aliases: a pinned one for the routing, a force
one declaring the kind for the label. The exclusivity is why #44 needed the
force-alias mechanism at all: a kind-only matcher cannot label traffic that
short-circuits before classification runs, so identity has to ride the alias
that declares it.

## Group selection strategies

A `group` alias's `select:` decides which member becomes the primary:

| `select` | Picks |
| --- | --- |
| `random` (default) | a random member |
| `cheapest_input` | lowest `input_cost_per_mtok` |
| `cheapest_output` | lowest `output_cost_per_mtok` |
| `fastest` | lowest `latency_ms_p50` |

A complete, loadable config (three members, `model_catalog` with cost and
capability fields) is in
**[docs/examples/router-group-select.yaml](examples/router-group-select.yaml)**.

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

### Capabilities

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

### Namespacing the emitted model name

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

### Coverage reporting

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

## Routing on what a model can do

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

## Routing on freeform tags

`tags` is the one axis Arbiter assigns **no meaning** to. Classifiers declare
whatever strings an operator wants (`python`, `french`, `user_is_angry`) and a
rule matches them by set membership:

```yaml
- when: { domain: "code_generation", tags: ["python"] }
  target: "py-strong"
```

Like `capabilities` (and unlike `domain`/`difficulty`/`cost_class`), tags are
**additive**: several classifiers may each contribute tags and the results
union; there is no confidence contest and no `unmatched` sentinel. A tag rule
requires **every** listed tag to be present (a subset requirement), and an
unset `tags` on a rule is a wildcard.

Tags are matched against their own signal set and never conflated with
`capabilities` — the two may even share a spelling without one satisfying the
other. The distinction is the contract: `capabilities` means "the model must be
able to do X" (strict, and consumable by `requires_input_modalities`), while a
tag asserts nothing and only selects which rules a request hits.

A tag named in a `when: {tags: [...]}` rule must be producible by SOME
classifier or force-alias — a heuristic's keyword group name, an `llm`/
`choice`-question label, a `noul` question's `value`, or a `force: {tags:
[...]}` entry. A tag nothing can ever emit is rejected at config load: it is
almost always a typo, and the alternative (the rule silently never matching on
that condition) is a far worse place to discover it.

Modalities come from the cost/latency catalog (`input_modalities` on a
`model_catalog` row). A model with **no catalog row, or a row that states
nothing about modalities, does not satisfy the requirement** — unknown is not
permission, and routing image traffic to a model whose support is simply
unstated is the guess this exists to prevent. The same applies when no catalog
is configured at all: an unverifiable guard never passes.

If every rule is skipped the router reports its usual no-match error, so a
router chained after it (a `simple` router, say) still takes over.

## LLM-backed classification

A `type: "llm"` classifier asks an upstream model to classify a request
instead of guessing from keywords — real semantic understanding, at the cost
of a real upstream call.

A complete, loadable config showing a heuristic-first, LLM-fallback pair
(`only_if_unset`, rubric `labels`, `escape`, `fallback`) is in
**[docs/examples/classifier-llm.yaml](examples/classifier-llm.yaml)**.

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
the `difficulty` axis was filled elsewhere. A decisions classifier, whose one call
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

## Decision-model classification (`type: "decisions"`)

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

Three primitives are built: `choice` (pick one of several labels, shown above
for `domain` and `cost_class`), `noul` (a bare yes/no), and `score` (a
fractional position on an ordered rubric). A `noul` question declares `value`
instead of `labels`: answering "yes" (probability > 0.5) applies that value to
the axis; answering "no" applies nothing and leaves the axis exactly as it
was, open for a later classifier or the fallback to fill — this is
deliberately NOT the same as a `choice`'s escape, which fills the axis with
the reserved `unmatched` sentinel. A `noul` answer carries no confidence of
its own (a confident "no" and a confident "yes" are equally confident), so the
confidence recorded is `max(p, 1-p)`. On an additive axis (`capabilities`,
`tags`) `noul`'s "yes" adds `value` as one set member and "no" adds nothing —
there is no single-value special case there.

A `score` question declares `levels` instead of `labels`: an ORDERED list,
low -> high (order is the data — a map would lose it to randomized iteration,
so only a list is accepted). The answer is a fractional position along that
order (e.g. `1.6` sits between level 1 and level 2); the classifier snaps it
to the nearest level and fills the axis with that level's name. Confidence is
read directly from the answer, unlike `noul`. Prefer `score` over `choice`
when the axis is genuinely a scale rather than a set of categories — effort or
cost class, for instance, is arguably better asked as a score than a choice.

A complete, loadable config showing all three question types (`choice`,
`score`, `noul`), the `decisions` provider type, and an `only_if_unset`
guard is in
**[docs/examples/classifier-decisions.yaml](examples/classifier-decisions.yaml)**.

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

## What a classifier reads, and how much of it

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

## Matching a request's own text (`match`)

A classifier's input is one user message. Some requests are identified by text
that lives somewhere else: a title generator's system prompt carries its
signature, and for such a request the user message is the conversation being
titled, which looks like ordinary chat. The identifying text was not merely
unmatched, it was invisible by construction.

`match` searches the request's own text instead, on **any** classifier type. This
is the shipped `request-kind` classifier, and the three patterns are the **real**
prompts, read out of the live store rather than invented. A complete, loadable
config is in
**[docs/examples/classifier-request-kind-match.yaml](examples/classifier-request-kind-match.yaml)**.

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
`arbiter.example.yaml` through the real parser, compares the patterns to the intended
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

## Structural capability detection (`detect`)

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
