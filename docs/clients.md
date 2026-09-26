# Clients

Arbiter speaks the two wire formats its clients speak. Point anything that
supports the OpenAI Chat Completions API or the Anthropic Messages API at it,
name a model, and the request is routed and recorded.

## Endpoints

| Path | Purpose |
|---|---|
| `POST /v1/messages`, `POST /messages` | Anthropic Messages API |
| `POST /chat/completions`, `POST /v1/chat/completions` | OpenAI Chat Completions |
| `GET /models`, `GET /v1/models` | model list (OpenAI shape): provider models, aliases, and advertised capabilities |
| `GET /health` | liveness |
| `GET /` | 302 to `/admin/ui/` |

Both `/v1/…` and bare paths are registered — some clients set a base URL that
already ends in `/v1`, some do not.

An unmatched path returns `{"code":404,"detail":"Not Found"}` rather than Go's
default plain-text 404, so a client that parses every response as JSON does not
choke on the one response that is not.

Both chat endpoints accept `stream: true` and respond with SSE **in the same
wire format as the request** (formats are never mixed). Every response —
streaming or not — carries `X-Arbiter-Trace-Id`; OpenAI-style response IDs are
built from it (`chatcmpl-<trace-id>`), so a response can be correlated with
Arbiter's structured logs and its event store by ID alone.

### The `model` field

Whatever you send in `model` selects how the request is routed — it is an entry
point, not necessarily a destination. It may be:

- **a real model name** declared by a provider — routes straight there;
- **a pinned or group alias** — routes to whatever the alias selects;
- **a force alias** (commonly `auto`) — runs classification and the policy
  rules;
- **anything unmatched** — depends on your router config, typically a catch-all
  rule or the default provider.

The response's `model` field tells you what actually served the request.

## Pointing a client at it

Any OpenAI-compatible client needs only a base URL and a model name.

```jsonc
// opencode.json — a custom provider behind Arbiter
{
  "provider": {
    "arbiter": {
      "type": "openai",
      "baseURL": "http://127.0.0.1:8080/v1",
      "apiKey": "unused",
      "models": {
        "auto": { "name": "auto" }
      }
    }
  }
}
```

