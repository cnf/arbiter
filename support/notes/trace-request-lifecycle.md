# Trace: an HTTP request reaching Execute()

Read this as a tree, not a story. Each line is one real file, with the exact
line number. Nothing here needs to be remembered — just look back up when lost.

```
cmd/arbiter/main.go
│
├─ main()                                    [line 35]
│  ├─ config.Load(...)                       [line 41]
│  ├─ openStore(...)  → writer               [line 55]
│  ├─ store.OpenReader(...) → reader         [line 79]
│  └─ buildPipeline(cfg, ...) → p            [line 93]
│        │
│        └─ internal/pipeline/pipeline.go
│           NewPipeline(...) → *Pipeline     [line 134]
│           (just fills the Pipeline struct  [line 56]
│            with everything buildPipeline
│            assembled — no logic here)
│
└─ (further down in main.go, not yet traced:
    p gets handed to something that starts
    the HTTP server — NOT traced yet)


internal/http/handler.go
│
└─ some handler function, not yet identified
   │
   └─ rt.pipeline.Execute(...)               [line 139]
         │
         └─ internal/pipeline/pipeline.go
            func (p *Pipeline) Execute(...)  [line 267/269]
            ← THIS is where the 9-step work
              (classify/route/guardrail/
              upstream/record) actually runs.
              We have NOT opened its body yet.
```

## What is confirmed vs. not yet done

- ✅ Confirmed: `main.go` builds one `*Pipeline` value and it just holds
  everything (translator, classifiers, router, guardrails, store writer).
- ✅ Confirmed: `internal/http/handler.go:139` is the only place `.Execute()`
  is called in production code.
- ✅ Confirmed: the buffer/logic handoff point — `internal/http/handler.go:128`,
  inside `func (h *Handler) handle(...)` (starts line 118).
  `body, err := io.ReadAll(r.Body)` reads the raw incoming HTTP bytes into
  `body`, a `[]byte`. Line 137 hands `body` straight into `Execute`.
- ✅ Confirmed: `Execute` is a **blocking call**. For a non-streaming request,
  `handle()` sits on `rt.pipeline.Execute(...)` (line 137) until it returns —
  one goroutine per request, blocked for the whole upstream round-trip
  (e.g. 60s upstream = 60s blocked here). Only then does `handle()` write the
  JSON response (lines 156-159).
- ✅ Confirmed: for a **streaming** request, `Execute` returns *early* — not
  the finished content, but an `*upstream.StreamResponse`
  (`internal/upstream/client.go:32`). That struct holds exactly one field,
  `EventChan <-chan *types.NormalizedStreamEvent` — a Go channel, not the
  socket itself. The real HTTP connection to the provider is held by a
  separate goroutine (`internal/upstream/stream.go:167` or `:219`, not yet
  determined which) that reads the provider's raw SSE bytes, parses them, and
  pushes parsed events onto that channel. `handleStream`
  (`internal/http/handler.go:163`) is the thing that then blocks, reading off
  `EventChan` and forwarding to the client for as long as the stream runs.
  So: `StreamResponse` holds the socket **by proxy** — it holds the channel
  fed by whoever holds the socket, not the socket itself. Each request gets
  its own fresh channel + goroutine pair, so concurrent streams never mix —
  no ID/lookup needed, they're just separate objects in memory.
- ❌ NOT yet traced: which of `stream.go:167` / `:219` is the actual
  socket-owning goroutine, and what it does line by line.
- ❌ NOT yet traced: `Execute`'s own body for the non-streaming case (line 267
  onward) — we've only looked at what calls it and what it hands back.

## Next step (pick up here, no need to re-derive anything above)

Open `internal/upstream/stream.go` around lines 167 and 219, identify which
one is the socket-owning goroutine, read what it does.

---

## Execute()'s own body — the straight-line sequence (no HTTP layer left)

User correctly guessed this is where the real density is. It is dense in
*comments explaining why*, not in control flow — no loops except the
pre-guardrail list, no recursion. Exact line numbers (previous session's
numbers in this doc were rough eyeball estimates off truncated output and
were OFF — these are grep-confirmed exact):

