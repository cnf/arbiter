# Arbiter refactor discovery — 2026-09-27

Consolidated from 5 parallel code-review passes (pipeline/router/classifier/guardrail,
store, UI, translator/upstream, config/repo-hygiene) plus manual cross-check against
the 17 open GitHub issues. Repo state at review time: `develop` = `e432404`, build/
vet/test/lint all clean, working tree has only untracked scratch files.

Not a decision document — a discovery list. Nothing here is scoped or greenlit.

## Status update (2026-09-27, later session) — the cleanup pass

The user greenlit a cleanup pass over a differently-numbered list (chat items
1–11, roughly: rebuild debris / oversized files / duplication / dead fields).
Status of *that* pass, since it changes several findings below:

| Chat item | Finding here | Status |
|---|---|---|
| 1 rebuild debris | §2 | ✅ done — `5e76765` (114 lines deleted; dead template funcs, cursor codec, stale docs) |
| 2 oversized files | §3 | ✅ done — `b94e05a`, `e3a9066`, `0cc9de4`, `5d76549`, `0676fd6` |
| 3 duplication | §4 | ✅ done — `bf71df0` (upstream senders), `5ab3d02` (timestamps, tool bucketing, classifier skeleton) |
| 4 dead/write-only fields | §5 | ✅ done — `30aaaee`; **two of the five claims were wrong**, corrected in §5 |
| 5 RateLimit | §5 (⚠️ correction) | → filed **#62**; it is a stalled half-shipped feature, not debris |