The `apiKey` is required by most clients and ignored by Arbiter — it implements
no authentication of its own (see [Admin surface and
access](../README.md#admin-surface-and-access)).

For an Anthropic-shaped client, point it at the same host and use the
`/v1/messages` path.

### Sub-agent and auxiliary traffic

Agentic clients make calls the user never sees: title generation, summarisation,
compaction, sub-agent turns. These arrive at Arbiter as ordinary requests and are
recorded as ordinary requests — a client's title call is still a request that
happened. Arbiter can identify a title-generation call from its system prompt
(see [Matching a request's own text](routing.md#matching-a-requests-own-text-match))
and route it somewhere cheap, and it
nests such a call under the conversation it named in the Sessions view.

Nothing is hidden from the store. If a call was made, there is a row.

## Attachments (images, PDFs, documents)

Clients that send attachments use the array form of `content`:

```json
{"role": "user", "content": [
  {"type": "text", "text": "what is in this image?"},
  {"type": "image_url", "image_url": {"url": "data:image/png;base64,..."}}
]}
```

Both `image_url` and `file` parts are accepted, and both a `data:` URL (inline
bytes) and an `http(s)` URL (which the upstream fetches itself). A document's
`filename` is required by OpenAI's wire shape and is carried through, so
`{"type":"file","file":{"filename":"report.pdf","file_data":"..."}}` survives
the round trip intact.

A message whose content is a bare string — every text-only client — still
serializes as a bare string on the way out, byte-identical to before attachments
existed. That is deliberate: emitting the array form unconditionally would change
the bytes of every request and invalidate cached prompt prefixes for traffic that
has nothing to do with attachments.

Internally all of these are one `attachment` content block (media type, payload,
filename, image flag); the translators own the per-format spelling. An attachment
arriving from an OpenAI client can therefore reach an Anthropic-speaking
upstream, where it goes out as an `image` or `document` block (by media type)
with the filename as the document's `title`.

**Attachment bytes are not stored.** The content store hashes an attachment over
its *identity* — media type, name, and a hash of the payload — so the same file
sent ten times deduplicates to one row and the row stays small, while discovery
still shows what was sent. The payload itself is never written.

**Anthropic client-facing parsing is not built** — see the README's "Not built
yet". An image or document block arriving on the Anthropic endpoint is dropped by
the translator.

### opencode refuses images, and it is not Arbiter's fault

opencode decides whether a model accepts images from its own bundled models.dev
catalogue, gated as
`capabilities.input.image = model.modalities?.input?.includes("image") ?? false`.
A **custom provider has no catalogue entry**, so anything behind a `baseURL`
override defaults to text-only and the refusal is generated client-side before
the request is ever built. No `/models` response can change this.

The workaround is config-side — declare the modalities on the model in
`opencode.json`:

```jsonc
"models": {
  "auto": {
    "name": "auto",
    // vision: true and a capabilities override are both IGNORED by opencode;
    // attachment: true alone leaves input.image false.
    "modalities": { "input": ["text", "image", "pdf"], "output": ["text"] },
    "attachment": true
  }
}
```

Note models.dev spells PDFs `pdf`, where Arbiter's normalized vocabulary uses
`file`.

## What a stream relays

Outbound bodies are **rebuilt** from the normalized types rather than forwarded
byte-for-byte, so anything the normalized types do not declare is stripped by
design. The streaming path therefore relays exactly these, and a field missing
from one of these lists is a bug rather than a passthrough:

- **Text deltas.**
- **Tool calls**, fragment-for-fragment as the upstream sends them: the call id
  and function name on the first fragment, a slice of the arguments JSON on each
  subsequent one. They are relayed rather than reassembled, because the OpenAI
  client contract is to concatenate arguments by `index` — buffering the whole
  call first would only add latency. An agentic client cannot terminate its loop
  if tool calls are dropped, and will retry instead.
- **Vendor reasoning** (`reasoning` / `reasoning_content`, as OpenRouter-family
  upstreams emit it) is not in the OpenAI spec, so it is relayed under the field
  the upstream used; a client that does not know it ignores it.
- **Token usage, cost and cache-read counters.** The outbound request always sets
  `stream_options.include_usage`, regardless of whether the client asked, because
  these are what the event store records — without the flag a stream reports no
  counts at all and every request is stored as zero tokens.

Two ordering invariants hold on the wire: the terminal `finish_reason` maps to a
stop event, and a start event is never emitted after a stop. Upstreams send a
trailing usage chunk that also carries `role: "assistant"`, so deriving a start
from role alone would emit a second `message_start` after `message_stop`.

## Prompt caching on the Anthropic path

Anthropic caching is opt-in per content block, so a request with no
`cache_control` marker is a full uncached read however long the conversation is
(an OpenAI-speaking upstream caches a prompt prefix server-side without being
asked, which is why the two directions behaved differently before this existed).
Arbiter marks two breakpoints on every outbound Anthropic body, and a breakpoint
caches the prefix **ending** at it:

- **the last tool definition**, when the request carries tools. Tool definitions
  are frozen for the life of a conversation, so this is the stable prefix to
  start a cache at.
- **the last content block of the newest message** — the rolling breakpoint. A
  conversation only grows at its end, so marking the newest turn makes each
  completed turn cacheable input for the next request, which is the whole saving
  on a long agent conversation.

The **system prompt** is marked by `AnthropicSystem`'s marshaller, which emits
the block form (`[{"type":"text","text":…,"cache_control":{"type":"ephemeral"}}]`)
rather than a bare string. A string has nowhere to hang the marker, which is what
kept the one part of a conversation that never changes uncacheable.

Two more properties, both deliberate:

- **A client's own inbound markers are replaced, not relayed.** The rolling
  breakpoint already caches a superset of whatever the client marked earlier in
  the same conversation, so relaying them buys no extra cache read while spending
  breakpoints against Anthropic's limit of four. A client marking three of its
  own plus Arbiter's two would be rejected, not cheaper.
- **At most two markers go out**, against a limit of four.

Caching being *visible* is a separate matter: the store records cache-read and
cache-write tokens, but neither outbound usage object carries a cache breakdown
back to the client today, and the inbound Anthropic **streaming** parser reads
only `input_tokens` and `output_tokens`. A client therefore cannot yet see that
caching is working. Tracked in the README's "Not built yet".

## Session affinity

Once a request is routed, its conversation is pinned to whichever
provider/model actually served it, so later turns reuse the same upstream and
its warm prompt cache instead of re-routing every turn. The pin is keyed by:

1. **The configured inbound header** (`session_affinity.header`, default
   `X-Session-Id`), when the client sends one.
2. **Otherwise a hash of the client's own text** — `system prompt + first
   text-bearing user turn`, each mixed into the key, gated on their combined
   length. Both halves are needed: the system prompt (an agent CLI's house
   prompt, thousands of characters) is what distinguishes two conversations that
   share a terse opener, and the opening user turn is what distinguishes two that
   share a house prompt.

**Sending the header is the supported path for a client that cares.** It is
cheaper, exact, and immune to the two failure modes below. Without it, the
derived key is what you get.

Two properties of the derived key are load-bearing:

- **It is computed before pre-guardrails**, from the request as the client sent
  it. Hashing after the `system_prompt` guardrail ran would mix Arbiter's own
  injected text into every key — and would change every key at once whenever that
  guardrail's prompt is edited, invalidating every live pin.
- **It excludes the first assistant reply on purpose.** The reply does not exist
  on the opening turn, so including it would make turn 1 hash differently from
  turn 2 onward and the turn-1 pin would never be reused. The opening user turn
  is stable for the whole conversation, which is what pinning requires.

`default_ttl` is deliberately sized to a working session (25h) rather than to a
cache window: a short idle timeout drops the pin whenever the user pauses to read
or run a build, and the next turn then re-routes to a cold provider at full
price. A per-provider `cache_ttl` overrides it for that provider.

A content-hash key is only as stable as the text it hashes, so a client that
*mutates* its opening turn — appending a fresh `<system-reminder>` block to it
each turn, as some desktop clients do — produces a different key every turn and
never pins. Arbiter serves clients that replay their history verbatim, so this
does not arise today; a denoise step before hashing is the fix if one ever does.
Note that changing the key derivation (denoising, a different anchor, a different
gate) invalidates every existing pin at once, which shows up as one burst of
re-routing across all live conversations.

**A conversation that opens with too little text is never pinned** — not on its
opening turn, and not later either: the key is derived from that *first* message
every time, so a chat that opens "hi" has the same too-short prefix on turn 40 as
on turn 1. Such a conversation routes fresh on every turn and records a NULL
`session_key` for its whole life. The gate exists because pinning two unrelated
chats together (both opening "hi") is worse than not pinning at all — but note
that is a multi-tenant instinct in a single-user tool, and the fix, if it is
wanted, belongs in the derivation (hash a stable prefix of the whole conversation
once it is long enough, rather than only the first user turn) not here.

The pin overrides classification for as long as the client keeps requesting the
**same `model` value**. A client that explicitly switches models means it, so the
pin is discarded and routing runs fresh. The pin is recorded from the route that
*actually served* the request, so it follows a fallback to another provider; a
pinned provider currently in 429 cooldown is treated as a miss (fresh routing
runs).

**Pins are persisted** (the `affinity_pins` table) when a store is configured, so
a conversation stays pinned across a **config reload** and a **restart**. Both
used to lose every pin: a reload rebuilds the whole pipeline, and the pins lived
inside it, so saving `arbiter.yaml` — even to change something unrelated —
silently re-routed every live conversation and cost it its warm prompt cache.
Persistence is also why a reload is no longer a way to clear pins deliberately;
that wants its own explicit affordance rather than happening as a side effect of
editing an unrelated setting.

The in-memory map is a cache in front of the table, not the source of truth: a
miss falls through to the store, and the common case (a hit on the very next
turn) never touches the database. Expiry is enforced on read as well as by the
hourly sweep, because a stale row can sit in the table for up to a sweep interval
and honouring it would pin a conversation past its idle timeout. With no store
configured, pins stay in memory only — the behaviour before persistence existed.

## What survives a config reload

A reload rebuilds the whole pipeline — providers, routers, classifiers,
guardrails — and swaps it in atomically. That is correct for everything derived
from config, but **runtime state must not live inside the thing being rebuilt**,
or editing an unrelated setting silently discards it. Three pieces of state are
therefore held outside the pipeline and injected into each new one:

| State | Where it lives | Survives |
|---|---|---|
| Session-affinity pins | `affinity_pins` table | reload **and** restart |
| Rate-limit counters | seeded from the store | reload **and** restart |
| Provider 429 cooldowns | in-process `CooldownStore` | reload only |

Each was a real bug before:

- **Pins** were lost on every config save, silently re-routing every live
  conversation and costing it its warm prompt cache.
- **Rate-limit counters** reset to zero, so a per-day cap of 1000 became 1000
  again after each save — meaningless in practice. They are now seeded from the
  request rows, so a cap reflects what actually happened rather than what this
  process happens to have seen.
- **Cooldowns** were cleared, so editing a config while a provider was backing off
  immediately re-opened the flood and produced another 429. Clearing is now a
  **deliberate action** (`POST /admin/cooldowns/clear`) rather than a side effect
  of saving a file: a reload is what you do *while fixing something*, and it
  should not undo the backoff you were relying on.

Cooldowns are deliberately **not** persisted. A cooldown is short-lived backoff
state measured in seconds, and a process that just started has no memory of the
429 that caused it — honouring a stale one across a restart would be inventing
knowledge Arbiter does not have.