```
Execute(payload, format, traceID, sessionHint):        [line 267]

  1. normalize          payload → req                  [line 272]
  2. derive session key from req                        [line 296]
     (BEFORE guardrails run — deliberate, see comment)
  3. snapshot req.SystemPrompt → ClientSystemPrompt      [line 306]
     (before any guardrail can mutate it)
  4. [optional] capture req "as sent"                    [~line 309]
  5. run pre-guardrails, in order                        [line 320]
       → each can mutate req, or reject → recordFailed, return early
  6. [optional] capture req "as guardrailed"              [~line 336]
     (only if step 5 actually changed something)
  7. resolveRoute(req, hasKey) → route, sig              [line 349]
       → picks provider+model; can fail → recordFailed, return early
       → THIS is where classification lives — see below, it is NOT
         a separate step in Execute itself
  8. branch: req.Stream?                                  [line 359]
       yes → executeStream(...), return
       no  → tryUpstream(route, req)                      [line 363]
             (blocks on the real network call to the provider)
```

## Where classification actually fits — inside resolveRoute, not Execute

User asked directly where classification happens. It is NOT a step in
Execute's own body — it is one branch inside `resolveRoute`
(`internal/pipeline/pipeline.go:870`), and only runs as a fallback when
nothing earlier already decided the route:

```
resolveRoute(req, hasKey):                              [line 870]

  1. literal model?  (req.Model IS a real configured model name)   [line 871]
       → yes: route directly. classifyLiteral() still runs, but only
         to LABEL the row for the record — it does not affect routing.
  2. session affinity pin exists? (same conversation, already routed) [line 877]
       → yes: reuse same provider/model. NO classification at all.
  3. client named a pinned/group alias directly?                   [line 897]
       → yes: route directly. NO classification.
  4. req.Model set but not a known model or alias?                 [line 907]
       → yes: REJECT (typo/unknown name). No classification, no routing.
  5. otherwise → REAL classification happens here                 [line 913]
       sig, err := p.classify(ctx, req)
       → sig then feeds p.router.Route(ctx, req, sig), which picks
         the actual provider/model target.
```

Practical consequence: in a normal multi-turn conversation, turn 1 hits
step 5 (classifies), then turn 2+ hits step 2 (session affinity pin from
turn 1) and skips classification entirely. Classification cost is paid once
per conversation, not once per turn — this is deliberate (see comment above
`classifyLiteral`, "every request not yet part of a session gets classified").

## Updated confirmed/not-yet-done list

- ✅ Confirmed: `Execute`'s full straight-line sequence, steps 1-8 above.
- ✅ Confirmed: classification's exact location and the 4 ways it gets
  skipped (literal model, affinity pin, pinned alias, or rejected as unknown).
- ❌ NOT yet traced: `p.classify(ctx, req)` itself (what a classifier actually
  does, `internal/classifier/`).
- ❌ NOT yet traced: `p.router.Route(ctx, req, sig)` (how sig → actual
  provider/model, `internal/router/`).
- ❌ NOT yet traced: `tryUpstream` (line 363) — the actual network call.
- ❌ NOT yet traced: the socket-owning goroutine in `stream.go` (from before).

---

## Where req.Tools goes — traced end to end, and a real gap found

User asked directly: "where is the tool list captured, and passed on?"
Traced across every layer `req` passes through in `Execute`:

```
req.Tools  — touched at exactly these points, nowhere else:

1. pkg/types/request.go:17
     Field exists on the normalized type.
2. internal/translator/convert.go
     Populated from wire format on the way in (normalize);
     read back out to build the wire format on the way to upstream
     (denormalize). This is the only place it's actively used for its
     real purpose.
3. internal/classifier/classifier.go:281
     Read ONLY as a boolean: `len(req.Tools) > 0` — one capability signal
     among others (e.g. "has an attachment"). Never reads the actual
     tool schemas/content.
4. internal/pipeline/pipeline.go (Execute, all of it)
     NEVER touched directly. req.Tools just rides inside req, untouched,
     from normalize (step 2) straight through to tryUpstream, which hands
     the whole req to the translator to build the real outbound request.
5. internal/store/content.go — CaptureRequest (line 73)
     NEVER touched. Confirmed by grep: zero references to `.Tools` in this
     file. CaptureRequest only walks req.SystemPrompt and req.Messages.
```

**This is a real, confirmed gap, not a hypothesis** — the tool *definitions*
a client sends are never written to the store, on any code path. This was
already flagged in the findings file (§11) as a probe finding; now formally
filed as **GitHub issue #60**:
https://github.com/cnf/arbiter/issues/60 — not started, not scoped for
implementation, filed only per explicit "not maybe, definitely" instruction.

