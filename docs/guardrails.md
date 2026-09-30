# Guardrails

Guardrails are pre/post hooks that run on every request: pre-hooks on the
`NormalizedRequest` before routing and the upstream call, post-hooks on the
`NormalizedResponse` after it. Everything from prompt injection to rate limiting
is a guardrail — there is no special-cased path for any of it, and a new
behaviour is a new type rather than a branch in the pipeline.

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

A typo'd `type` is caught at build time by a test that reads this table out of
this file and asks the real `buildGuardrail` to construct each one, so the table
and the implementation cannot drift apart. That test exists because the first
version of this table documented `prompt_replace`, a name that was never
implemented, and every test passed.

## System prompt injection

`system_prompt` is the oldest guardrail and the one that runs first in the common
case. With `override: false` — the default, and the useful one — the configured
prompt is **prepended** ahead of whatever the client sent, so Arbiter's baseline
instructions always apply while the client's own are still there. With
`override: true` the client's system prompt is discarded entirely, which is
occasionally what you want for a fixed-purpose deployment and is otherwise a good
way to break a coding agent.

It has one load-bearing interaction: capture runs **before** pre-guardrails, so
what the store holds is the request as the client sent it, and the session key is
hashed before this guardrail runs. Otherwise Arbiter's own injected text would be
stored as though the client had sent it, and every live pin would be invalidated
the moment this prompt was edited. Both are covered under Content store and
Session affinity.

## Rate limiting

`rate_limit` caps how many requests Arbiter makes, per minute and per day. `0`
means unlimited, and a `0` window is not even queried.

The counters are **seeded from the event store** when one is configured, which is
what makes a cap mean anything: a purely in-memory counter is thrown away every
time the pipeline is rebuilt, so a per-day cap of 1000 became 1000 again after
each config save. Seeding also carries a cap across a restart, which is closer to
what the number is for than the original behaviour was. With no store configured
the caps count only what this process has seen — the behaviour before seeding
existed, and the correct degradation.

Counting deliberately includes Arbiter's **own internal requests** (classifier
and title-generation calls). Those are real upstream requests, and a cap that
mirrors an upstream's own published limit has to see them; the spend aggregates
filter them out for the opposite reason.

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
        mode: "prefix"          # exact | prefix | line_exact | line_prefix | paragraph_exact | paragraph_prefix | regex
        action: "strip"         # strip | replace | block | strip_paragraph
        where: ["system"]       # system | messages | all
```

**Matching is on the text, not on a client identity.** A per-client key would let
you write "this client injects X"; matching X directly is simpler and survives a
client renaming itself. Per-client attribution is a separate concern and isn't
needed here.

### `mode`

| `mode` | Matches when |
|---|---|
| `exact` | the searched text **is** the pattern, anchored to the WHOLE field (ignoring case and surrounding whitespace) |
| `prefix` | the searched text **starts with** the pattern, anchored to byte 0 of the WHOLE field (default) |
| `line_exact` | some one **line** of the text, trimmed, equals the pattern |
| `line_prefix` | some one **line** of the text starts with the pattern (after trimming its leading whitespace) |
| `paragraph_exact` | some one **paragraph** (a blank-line-delimited chunk) equals the pattern |
| `paragraph_prefix` | some one **paragraph** starts with the pattern |
| `regex` | a Go regular expression matches anywhere |

**`exact` and `prefix` anchor to the whole field, not to a line or paragraph
within it.** The moment your system prompt is a concatenation of several
sources — Arbiter's own framing followed by a client's injected preamble, say,
or several stacked blocks — the target text is no longer at offset 0 / the
entire field, so `exact`/`prefix` silently never match. Use `line_prefix` /
`paragraph_prefix` (or their `_exact` counterparts) when the target text is a
whole line or paragraph but not the first thing in the field; use `regex`
(unanchored) for anything else.

### `action`

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

### `where`

`where` selects what's searched: `system` (default), `messages`, or `all`. Only
**text** blocks are rewritten — a `tool_use` or `attachment` block isn't free
text, and rewriting it would corrupt the request.

### Matching details worth knowing

`exact` and `prefix` are case-insensitive and ignore leading/trailing whitespace,
because different clients and wire formats re-serialize the same preamble
differently and a case difference isn't a different prompt. `regex` is used
verbatim — write `(?i)` yourself if you want case-insensitivity. **Watch the
character class**: `\w` excludes `-`, so `you are \w+` does not match
`claude-code`; use `[\w-]+`.

A typo'd `mode`, `action`, or `where` is a **config-load error**, not a silent
no-op — a guardrail that quietly rewrites nothing would leave you believing
injected text is stripped while every request still carries it.

### Finding what to strip

`/admin/content/repeated` (and the Discovery page) lists blocks appearing across
many requests and sessions — the client's own preamble shows up there, with a
preview, before you write a rule for it. See
[observability.md](observability.md#discovery-the-blocks-that-recur).