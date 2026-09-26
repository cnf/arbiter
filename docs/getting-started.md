# Getting started

This walks you from nothing to a request Arbiter routed, recorded, and can show
you. Budget about ten minutes.

You need:

- **devenv** — Arbiter builds inside it. The host does not need a Go toolchain or
  a C compiler.
- **A provider to point at** — a local runtime (Ollama, a self-hosted LiteLLM, any
  OpenAI-compatible endpoint) or a cloud API key. If you just want to see the
  thing work before wiring a real provider, skip to [the fake upstream
  step](#step-4-with-no-real-provider).

---

## 1. Build it

```bash
git clone https://github.com/cnf/arbiter && cd arbiter
devenv shell --no-tui -- bash -c 'go build -o build/arbiter ./cmd/arbiter'
```

The binary lands at `build/arbiter`. Everything — build, test, run — happens
inside `devenv shell`; that is where the toolchain lives.

> **Why `--no-tui`?** Plain `devenv shell -- cmd` renders a progress UI that
> buries real error output. `--no-tui` gives you the actual text. It does not
> change what runs, only what you can read.

---

## 2. Write a config

Arbiter needs `arbiter.yaml`. The minimum that starts is one provider and one
router.

```yaml
providers:
  local:
    type: "openai"                     # openai | anthropic | ollama
    endpoint: "http://localhost:11434/v1"
    models: ["llama3.2"]

aliases:
  cheap-local:                         # a target must be a pinned or group alias
    type: "pinned"
    provider: "local"
    model: "llama3.2"

routers:
  - name: "policy"
    type: "policy"
    config:
      rules:
        - when: {}                     # catch-all
          target: "cheap-local"

storage:
  path: "arbiter.db"                   # omit this whole block for no persistence
```

Two things worth knowing before you go further:

- **A provider with no `key:` is fine.** A local runtime usually needs none. A
  cloud provider reads one from the environment via `${MY_KEY}` syntax.
- **Unknown fields are a hard error, not a warning.** A typo in a key name stops
  startup with the offending line and field named, rather than silently
  configuring nothing. This is deliberate — a silently ignored option is worse
  than a failure to launch.

Send this config `model: "cheap-local"` and it routes to `local/llama3.2`. What
you send in the `model` field picks the *entry point*, not necessarily the
destination.

The complete surface is [Configuration](../README.md#configuration) in the
README. The config in the repository root (`arbiter.yaml`) is a fuller worked
example with every section filled in and annotated.

---

## 3. Run it

```bash
devenv shell --no-tui -- bash -c './build/arbiter --config arbiter.yaml'
```

```
INFO Arbiter starting config=arbiter.yaml port=8080
INFO event store enabled path=arbiter.db
INFO listening addr=127.0.0.1:8080 network=tcp
```

Check it is alive:

```bash
devenv shell --no-tui -- bash -c 'http --ignore-stdin -S GET :8080/health'
# {"status":"ok"}
```

Two flags matter here: `--port` (default `8080`) and `--bind` (default
`127.0.0.1`). The loopback default is not an accident — see [Admin surface and
access](observability.md#admin-surface-and-access) for why, and do not change `--bind`
without putting something in front of it.

`--socket <path>` serves on a unix socket instead, and overrides both.

---

## 4. Send a request

Any OpenAI- or Anthropic-shaped client works. The simplest is a hand-made one:

```bash
devenv shell --no-tui -- bash -c \
  'http --ignore-stdin -S POST :8080/chat/completions \
     model="cheap-local" \
     messages[0][role]=user \
     messages[0][content]="write a function to sort a list"'
```

```json
{
  "id": "chatcmpl-8c325b7d-eb0e-439f-8a57-1c9ef98319ca",
  "model": "llama3.2",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "…"}}],
  "usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}
}
```

Note the response's `model`: you asked for `cheap-local`, and `llama3.2` answered,
because `cheap-local` is an alias that resolves to that provider/model. The
response's `id` is built from Arbiter's trace id, so a response can be correlated
with the store by ID alone.

### What just happened

Arbiter ran the request through, in order: normalization, pre-guardrails,
classification, routing, the upstream call, and a store write. You don't have to
take that on faith — look at the row.

---

## 5. See what it recorded

The point of the whole thing. Every request leaves a row, including the ones
that failed and the ones Arbiter made for itself.

**The UI:**

```
http://127.0.0.1:8080/admin/ui/overview
```

Three pages, each a view onto the same store:

| page | shows |
|---|---|
| **Overview** | the routing flow — what came in, what it became, where it went; cost and cache KPIs |
| **Sessions** | one lane per conversation, with the turns in order |
| **Discovery** | blocks of content that recur across requests and sessions |

**The JSON surface**, if you would rather script it:

```bash
devenv shell --no-tui -- bash -c 'http --ignore-stdin -S GET ":8080/admin/requests?limit=1"'
```

```json
[{
  "id": 1,
  "trace_id": "011b1286-0bcd-4976-9bed-52b31defc9d4",
  "session_key": "9f2c…c68e",
  "format": "openai",
  "provider": "fake",
  "model": "fake-model",
  "alias_used": "auto",
  "routing_rationale": "policy router \"policy\": domain=\"code_generation\" effort=\"\" capabilities=[] cost_class=\"\" -> alias \"fake-alias\" -> fake/fake-model",
  "domain": "code_generation",
  "input_tokens": 11,
  "output_tokens": 7,
  "latency_ms": 6,
  "status_code": 200,
  "stream": false,
  "config_epoch": "d369219bb82683b7",
  "kind": "client"
}]
```

Read `routing_rationale` first. It is the answer to "why did this go there",
written by the router that made the call — the axis values it matched on, the
alias chain it walked, and the provider/model it landed on.

`?errors` (presence-only) narrows the list to status ≥ 400. A failed request is a
request: a routing failure or a guardrail refusal gets a real row with a status
code, not silence.

---

## Step 4, with no real provider

To exercise all of the above without wiring a real backend, point Arbiter at a
throwaway upstream that answers deterministically:

```python
# build/fakeupstream.py — a minimal OpenAI-shaped upstream
import json
from http.server import BaseHTTPRequestHandler, HTTPServer

class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(n) or b"{}")
        out = json.dumps({
            "id": "chatcmpl-fake", "object": "chat.completion",
            "model": body.get("model", "unknown"),
            "choices": [{"index": 0, "finish_reason": "stop",
                         "message": {"role": "assistant", "content": "hello"}}],
            "usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
        }).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def log_message(self, *a): pass

HTTPServer(("127.0.0.1", 5699), Handler).serve_forever()
```

Point a provider at it and send the same request as Step 4:

```yaml
providers:
  fake:
    type: "openai"
    endpoint: "http://127.0.0.1:5699/v1"
    models: ["fake-model"]
```

---

## When it does not start

**`failed to load config error="CONFIG_ERROR: …"`** — the message names the field,
the line, or the collision. Config validation is strict and its errors are
specific; read the message rather than the code.

Two collisions that are easy to hit and are not obvious from the schema:

- **An alias may not share a name with a provider.** `aliases: {fake: …}` next to
  `providers: {fake: …}` is rejected — `alias "fake": name collides with a
  configured provider` — even though the two sections look independent. Name the
  alias something else.
- **A name may not be declared twice** — caught by the YAML parser, with both
  line numbers: `line 6: mapping key "p" already defined at line 2`.

**`error="CONFIG_ERROR: provider "broken": missing endpoint"`** — a provider needs
one. Declaring `models:` alone is not enough.

**`unknown field`** — the reported field name is the typo:

```
CONFIG_ERROR: parsing arbiter.yaml (yaml: unmarshal errors:
  line 6: field moedls not found in type config.ProviderConfig)
```

**`ROUTING_ERROR: … alias "auto" is a force-alias and selects no provider/model`**
— a routing rule targeted a **force-alias**. A rule's `target:` must name a
**pinned** or **group** alias, because only those select a concrete
provider/model. A force-alias (like `auto: {force: {}}`) fills axes and hands the
decision back to the rules — it is an entry point, never a destination. This one
is worth knowing because it **loads and validates cleanly and fails per
request**, not at startup.

**Nothing appears in the UI, and `/admin/requests` returns an error** — the event
store is off:

```json
{"error":"arbiter: event store disabled (storage.path unset)"}
```

with `event store disabled (storage.path unset)` logged at startup. Set
`storage.path`. This is deliberately distinguishable from "store enabled, no
traffic yet" — a silent empty page would hide which of the two you are looking at.

**An unset `${VAR}` does not fail at startup.** It expands to empty, so
`endpoint: "${MY_URL}"` with `MY_URL` unset produces a provider pointed at
nothing. It is caught later, when a route resolves to it, as a routing or
upstream error — not at load time. Set the variable, or write the literal value.

---

## Next

- **Pointing a client at it** — **[docs/clients.md](clients.md)** — endpoints,
  streaming, attachments, session affinity, prompt caching.
- **Reading what it recorded** — **[docs/observability.md](observability.md)** —
  the event store, the admin UI, the live tail, grouping, discovery.
- **Controlling what goes through** — **[docs/guardrails.md](guardrails.md)** —
  system-prompt injection, rate limiting, and stripping client-injected preamble
  text.
- **Deciding where requests go** — **[docs/routing.md](routing.md)** —
  the `model` precedence, group selection, capability-aware rules, and the
  three ways a request gets classified.
- **Every config key** — [Configuration](../README.md#configuration) in the
  README, and the annotated `arbiter.yaml` at the repository root.