Left as-is deliberately: `attachTitleChildren` (§1, not greenlit), the
`RateLimit` code (→ #62), `arrival_ts` backfill (user deferred).

The oversized-file work (§3) is the part that changes *this* document most: the
"Files over 500 lines" list below was computed at `e432404` and is now stale —
every entry in it except the two UI pages has been split. Largest production
files are now `pkg/types/request.go` 659 and `internal/store/writer.go` 441.

---

## 1. The big one: title→session attribution is inference, and its UI half is dead code

Three of five reviews converged on this independently.

- **`ParentSessionForTitle`** (`internal/store/discovery.go:401-474`) is a two-tier
  best-effort guess, not a fact: tier 1 needs the client to send the same
  session-affinity header on the title call as on real turns (opt-in, not
  guaranteed); tier 2 falls back to a fuzzy content-hash join that silently fails if
  `capture_content` is off or the client wraps the title text before sending. The
  fragility was severe enough that the UI layer invented a **4-state enum**
  (`titleParentState`: nested / found-but-off-page / not-found-capture-off /
  not-found) just to explain to a human why the inference didn't resolve —
  scaffolding built to compensate for a root-cause gap, exactly what the
  2026-09-27 revert postmortem described.
- **New finding: `attachTitleChildren` (`internal/ui/requests.go:443`) is dead
  code.** `go`'s deadcode tool confirms it's unreachable from `cmd/arbiter` —
  `sessions.go`'s `buildLane` only calls `attachTraceChildren(foldRequestLines(...))`,
  never `attachTitleChildren`. PICKUP.md and `docs/observability.md` both describe
  title requests as nested under their parent session in the UI; that is **not
  currently true in the running app**, regardless of what shipped in the commits
  #11's pieces 2/3 claimed. Either this regressed silently during the `newui`
  rebuild (a caller that used to invoke it got rewritten and lost the call) or it
  was tested but never actually wired in.
- This is very likely upstream of #59's symptoms — a session investigating "why
  doesn't title nesting work right" may have been chasing a UI that never called
  the nesting function at all.
- **A structurally simpler fix exists and wasn't tried**: trust a client-sent
  session-affinity header **at write time** (stamp the title request's
  `session_key` directly when the pipeline already knows the originating session
  in-process) instead of re-deriving it at read time via two tiers of guessing.
  This was already the direction #59/PICKUP's session-affinity notes were heading.

**Verified independently (not just the subagent's claim):** `grep -rn
"attachTitleChildren" internal cmd` shows it is called only from
`grouping_test.go`. Production code touches it in comments only —
`sessions.go:349` mentions it in a comment but the actual `buildLane` body
(`sessions.go:330`) calls `attachTraceChildren(foldRequestLines(views))` and
nothing else. #11 (closed 2026-09-23, "grouping half piece 2/3 shipped") is
**not actually in effect on `develop` today** — the feature it shipped was
either lost in the `newui` rebuild (#45-#55, which deleted and rewrote
`internal/ui` page-by-page) or never wired into the page that replaced it.

**Recommendation:** before touching #59 again, decide whether the fix is "wire
`attachTitleChildren` back into `buildLane`" (small) or "the whole
read-time-inference design should go" per the postmortem's explicit instruction
not to patch further without re-deriving the mechanism. Also worth reopening
#11 or filing a fresh regression ticket — it's currently closed on the board
while its shipped behavior is absent from the running code.

---

## 2. Newui-rebuild debris — same pattern as the just-closed #56/#57

The rebuild deletes old pages wholesale each phase; every review pass flagged more
leftovers of that pattern:

- `requestsForSession` (`internal/ui/sessions.go:386`) builds a link to
  `/admin/ui/requests?session=...` — that route was deleted in #54's rip-out.
  Currently harmless (nothing calls it), but identical in shape to #56.
- Dead keyset-cursor codec (`encodeCursor`/`decodeCursor`,
  `internal/ui/requests.go:531,537`) — supported the flat requests page's
  pagination; that page is gone, only their own unit test still calls them.
- `overviewURL` template func (`internal/ui/overview.go:769`) — registered, zero
  template callers; the toolbar builds its own query string instead.
- Stale doc comment in `internal/ui/sessionsLive.go:171` references `live.go`,
  which commit `fd7ea89` deleted.
- `requests.go`'s own top-of-file comment justifies keeping the file around by
  listing four consumers — two of which (cursor codec, title children) turned out
  to be dead, so the justification itself is now stale.

**Recommendation:** one cleanup pass, low risk, could be a single small PR:
delete the four dead-code items, fix the stale comment, re-decide on
`requestsForSession`/nesting only after #1 above is resolved.

---

## 3. Oversized/monolithic files (structure, not correctness)

| File | Lines | Concerns mixed |
|---|---|---|
| `internal/pipeline/pipeline.go` | 1580 | HTTP-facing request lifecycle, streaming reassembly, retry/cooldown state machine, cost computation, event recording — 5 distinct jobs in one file |
| `internal/store/reader.go` | 1444 | windowed aggregate stats, session/transcript navigation, content-block/guardrail-diff retrieval, generic request CRUD — the one file in `internal/store` that never got the one-file-per-concern treatment its siblings (flow.go, summary.go, discovery.go, content.go) already have. Its own doc comment ("five queries do not warrant a codegen pipeline") is stale — it has 23 exported methods now. |
| `cmd/arbiter/main.go` | 1304 | **Not just wiring** — contains a full untyped-map interpreter for policy rules, guardrails, and classifier config blocks (`policyRules`, `buildGuardrail`, `buildLLMClassifier`, `buildDecisionsClassifier`, ~600 lines) that arguably belongs in the packages it configures (router/guardrail/classifier), not in `main`. This is the real reason main.go is this large, not process wiring. |
| `internal/config/config.go` | 1328 | Size is mostly justified (genuine domain breadth: classifiers/aliases/catalog/decisions), well-organized internally with a consistent validate-function pattern. Splittable by concern (validate_classifiers.go / validate_aliases.go / catalog.go) for navigability, not a correctness issue. |

All four were independently flagged; none is a "delete and redo," all are
splittable without behavior change.

---

## 4. Duplication (four separate instances, four different packages)

- **`LLMClassifier.tryClassify` vs `DecisionsClassifier.tryDecide`**
  (`internal/classifier/llm.go:206-270`, `decisions.go:238-322`) — near-identical
  candidate-iteration/call-info-building skeleton, ~60 duplicated lines. A third
  model-backed classifier type would be the third copy.
- **`internal/upstream/client.go` / `stream.go`** — 4 near-duplicate HTTP-request-
  building functions (`sendAnthropic`/`sendAnthropicStream`/`sendOpenAI`/
  `sendOpenAIStream`). Notably, **the project's own test file already documents
  this as a known regression risk** (`anthropic_request_test.go:44-47`: "a fix
  applied to only one of them is the classic way this regresses") — it's a
  flagged-but-unfixed risk, not a fresh discovery.
- **Tool-name classification** duplicated verbatim between
  `internal/ui/toolcall.go:104-124` and `internal/ui/transcript.go:542-556` (same
  bash/grep/edit/read substring buckets, two independent switches).
- **Timestamp parsing/formatting** solved three separate times in
  `internal/store`: `counts.go:tsText`, `flow.go:parseStoredTs`,
  `reader.go:formatTime` — each re-deriving the same "ts is TEXT in Go's
  `time.Time.String()` layout, not RFC3339" fact, with **inconsistent error
  handling** (one silently zeroes on failure, another falls back to the raw
  string). `counts.go`'s own comment documents a real historical incident from
  exactly this bug class ("made a 30-minute window include everything from the
  last two hours").

---

## 5. Dead / write-only fields (cheap batch cleanup)

**Status: mostly done (commits `5ab3d02`+ for #5, and the #6 batch). Each item
below was re-verified by deleting it and letting the compiler judge, not by
grep — see the correction at the end.**

- `pkg/errors.Attributes`, `UpstreamError.Retriable` — ✅ **removed.** Allocated
  or computed on every error, read by nothing. Confirmed dead: deleting both
  field declarations and every `Attributes: make(...)` allocation still builds.
- `types.Metadata` — ✅ **removed** — including the return value it forced on
  `Router.Route` (the whole `(Route, Metadata, error)` triple is now
  `(Route, error)` across all 6 implementations). Every implementation built a
  real value; the only caller discarded it.
- `RequestFilter.IncludeContent` — ✅ **removed**, as its own doc comment asked.
- `NormalizedResponse.RoutingDecision` — ✅ **removed.** Written once
  (`pipeline.execute`), read nowhere. Note this is a *different* field from
  `Route.Rationale`, which **is** read (logged and recorded as
  `RoutingRationale` on every stored event) — do not conflate them.
- `AnthropicResponse.StopSequence` — ✅ **removed.** Parsed off the wire, never
  copied onto `NormalizedResponse`. This does mean the information is dropped;
  it was already being dropped, the field just implied otherwise.
- Unused `Factory`/`Registry`/`Register()` scaffolding in all **three** packages
  (`router`, `classifier`, `guardrail`) — ✅ **removed.** Nothing called
  `Register()`; `buildPipeline` type-switches directly.
- `internal/logging.LogEntry` — ✅ **removed** (found while verifying); the whole
  struct was unreferenced, not only its `Attributes` field.

### ⚠️ Correction: two claims in this section were wrong

1. **`types.Metadata` (the model-catalog one, `pkg/types/models.go`)** was
   listed as dead. It is **not**: `internal/http/models.go` serializes it into
   the `/models` response (`metadata` JSON key, `omitempty`), so it is reachable
   outward API surface. Left in place. (This is a *different* type from the
   removed `types.Metadata` routing struct — the two shared a name.)

2. **`RateLimitInfo` / `NormalizedResponse.RateLimit`** — ⏸️ **deliberately NOT
   removed.** Its doc comment claims it feeds routing (degrade to OpenRouter on
   a low Anthropic 5h window); that routing does not exist, so it is a stalled
   half-shipped feature. Deleting it would be discarding a stated intent, not
   cleaning debris. Left for a decision: either wire the routing or drop the
   whole rate-limit capture. It is populated from real headers today, so it
   is not simply dead.

**Method note:** this section was originally "confirmed by grep". Grep cannot
see JSON tags, reflection, or interface-method satisfaction. Every item above
was instead verified by removal + compile, which is exhaustive for Go. Two of
the original claims did not survive that.

---

## 6. Real bug found (not cleanup): thinking-block signature lost on non-streaming Anthropic path

`types.ContentBlock` / `types.AnthropicContent` have **no `Signature` field at
all** (only the streaming-only `NormalizedStreamEvent` does). All four
non-streaming content-conversion sites in `convert.go` merge "thinking" into
"text" and copy only `.Text`. Anthropic requires the signature to be replayed
verbatim on the next turn — so a multi-turn **non-streaming** thinking
conversation through Arbiter is silently corrupted turn over turn. Same failure
shape as the historical #33 (empty replies) and #27 (cache-hit reporting) bugs.
Worth its own ticket; this is a correctness bug, not tech debt.

---

## 7. Test coverage gaps

- `internal/logging` has **zero** test files — the one shared logging path
  every package depends on.
- `convert_test.go`/`prompt_details_test.go` still build test input via
  hand-constructed Go structs rather than literal captured wire JSON, unlike
  `relay_fidelity_test.go`/`thinking_request_test.go`/`attachment_test.go`,
  which explicitly exist to avoid the self-consistency blind spot ("a struct
  built by hand only proves Arbiter agrees with itself" — the exact blind spot
  that hid the #33/streaming bugs originally).

---

## 8. Repo hygiene (no code risk, just cruft)

- **Three near-duplicate untracked scratch binaries**: `cmd/bigpreview/`,
  `cmd/previewserver/`, `cmd/realpreview/` — all wire up nearly identical mux
  route tables against `internal/ui`+`internal/store`, differing only in data
  source (synthetic fixture vs real read-only DB) and port. One parameterized
  tool would replace all three.

  **DONE (2026-09-27)**: moved to `support/preview/` (one package each, so
  `go build ./...` still compiles them as it did in `cmd/`) with a README. The
  three-way duplication was left in place deliberately — converging them is a
  real refactor of throwaway tools, and being tracked but unshipped is what
  they actually needed. See `support/README.md`.
- `build/` (gitignored) has grown to **~1.8GB**: `livecheck.db` alone is 1.2GB,
  `fontcache` 449MB, stale `linux`/`osx` cross-compiled binaries ~180MB.
- `support/downloads/` has several MB of raw upstream JSON dumps
  (`litellm-prices.json` 2.5MB, `models-dev-*.json` 5MB) — worth confirming
  whether these are tracked in git or just sitting there.
- `support/example1.json` — 0-byte, untracked, unknown purpose. Confirm or
  delete.
- `devenv.nix` has a commented-out health-check block for `mockllm`.

---

## 9. Store: migration debt and known-accepted growth (lower priority)

- **5 hand-maintained `ALTER TABLE` guards** in `writer.go` (no schema-version
  table) — manageable today, tested, but no ceiling; a 6th/7th column follows
  the same manual, unchecked path.

  **Observed live (2026-09-27)**, with the exact failure this debt predicts:
  `realpreview` serving `build/livecheck.db` returned `500` with
  `list requests: SQL logic error: no such column: arrival_ts (1)`. That
  database was a snapshot taken before `arrival_ts` shipped, and
  `store.OpenReader` (`reader.go:38`) does NOT run the migration pass — only
  `NewSQLiteWriter` does, via `addColumnIfMissing`. So a reader against a
  pre-migration database fails on every query touching a newer column, and the
  failure is a hard 500 rather than a degraded view.

  **Status: the instance is resolved** — the user refreshed `build/livecheck.db`
  from current production, and `realpreview` now serves that database
  successfully (verified: 152KB page, no error, current session keys). The
  *mechanism* is unchanged, though, and that is the part worth keeping: the
  migration is writer-only by construction (a read-only consumer cannot repair
  a schema it does not own), so **any** stale database is still unreadable by
  the UI until something opens it with a writer. Refreshing the file worked
  because the fresh copy already carries the column — it did not exercise the
  migration path, it sidestepped it.

  This also sharpens the arrival_ts backfill question (user: "hold off until i
  know everything else works"): the backfill is about old *rows* in a live
  database, which is a different thing from a stale *file* like this one.
  Neither is urgent, but they are not the same problem and the resolution of
  this one says nothing about the other.
- **`content_refs` grows ~O(n²) per long conversation** (a client that resends
  full history each turn re-references every prior block every turn) — real,
  measured against production (4M rows before an index fixed multi-second
  queries), mitigated by an index + TTL sweep + session-scoped dedup in the UI,
  but none of the mitigations cap *per-conversation* reference count directly.
  Accepted debt, not a live incident, but worth knowing the mitigations are
  indirect.

---

## Cross-reference against existing open issues (confirmed, not re-discovered)

- **#59** (transcript rework) — directly implicated by finding #1 above; the
  dead `attachTitleChildren` call is new information relevant to scoping it.
- **#41** (dead "full block" link), **#56/#57** (closed, same debris pattern as
  finding #2) — matches the general "rebuild leaves dead links/handlers" shape.
- **#9** (no client column), **#38** (header ordering bug), **#36**
  (`choices[].index` bug), **#18** (cache token pricing), **#26** (deferred
  subagent attribution) — all independently re-confirmed still accurate and
  unchanged; no new information, just verified still true.

## New candidate items not yet on the board

A. Resolve #1 (attachTitleChildren dead / inference redesign) — before any
   further #59 work, per the postmortem's explicit "re-derive first" instruction.
B. Cleanup pass for §2's dead-code debris (small, low-risk).
C. Fix §6's non-streaming thinking-signature bug (correctness, medium).
D. §3's file splits — pipeline.go, reader.go, main.go's config-interpreter
   extraction (large, no rush, no behavior change).
E. §4's four duplication instances (small/medium each; upstream one is a
   flagged-but-unfixed known risk).
F. §5's dead-field batch cleanup (trivial, bundle into one PR).
G. §7 test-coverage gaps (small).
H. §8 repo hygiene (housekeeping, no urgency).
I. §9 store migration versioning (optional, low urgency given only 5 entries).

---

## 10. Direction change (user, 2026-09-27, same session): back to basics first

All findings above stand, but the user reframed priority before any ticket work
starts: **don't chase feature→optimize→debug loops. Break Arbiter down to its
core functional blocks, verify each works, minimize duplication — and explicitly
audit for core-logic-vs-UI-logic crossover — before any further UI work,
including a planned full audit of every SQL query the UI issues.**

**#8 confirmed as the concrete example of the crossover pattern to hunt for.**
Verified in code: `ts` is stamped at **finish** time
(`internal/store/writer.go:46`, filled by `Record`). The store layer already
knows this is semantically wrong for causal ordering —
`internal/store/reader.go:456` and `:506` both carry comments admitting "a
classifier routinely finishes before the request that spawned it," and the
shipped fix was **nest children by `trace_id` instead of sorting/storing by
arrival time** (`SessionChildren`). That workaround is *why*
`attachTraceChildren`/`attachTitleChildren`/`foldRequestLines` exist as three
composed UI-layer passes (see §1/§3 above) — they compensate in the
presentation layer for a fact (arrival vs. finish time) the core pipeline never
recorded correctly. This is the pattern to generalize: **where else has a
core-layer gap been patched with UI-layer inference instead of fixed at the
source?** Title-session attribution (§1) is the other confirmed instance.

**Note: the package import graph is already clean** at the boundary level —
`internal/ui` imports only `internal/store` + `internal/logging`, never
`pipeline`/`router`/`classifier`/`guardrail` directly (verified via grep). So
the crossover isn't "UI code calling core code" — it's "core recording the
wrong/incomplete fact, and UI reconstructing/inferring around the gap." Look
for that shape specifically, not import violations.

**Planned sequencing (multi-session, not yet started):**
1. Map Arbiter's core pipeline blocks (ingress → normalize → classify → route
   → pre-guardrail → upstream → post-guardrail → denormalize → record) —
   one responsibility per stage, confirm no duplication, no UI-shaped
   compensation logic living in the wrong layer. #8 (arrival vs finish time)
   is the first concrete case to fix here, not in the UI.
2. Audit core/UI boundary specifically for other "UI infers a fact core should
   have just recorded" instances (title attribution + #8's timestamp are the
   two found so far).
3. Only after core is solid: full audit of every SQL query
   `internal/ui` issues against `internal/store`'s reader — which are
   legitimate presentation queries vs. which compensate for a missing/wrong
   core fact.

Nothing in this section has been started. This is the agreed direction for
subsequent sessions.

---

## 11. Confirmed gap found while probing the docs question: tool definitions are not captured

Traced live (this session) as a worked example of "does X get stored":
`req.Tools` exists on the normalized request (`pkg/types/request.go:17`) and
survives translation both directions (`internal/translator/convert.go`), but
`store.CaptureRequest` (`internal/store/content.go:73`) only walks
`req.SystemPrompt` and `req.Messages` — it never reads `req.Tools`. So the tool
*definitions* (schemas) a client sent are never written to the `blocks` table.
What **is** stored is `tool_calls_json` (`schema.sql:43`) — the *names* of
tools the model chose to call in its response, a different and much smaller
fact. There is no way today to answer "what tools was this agent offered at
this point in the conversation" from the store. Add to the core-pipeline
findings once §12 below is written and the fix location is clear (likely
`CaptureRequest`, store schema, or a deliberate decision not to store it —
undecided).

## 12. Standing instruction: document the code-tracing process itself, live, as we go

User's actual problem, stated directly (2026-09-27, same session): they don't
have a feel for *where in the code decisions are made* or *how data flows*,
and reading the existing docs/code doesn't fix that — the docs are written by
agents for agents, in a register the user (who is autistic and needs concrete,
literal, non-hand-wavey explanations) can't parse. They tried to trace
`req.Tools` from `cmd/arbiter/main.go` outward themselves after I named the
three destination points, and got stuck — pointing at scattered file:line
locations without the connecting hops in between doesn't help; jumping between
"how main.go wires the pipeline together" and "how a struct field actually
gets populated at runtime" is a different kind of trace, and skipping straight
to conclusions skips exactly the part they need.

**What actually worked, to repeat:** the three-grep pattern for finding a data
field's path — `pkg/types` (is it on the normalized type) → `internal/translator`
(does translation preserve it) → `internal/store` (does anything ever capture
it) — is a real, repeatable trail specific to this codebase's shape (every
request-shaped field flows through those three layers, since router/classifier/
guardrail only ever read the normalized type, never add fields to it).

**Standing instruction for future sessions:** when tracing *anything* through
the code with the user, do it as a literal hop-by-hop walk — pick a real
starting point (e.g. `main.go`'s pipeline construction), open the actual next
file, show the actual next line, confirm it lands before moving to the next
hop. Write each confirmed hop down as we go (file:line + one line of what
connects it to the previous hop), in the order actually walked, not
reorganized afterward into a clean narrative. This running trail *is* the
documentation deliverable — not a polished prose doc written after the fact.
User explicitly wants this documented "along the way," i.e. build it during
the trace, don't summarize at the end.

Format/location of this running trail: **undecided.** Options not yet
discussed with the user: a growing `docs/traces/*.md` per subsystem, inline
code comments at the exact hop points, or something else. Do not assume a
format — ask, or default to appending to this same findings file under a new
dated section, next session.

Session ended here at the user's request ("maybe i need a break"). No further
work performed after this point. Nothing was implemented; only discovery,
verification, and this documentation-approach discussion happened across the
whole session.