Distinct from #34 (closed) — #34 was tool *calls* (what the model invoked)
missing on the streaming path only. #60 is tool *definitions* (what the
client offered) missing on every path, always.

## Note on function/file naming and grouping

User's read after this trace, stated directly: the current naming/grouping
of functions across this flow doesn't match how they'd want to navigate it
for *understanding* — not a functionality complaint, a discoverability one.
Not yet expanded on or actioned; flag for a future session if the user wants
to pursue renaming/regrouping as part of the "back to basics" cleanup (see
findings file §10).

---

## arrival_ts — done: `ts` was finish-time, not arrival-time (ticket #8's root cause)

The `ts` column (`internal/store/schema.sql:23`) is written by `Record()`
(`internal/store/writer.go`) at the *end* of a request's processing — after
the classifier/router/upstream round-trip finishes. `id` (SQLite rowid) is
assigned in that same INSERT, so `id` and `ts` always agree with each other,
but **both disagree with when the request actually arrived**. Concretely:
a classifier sub-request starts and finishes fast, inside the parent's
`resolveRoute` call, and gets its `ts`/`id` stamped *before* the parent's —
even though the parent arrived first. The UI's existing workaround was to
nest by `trace_id` instead of trusting `ts` order — never fixing the root
cause. This is the mechanism behind ticket #8 (title/classifier sort issues).

**Fix shipped:** a new nullable `arrival_ts TIMESTAMP` column, stamped from
`Execute`'s existing `start := time.Now()` — the moment a request entered
the pipeline, not when it finished. `ts` is untouched (still finish-time,
still what pagination/dedup rely on). Only the *parent* (client) event gets
`arrival_ts`; classifier sub-events deliberately keep it NULL — comparing
the parent's arrival against the parent's own finish is enough to establish
causal order; the classifier doesn't need its own arrival stamp for that.

Files touched:
- `internal/store/schema.sql` — added the column + comment.
- `internal/store/writer.go` — migration guard list (auto-adds the column to
  existing DBs), `Event.ArrivalTs time.Time` field, `nullTime()` helper (nil
  for zero time → SQL NULL), INSERT column + arg.
- `internal/store/reader.go` — `RequestRow.ArrivalTs`, `requestRowColumns`
  (shared projection used by `ListRequests`/`GetRequest`/`SessionClientPage`),
  `scanRequestRow`, and `GetRequest`'s own manual scan (it doesn't use
  `scanRequestRow` — it hardcodes its own column list and appends 6 more
  after `requestRowColumns`, so adding a column to the shared const shifted
  those trailing offsets; fixed to avoid silently corrupting `confidence`
  onward).
- `internal/store/discovery.go` — `SessionsForContent`'s own hardcoded
  `WITH ranked (...)` CTE column list (not `requestRowColumns`-driven) also
  had to be updated by hand; caught by test failures
  (`SQL logic error: table ranked has 26 values for 25 columns`) in
  `internal/store` and `internal/ui`, since the CTE's inner SELECT uses
  `requestRowColumns` but its outer `WITH ranked (...)` name list and final
  `SELECT` were both still hand-written to the old 25-column shape.
- `internal/pipeline/pipeline.go` — `ArrivalTs: start` added at all 5 of the
  client-record `store.Event{}` sites that have `start` in scope: the
  non-streaming success path, the non-streaming/generic failure path
  (`recordFailed`), and both `executeStream` sites (failure + success).
  Deliberately *not* added at `recordClassifierCalls` (no `start` in scope
  there, and classifier rows are meant to stay NULL).

Verified: `go build`, `go vet`, `go test ./...`, `golangci-lint run ./...`,
`gofmt -l` all clean on `develop` after these changes. The only test
failures are pre-existing and unrelated (`internal/config`'s two tests fail
on a missing `litellm` endpoint in `arbiter.yaml` — reproduced identically
on plain `develop` before any of these edits, i.e. an environment/config
issue, not a regression from this work).

**Not done, and out of scope for this pass:** nothing yet reads
`arrival_ts` to actually fix ticket #8's sort/nesting behavior in the UI —
this session only got the data plumbed through and captured correctly.
Wiring the UI to prefer `arrival_ts` (or to stop relying on trace-ID nesting
as a workaround) is a separate, not-yet-started follow-up.
