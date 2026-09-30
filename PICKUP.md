# PICKUP — Arbiter session handoff

This is a **map, not a source of truth**. Anything that can drift (git state,
issue state, row counts) is a *command to run*, not a fact to trust. The
GitHub issue tracker is authoritative for work items; the code is
authoritative for behavior; `support/notes/refactor-findings-2026-09-27.md`
is the running discovery/decision log for the current refactor pass.

Written 2026-09-28, at `develop` = `d76f32e`; updated same day at `69b087f` —
see §5 (verified state), §6 (#44 closed, #26 split), §7 (new landmines).
Updated again 2026-09-28 at `e78e4f9` — see §5 (Discovery rollup +
hide-ignored-by-default landed) and §7 (content_ttl / rollup landmine).
Updated again 2026-09-29 at `dec5f70` — see §5 (session-affinity pin-key
split + `no_pin`, #69/#70/#71/#72 landed), §6 (#70/#71/#72 closed, #69 parent
still open), §7 (composite pin key, `no_pin` config).
Updated again 2026-09-29 at `8345dff` — see §5 (#59 landed: per-request
preamble button/modal + a real page-load fix, squashed to one commit), §6
(UI freeze explicitly lifted for #59 only, #73 filed and open), §7 (the
`contentFor`/`ContentForRequests` split, `.bench/` harness pattern for
measuring against a DB copy without touching the live instance).
Updated again 2026-09-29 at `b603b74` — see §5 (#73 landed: bidirectional
lazy-load pagination), §6 (#73 CLOSED, item 3/search explicitly deferred
pending a fresh scoping conversation), §7 (IntersectionObserver `root` must
be the actual scrolling ancestor not the viewport; a page's own inline copy
of a partial's markup can silently diverge from the partial itself).
Updated again 2026-09-29 at `59a0b7d` — see §5 (#63 landed: tool definitions
now render in the transcript inspector), §6 (#63 CLOSED — the last item on
the pre-freeze UI backlog; #8 remains open and still frozen).
Updated again 2026-09-29 at `4bb797b` (PICKUP-only commit, no code change) —
this file itself now reflects the #63 landing above; nothing new to verify.
Updated again 2026-09-30 at `40c4c1a` — see §5 (#22 landed: content_refs
gains a `name` column for attachment filenames), §6 (#22 CLOSED).
Updated again 2026-09-30 at `dde3912` — see §5 (#18 landed: cache-read/
cache-write token pricing modeled end to end, plus a catalog-convert
sidebug fix), §6 (#18 CLOSED).
Updated again 2026-09-30 at `540c3cf` — see §5 (#75 landed in two commits,
`acfbd69` OpenAI + `540c3cf` Anthropic: safe field diagnostics —
message index/field/JSON type only, never the value — for a request whose
`content`/`system` field is neither the accepted string nor array shape),
§6 (#75 CLOSED).
Updated again 2026-09-30 at `5c8f68b` — see §6 (#40 landed: four new
`types.MatchMode` values — `line_prefix`/`line_exact`/`paragraph_prefix`/
`paragraph_exact` — fix prompt_rewrite's `prefix`/`exact` modes silently
no-op'ing on a multi-source system prompt; #40 not yet closed on the
tracker, pending your go-ahead).

---

## 1. Orient — run these, don't read files

```bash
git status -sb
git log --oneline -15
devenv shell --no-tui -- bash -c 'gh issue list --repo cnf/arbiter --state open --limit 40'
```

`git` works directly on the host copy of the repo. **`gh` and `go` need
devenv** — see §3 for the exact invocation and its landmines.

## 2. What this project is

Arbiter — a from-scratch Go LLM proxy/gateway. Single binary, SQLite event
store, web UI for inspecting traffic.

**Its one job, in the user's words:** *"the point is to see what is
happening"* and *"nothing should be invisible. if it happened, it should be
shown."* That is the priority lens for judging any change — a request, a
classifier call, a failure, a rejection are all *requests* and should all
leave a row.

**First principle restated even more sharply this session:** *"what is shown
should be correct. period. HOW we show it we can debate on, but it should
always be clear what is happening."*

**Scale, and why it matters for design calls:** single-user deployment. One
user, single-digit concurrency at worst, ~1 client at a time. Real clients are
**Hermes** and **opencode** — neither sends session metadata (no `session_id`,
no `prompt_cache_key`). Claude Desktop runs in 3p mode against LiteLLM, *not*
Arbiter. Collision/sharding/guardrail machinery sized for multi-tenant traffic
is pure loss here — say so when you see it proposed, including by yourself.

## 3. Environment — non-negotiable

READ `/container-instructions.md` first — it is the authority for the
container's mounted locations, toolchain wrapper, and scratch rules.

  - A cold `devenv shell` eval can take 100s+ the first time in a fresh
    container; it's cached (~50ms overhead) for the rest of that container's
    life. This is normal, not a hang.

- **Secrets:** commands that touch secretspec need `SECRETSPEC_REASON="..."`.
  Never open `.secrets/`, `devenv.yaml`'s secret config, or any key/env file
  to obtain a credential — ask instead. A full `export -p` dump has leaked
  real secrets (`GITHUB_TOKEN`, `LITELLM_API_KEY`) into a scratch file once
  already this project — it was deleted immediately, never staged. Only ever
  capture `$PATH` if you need to inspect the environment, never a full export.

- **One test needs an env var.** `TestShippedArbiterYAMLLoads` fails on a bare
  `go test ./...` with `provider "litellm": missing endpoint`, because the
  shipped `arbiter.yaml` interpolates `$LITELLM_URL`. Pre-existing, not your
  change:
  ```bash
  devenv shell --no-tui -- bash -c 'LITELLM_URL=http://localhost:4000/v1 go test ./...'
  ```

- **`arbiter.yaml` in the repo is an EXAMPLE**, not the deployed config —
  don't read it as ground truth for what's actually running or what a
  feature's real-world shape looks like.
- **The deployed config and store are not in git** — the live config is
  `/data/arbiter/arbiter.yaml` (read-only from this container; the user edits
  and deploys it himself) and the live store is `/data/arbiter/arbiter.db`
  (query read-only, e.g. via the sqlite3 in devenv). A repo-vs-live config
  divergence is the expected state, not a finding.

### Hard boundaries

- **Work only inside this repo.** Never write to `/tmp`, `~/.config`, or
  anywhere else on the host. `build/` is gitignored and is the usual scratch
  home; `support/` is for things worth keeping that you don't necessarily
  want committed yet — it's picked up by the user's laptop backups, unlike
  `build/` which can be scratched anytime.
- **Never send traffic through, start, restart, or reload a live/running
  instance.** Reaching a live process or a real upstream requires asking
  first.
- **Never open a key/startup/env file to obtain a credential.**

## 4. How work is done here

- **Start only on an explicit go.** The user plans across parallel threads
  and most ideas are thought experiments. "Let's design X" is exploration.
  Wait for "let's start" / "make it so" / equivalent. Violated more than
  once in past sessions — watch for it.
- **Tight scope.** Say-so is explicit ("move phase 7 to the front, nothing
  else"). Do not add polish, refactors, or adjacent fixes that weren't asked
  for.
- **Separately shippable phases**, not one big commit. Commit bodies carry
  the *why* — write them properly.
- **A test that passes with the feature deleted proves nothing.** Revert the
  fix and confirm the test fails *for the right reason* before reporting
  done.
- **Check the data/code before drawing conclusions from either alone.**
  Reasoning about a component in isolation has produced wrong conclusions in
  this repo's history more than once. Standing example: don't infer
  "current behavior" from `build/livecheck.db` — it is **historical dev
  data**, a record of things tried, reverted, and changed, not a snapshot of
  what the current code does. Verify against the code + direct test
  execution instead.
- **Don't declare a ticket's narrow acceptance criteria satisfied and call
  the underlying problem solved.** The user calls this "the circle": fix a
  narrow technicality, declare victory, move to the next ticket, while the
  actual thing never works end-to-end. When in doubt, ask what "done" means
  for the *real* problem, not the ticket text.

## 5. Verified state as of `540c3cf`

Landed most recently (all on `develop`):

| commit | what |
|---|---|
| `540c3cf` | #75 (Anthropic half): the ticket was filed OpenAI-only because that's where the failure was noticed, not because Anthropic requests are exempt — `AnthropicMessage.Content` and `AnthropicSystem` have the identical bare-string-or-array wire ambiguity as the OpenAI side. `ContentShapeError` (added in `acfbd69` below) gains a `PerMessage` bool: true renders `messages[i].field: got <type>; expected string or array`, false (for the top-level `system` field, which has no message index) renders `field: got <type>; expected string or array`. New `AnthropicRequest.UnmarshalJSON` mirrors `OpenAIRequest`'s: on a per-message shape error it re-walks the raw `messages` array once to attach the correct index (the field-level unmarshaler can't see its own array position); a top-level `system` error or any unrelated error passes through unchanged. |
| `acfbd69` | #75 (OpenAI half, as filed): `OpenAIMessageContent.UnmarshalJSON` returns a new `types.ContentShapeError` (`pkg/types/content_diagnostics.go`) — carrying only message index, field name, and the observed JSON type (`object`/`number`/`boolean`/...), never the value or message text — instead of the raw Go `json.UnmarshalTypeError` when `messages[i].content` is neither a string nor an array. `OpenAIRequest.UnmarshalJSON` catches that error and re-walks `messages` only on the failing path to fill in the index. `internal/http/handler.go`'s new `errorLogFields` surfaces `message_index`/`field`/`got_type` as structured log fields via `errors.As`, additively — the client-facing 400 status/body is unchanged. Malformed JSON and unrelated field mismatches (e.g. `max_tokens` as a string) keep the original generic error rather than guessing a location that isn't real. |
| `dde3912` | #18: `cost_usd` priced plain input/output only; `cache_read_tokens`/`cache_write_tokens` were captured and stored but never priced in. `pkg/types.ModelCost`/`config.ModelCatalogEntry` gain `CacheReadCostPerMTok`/`CacheWriteCostPerMTok`; `cmd/catalog-convert` reads them from litellm (`cache_read_input_token_cost`/`cache_creation_input_token_cost`, scaled per-token→per-million like the existing fields) and models.dev (`cost.cache_read`/`cost.cache_write`, already per-million); `cmd/arbiter/builders.go` carries them into the runtime catalog; `Pipeline.computeCost` prices `usage.CacheRead`/`CacheWrite` at those rates, same formula as input/output, with an unstated rate pricing at 0 rather than being estimated from the plain rate. Sidebug fixed alongside: an unset provider `source:` bound positionally to `sources:`'s first entry regardless of kind, so listing models.dev before litellm made a litellm-only provider silently resolve against the wrong source and fail downstream with a confusing "no provider key" skip (`pickSource` in `cmd/catalog-convert/convert.go`); default source selection is now content-aware — it searches `sources:` in order for the first source that actually yields a match for that provider. Also added a generic `provider:` mapping key as a fallback for whichever kind-specific key (`litellm_provider`/`models_dev_provider`) is unset, covering the common case where the slug is the same across both source kinds; a kind-specific key still wins when set. New tests including `TestComputeCostPricesCacheTokens` (computeCost had no direct unit test before this) and `TestDefaultSourceIsContentAwareNotJustFirst` (the sidebug regression). No catalog YAML regenerated — none is checked into the repo. |
| `40c4c1a` | #22: `content_refs` gains a `name TEXT` column (schema.sql + `addColumnIfMissing` migration, mirroring `request_kind`'s precedent) carrying an attachment's filename — required for re-emission fidelity on the OpenAI wire shape (`file.filename`/`input_file.filename` are mandatory there) but previously unrecoverable from storage (attachments are hash-only by the 2026-09-15 "save hash, don't save file" decision, and the filename was folded into the block hash but never stored as retrievable data). `types.ContentBlock.Name` and both translators already carried it end to end — only the storage layer had the gap. Lives on the reference row, not the hash-keyed `content` table, since the same bytes can arrive under different names (`role` already sets this precedent). `store.Block`/`store.ContentBlock` gain `Name`; `CaptureRequest`/`CaptureResponse`/`writeContent`/`contentFor`/`ContentForRequests` all touched. Empty for images on both wire formats, by design. No UI display added — out of scope per the ticket. |
| `59a0b7d` | #63: tool definitions (captured since #60, never rendered) now show in the transcript inspector as a collapsed "tools offered (N, size)" section — name, optional description, raw schema behind the existing `transcript-snippet` toggle. Two places were silently dropping `tool_def` blocks (`msg_index = toolDefsMsgIndex = -1`, since a definition belongs to no single message): `ContentForRequests`' SQL only let a request-direction row through on `role='system'` or newest-`msg_index`, now also `block_type='tool_def'`; `newestRequestMessage` (`internal/ui/transcript.go`) exempted tool_def blocks the same way it already exempts nothing-per-turn narrowing for the system prompt. `splitBlocks` gained a `tool_def` case populating `Inspector.ToolDefs`/`ToolDefsBytes`. Sized in bytes (`fmtBytes`, SI/decimal: B/kB/MB/GB), not tokens — Arbiter has no real tokenizer, only a char/4 routing heuristic (`internal/classifier.estimateTokens`) never meant to be shown as fact, same reasoning `fmtChars` already documents. `TestTranscriptShowsToolDefinitions` confirmed to fail without the fix (no rendering at all, not a tautology) before the fix landed. Closes the last item on the pre-freeze UI backlog (#8 is the remaining open, still-frozen item). |
| `b603b74` | #73: bidirectional lazy-load pagination for the transcript admin UI. `SessionClientPageBefore` (`internal/store/sessions.go`) backs "load older" by reusing the existing ASC offset-paging with a computed earlier `(offset, limit)` — no new SQL; a `DESC LIMIT/OFFSET` attempt was tried first and is wrong (OFFSET counts from the table end, not from a position). `transcriptPageSize` 100→30 (fixed constant, not viewport-tuned — user explicitly rejected teaching the server about viewport height). `?seq=N` now centers the window (`centeredOffset`) instead of page-aligning forward, so a deep link has room to scroll either direction. `transcript-turn-inspectors` is now a shared template block included by both `transcript-more` and `transcript-older` fragments, so a lazy-loaded row always ships its own inspector (was bug 1: rows loaded via "load more" had no paired inspector markup). Client-side: an `IntersectionObserver` in `transcript.js` watches the load-more/load-older buttons and auto-clicks one when visible — root MUST be `.list-pane` (the actual scrolling ancestor), not the viewport, see §7. Both load controls now self-replace (`hx-target=self, hx-swap=outerHTML`) via a shared `transcript-more-control`/`transcript-older-control` template, fixing a real bug where `session.html`'s own hand-copied button never got the self-replacing update and caused an infinite re-fetch loop at a fixed offset. Verified live against the repo's own `support/preview/bigpreview` (and a throwaway larger seeder, since deleted) via Playwright in a headless Chromium, not just `go test` — see §7 for the devenv Playwright recipe if needed again. Item 3 from the original scoping (search/filter over a partially-loaded window) was explicitly NOT done — user wants to rethink search as its own piece; do not start it without a fresh scoping conversation. Full detail: `/data/memory/-home-cnf-Documents-Projects-Arbiter/memory/project_73_lazy_load_pagination_2026-09-29.md`. |
| `8345dff` | #59, squashed from 5 commits (`e366348`..`53154b9`) at the user's request once the perf fix landed: per-request "system preamble" button + modal in the transcript inspector (each request's own system prompt as sent, its original client form when a pre-guardrail rewrote it, a line diff between the two, a "touched" indicator) — query-neutral, no cross-turn comparison. The modal is a shared shell; each turn's own hidden tabs+panels get MOVED in on click, not fetched, so a per-request preamble costs zero extra queries. Store-side: `ContentForRequests`/`RequestExtras` batch the page's per-turn reads into 2 queries (was 2×N); `ContentForRequests` additionally pushes "only the newest resent message per direction, or any role=system row" into SQL via a msg_index-bounds CTE, so SQLite stops materializing content bodies for rows a stateless resend produces and the page never shows — measured on a real 100-turn production page: 26,301 rows/53MB read to keep 315 before this filter. End-to-end, comparing isolated fresh processes against a copy of the real DB (not the live instance): page load went from 1.32s (this session's pre-SQL-fix state) to 0.82s. Payload is UNCHANGED (~8.85MB for that page) — this was a read-cost fix, not a payload fix; the payload problem was #73, now landed above (`b603b74`). `contentFor` (singular, backs the public JSON endpoint + lanePreview) was deliberately left unfiltered — only `ContentForRequests`' one caller (the transcript page) gets the narrower query. |
| `dec5f70` | #71: `session_affinity.no_pin []string` in config, validated against `request_kind` values known from configured aliases/classifiers (`knownRequestKinds`). Ships `[]` — Arbiter can't know a client's traffic shape. `Pipeline.SetNoPin` follows the existing post-construction setter pattern (`SetCaptureContent`/`SetConfigEpoch`) rather than growing `NewPipeline`'s already-17 positional args; `pins(kind)` gates both pin-write sites (`execute.go`, `streaming.go`). Classifier/alias `RequestKind` still only labels — `no_pin` is policy read only at the point a pin would be written. `docs/clients.md` + `arbiter.yaml` documented. |
| `0885d1e` | #70 (the real fix, #69's root cause): the affinity pin was keyed on the same `X-Session-Id` Hermes stamps on ALL chat-scoped traffic (main thread, title calls, subagent runs), so non-main traffic collided with and overwrote the main thread's pin — forcing repeated re-classification and mid-conversation provider churn. Fix: pin key becomes `session_key` (the raw header, untouched — grouping/UI still reads it as before) **+** `prompt_hash = hash(client system prompt)`. `affinity_pins` gets a `prompt_hash` column and a composite `(session_key, prompt_hash)` primary key, with a migration that rebuilds any pre-existing table (`internal/store/writer.go`). Also folded into this commit: #72's fix (`ActiveSessionCount` → `COUNT(DISTINCT session_key)`, `SessionPinExpiry` → `MAX(expires_at) GROUP BY session_key`) since the composite key makes those queries wrong on day one otherwise; and a latent bug fix (SQLite `MAX()` over a TIMESTAMP column doesn't scan into `time.Time` — scan into `string` + `ParseStoredTime`). Zero config, zero classifier dependency: a different system prompt is structurally guaranteed for any client's non-main traffic, so nothing needs a special case for "is this a title call". |
| `e78e4f9` | Discovery page's ~20s live-aggregation replaced with an incremental all-time rollup (`content_hash_stats` + `content_hash_sessions`, `internal/store/rollup.go`), maintained by the existing hourly sweeper (`cmd/arbiter/store.go`) rather than the request hot path — user chose "option 1, all-time, no sliding window" and explicitly rejected filling it on every request. Old `RepeatedContent`/`ContentHashCounts` kept for the JSON stats API. Same commit: ignored blocks hide by default (`show_ignored` opt-in query param, was `hide_ignored` opt-out) — an unchecked HTML checkbox submits nothing, so hiding had to be the absent-param default. Seen-block resurfacing on new activity needed no change (already keyed off `LastSeen` vs a stored mark, now fed by the rollup's `last_ts`). This was done under an explicit user override of the UI freeze below, scoped to Discovery only — the freeze still applies to #8 (see §6 — #59 and #63 have since ALSO been explicitly unfrozen and landed). |
| `69b087f` | #44's mechanism half, squashed from four commits at the user's request: matcher key renamed `kind:` → `request_kind:` (clean break, old spelling fails at load with an error naming both); `request_kind` settable on a **force alias only** (the three alias shapes are exclusive — pinned/group + request_kind is a load error, per the user's "EITHER pinned, OR a group, OR metadata" rule); stamped on turn 1 via `applyForceAlias` (overriding a classified kind) and re-stamped on turn 2+ by the affinity pin. Docs + example config shipped in the same commit. |

Earlier in this same continuous session (before the above): `d76f32e`,
`fe7aa33`, `5476af7`, `9fa3078` (Sessions page live-poll fixes + findings
file), and further back `46114c5` (#38 closed), `db64885` (test gaps),
`725d100` (status tracking), plus the larger structural cleanup pass
(commits `b94e05a` through `5ab3d02`) referenced in §6 of the findings file.
The 11-item cleanup-pass list is exhausted.

**Push state — run it, don't trust this file:** `git status -sb` / `git log
origin/develop..develop`. At last write `develop` was **ahead 9 of
`origin/develop`** (`...`, `540c3cf`) — pushing is the user's step. `git
fetch` may fail in this container (`cannot run ssh: No such file or
directory`); that's an environment limitation, not a signal the branches
diverged further than shown.

```bash
devenv shell --no-tui -- bash -c 'go build ./... && go vet ./... && gofmt -l cmd internal pkg && golangci-lint run ./...'
devenv shell --no-tui -- bash -c 'LITELLM_URL=http://localhost:4000/v1 go test -count=1 ./...'
```
Last known-good result: full gates green at `b603b74` (build/vet/gofmt all
clean); full suite green including `internal/config` (with `LITELLM_URL`
set — see §3, the var is required, not the test being flaky).

**#59 is still OPEN on the tracker despite `8345dff` landing its code** —
same shape as #44/Discovery below: verify the fix actually resolves the
user's complaint (page load, modal scroll) against a live deploy before
closing, don't close on code-complete alone. `gh issue view 59` to re-check
state.

**Not yet deployed:** the `affinity_pins` schema change (`prompt_hash`
column + composite PK) exists only in this repo's `schema.sql` + migration
code; the live DB (`/data/arbiter/arbiter.db`) migrates automatically the
next time the deployed binary opens it (the migration is idempotent and
runs on startup, not a manual step) — but that hasn't happened yet as of
this writing. Same for the rollup tables from `e78e4f9` below — still
undeployed as of last check; re-verify rather than assume either has
landed live.

## 6. Open work — READ THIS BEFORE PICKING ANYTHING UP

**#40 landed** (2026-09-30, `5c8f68b`) — `prompt_rewrite`'s `prefix`/`exact`
match modes anchor to the WHOLE searched field (byte 0 / entire string), so
they silently never match once anything precedes the target text, e.g. a
multi-source system prompt (Arbiter's own framing followed by a client's
injected preamble). Fixed by adding four new `types.MatchMode` values that
anchor to a smaller unit instead: `line_prefix`/`line_exact` (per
newline-delimited line) and `paragraph_prefix`/`paragraph_exact` (per
blank-line-delimited paragraph). Both `prompt_rewrite` (guardrail) and
`classifier.RequestMatcher` pick these up for free — both compile through
`types.NewTextMatcher`/validate against `types.ValidMatchModes`, no
per-consumer wiring needed. Existing `prefix`/`exact` behavior is
unchanged (regression tests pin this). Docs updated
(`docs/guardrails.md`'s mode table + a new explanatory paragraph). **Not
yet closed on the GitHub tracker** — close it once you're satisfied, or
say the word and I will.

**#75 is CLOSED** (2026-09-30) — safe field diagnostics for a
content-shape mismatch (object/number/boolean where a client should send a
string or array) landed in two commits: `acfbd69` for OpenAI (as the ticket
was originally filed) and `540c3cf` for Anthropic (added same-day once it
was noticed the ticket's OpenAI-only scope was an oversight, not a
decision — Anthropic's `messages[i].content`/`system` have the identical
wire ambiguity). Verified by unit tests asserting the diagnostic never
contains the submitted value/text; no live-deploy check needed, this is a
pure parse/logging change with no behavior change for valid requests.

**#18 is CLOSED** (2026-09-30) — cache-read/cache-write cost pricing
landed as `dde3912`, verified by unit tests, no live-deploy check needed
per the issue's own acceptance bar (see the closing comment on GitHub).

**#70, #71, #72 are CLOSED** (2026-09-29, this session). **#69 (the parent)
is still OPEN** — it was retitled/rewritten mid-investigation to hold the
overall design + phase table; close it once its own acceptance criteria are
confirmed against live traffic (see #69's "Acceptance" section: a session
issuing a title call and a subagent call must not have the main thread's
later turns served by a pin recorded under `title`/`subagent`, and no
second classifier call should appear purely because the session's prompt
family alternated) — that needs a deploy + real traffic, not just the test
suite. Don't close #69 on code-complete alone.

**#68** ("[feature] subagent session pin reset after x turns") is still
open and adjacent to this work — read it fresh before touching, its
relationship to #69/#70 was not resolved this session.

**Two contradictory comments in `arbiter.yaml` were flagged during the
investigation but NOT corrected in the shipped commits** — worth fixing in
a small follow-up:
- `arbiter.yaml:200` ("a title call never pins to a session... its session
  key hashes the conversation being titled, which differs every call") —
  this was true of the OLD derived-key-only world; with the header present
  (the common case, Hermes always sends one) a title call now DOES get its
  own real pin slot (via `prompt_hash`), it just never collides with the
  main thread's. Comment is stale, not fixed by `dec5f70`/`0885d1e`.
- `arbiter.yaml:499-505` ("the same header... makes the title row's own
  session_key equal to its parent's, an exact indexed lookup") — this part
  is accurate for grouping (`requests.session_key`), but doesn't mention
  `affinity_pins` now needing `prompt_hash` too. Neither comment was
  touched because #70/#71 stayed scoped to code + the `session_affinity`
  block's own comments (lines 507-517) rather than the title-classifier
  comment block up at line ~200.

**FIXED** — both comments corrected during the `arbiter.yaml` →
`arbiter.example.yaml` rename (2026-09-29): the title-classifier block now
says a title call gets its own real affinity-pin slot via `prompt_hash` and
never collides with the main thread's, and the `session_affinity` block now
explicitly calls out that `affinity_pins` keys on `prompt_hash` in addition
to `session_key`.

**UI work is FROZEN**, with two exceptions explicitly landed: the user
authorized the Discovery-page rollup + hide-ignored-by-default work
(`e78e4f9`) and, separately (2026-09-29), **#59** (per-request preamble
button/modal + the page-load perf fix, `8345dff`) — both scoped overrides
because each was blocking real investigation (Discovery's ~20s load;
#59's own "page load times still way too high" complaint). Neither
override extends to other UI tickets — user's words on the freeze itself:
*"no more ui fix things until the functionality below it is stable... i
want to track down the functionality properly, so when we get back to the
UI, we have what we need."* Do not resume #8, or any other UI ticket
until told otherwise. (#59 and #63 have since been explicitly unfrozen and
landed — see below.)

**#59's own follow-on work was deferred to a new ticket, then landed and
closed this session:** while diagnosing #59's page-load complaint, the user
asked "would lazy loading not help?" for the transcript's broken pagination
(load-next-100's rows weren't selectable; a `?seq=N` deep link couldn't
scroll up — both root-caused to the same "append-only, list/inspector split
that only agrees on page 1" design). Filed as **#73**
(https://github.com/cnf/arbiter/issues/73), scoped as its own piece, then
**implemented and CLOSED as `b603b74`** — see §5 for the full mechanism.
Item 3 of the original #73 scoping (search/filter over a partially-loaded
window) was explicitly left undone; see the "#73 search/filter" entry below.

**A `.bench/` scratch pattern was used this session to get real
end-to-end timing** without touching the live instance: copy
`/data/arbiter/arbiter.{db,db-shm,db-wal}` into a gitignored scratch dir,
`git worktree add` the commits being compared, build each as its own
binary, run each on its own high port (`-port 188xx -bind 127.0.0.1`)
against its own config pointing at the DB copy, time with a plain Python
`urllib` loop (no `curl`/`httpie` timing flags reliably available in this
container — `curl` isn't even installed). Clean up worktrees + `.bench/`
afterward; nothing here is meant to persist. Reuse this pattern rather
than reasoning about performance from synthetic in-process benchmarks
alone — a synthetic "2x faster" batching benchmark earlier this session
was correctly called out by the user as not proving anything about real
page load, and it didn't: real end-to-end measurement showed 1.32s→0.82s,
not 2x.


**#44 is CLOSED** (user's explicit go, 2026-09-28, after `69b087f` landed the
mechanism). The code mechanism is complete, and the deployed config already
carries the new spelling — `/data/arbiter/arbiter.yaml` has `request_kind:`
on both matchers and the `subagent: force: {} + request_kind: "subagent"`
alias (verified by loading a copy of it through the real loader). **The
remaining step is the user's: reload the service and send real subagent
traffic.** End-to-end verification against real traffic has NOT happened
yet — if a future session touches anything on this path, first check whether
that verification has since produced `request_kind='subagent'` rows (query
the store read-only); if it hasn't and something is wrong, that is where to
look — do not re-litigate the closed design.

**#26 was split on the user's request (2026-09-28):** the catalog and
empirical-cost halves became **#66** (Fetchable-URL catalog) and **#67**
(empirical cost/latency, labeled UI); #26 now holds only the subagent
attribution half, rewritten to reflect what's done. Duplicates **#64/#65**
were created by broken `gh` calls and are CLOSED — do not reopen them.

**Discovery rollup (`e78e4f9`) needs the user's deploy step to take effect**
— same shape as #44: code is done, but `/data/arbiter/arbiter.db` doesn't
have `content_hash_stats` populated until the user deploys this commit and
the hourly sweeper runs (or a manual backfill trigger, if one gets added —
none exists yet). Not a ticket, just don't assume live Discovery reflects
this change until you've checked.

**Other open items, not currently being worked (UI frozen, so these wait
too):**
- **`MAX(ts)` bare-column bug** in `ListRequests` (`internal/store/reader.go`)
  — an unqualified `MAX(ts)` in a `GROUP BY` hits SQLite's bare-column
  special case, so `provider`/`model`/`alias_used`/`status_code`/
  `request_kind`/`stream` on a lane's summary row come from an *arbitrary*
  row in the group, not the actual latest one. Found while investigating
  #44/#8; not yet fixed. Likely correctness-critical for anything the lane
  view shows.
- **`arrival_ts` backfill** — deliberately deferred by the user: *"no, im
  going to hold off on that until i know everything else works. dont want to
  sit there wondering why things ar enot working, and having 2 reasons it
  could be."* Do not start this until the subagent mechanism is settled.
- **#8** (rows sort by finish time, not arrival) — open, UI-frozen, not
  started.
- **#63** (tool-def display) — CLOSED, landed as `59a0b7d`: tool
  definitions render as a collapsed "tools offered (N, size)" section in
  the transcript inspector, size in bytes/SI (no real tokenizer exists).
  This was the last open item on the pre-freeze UI backlog.
- **#59** — landed as `8345dff` this session (per-request preamble
  button/modal + page-load perf fix); still OPEN on the tracker pending
  live-deploy verification, see §5. Do NOT re-open the design debate.
- **#73** (transcript pagination was broken — loaded-more rows unselectable,
  deep-link couldn't scroll up) — CLOSED, landed as `b603b74`: bidirectional
  lazy-load (SessionClientPageBefore + centered ?seq= jump + shared
  inspector-shipping fragments + IntersectionObserver auto-fill), verified
  live against the repo's own preview seeders, not just `go test`. Item 3
  from the original scoping (search/filter over a partially-loaded window)
  was deliberately left out — the user wants to rethink search as its own
  piece, not a mechanical follow-on; do not start it without a fresh
  scoping conversation.
- **#62** (`RateLimit` captured but drives nothing) — filed, undecided,
  low priority.
- **#68** (subagent session pin reset after N turns) — open, not started,
  relationship to #69 unresolved (see above).
- **#22** (attachment filename on content_refs) — CLOSED, landed as
  `40c4c1a`: see §5. Storage-layer only, no UI display.
- **#18** (cache-read/cache-write cost pricing) — CLOSED, landed as
  `dde3912`: see §5.
- **#66, #67, #51, #41, #40, #36, #24, #23, #21, #20, #17, #14, #4**
  — feature/deferred, bug, or `priority:low`/meta items, untouched this
  session. Run `gh issue list` for the live state, don't trust this list's
  staleness.


## 7. Landmines

- **`IntersectionObserver`'s `root` option must be the actual scrolling
  ancestor of the observed element, not the viewport (`root: null`), even
  when `rootMargin` looks like it should cover the gap.** `rootMargin` only
  expands the *given* root's own box — it does not "see through" an
  intermediate `overflow: auto` ancestor the element is clipped by. On the
  transcript page `.list-pane` scrolls (`overflow-y: auto`, `app.css:662`),
  not `#list` or the viewport; a load-more button just past `.list-pane`'s
  edge was reported non-intersecting with `root: null` + a 200px margin.
  Verified empirically with Playwright before trusting it, not from
  intuition — this is genuinely non-obvious spec behavior. If auto-loading
  silently doesn't fire on a scrollable inner pane anywhere else in this
  codebase, check `root` first.
- **A page's own inline copy of markup that's "supposed to" match a shared
  partial can silently diverge and cause an infinite request loop.**
  `session.html` had a hand-copied "load more" button predating the
  self-replacing (`hx-swap=outerHTML`) refactor of `transcriptList.html`'s
  `transcript-more` fragment; the page's copy still said
  `hx-target="#list" hx-swap="beforeend"`, so every htmx response appended a
  *new* button beside the untouched stale original — the same offset got
  re-fetched forever (1370+ rows before it was caught). `go test` didn't
  catch it because Go's `html/template` tests assert on one render path at a
  time and don't execute the runtime htmx swap loop. Fix pattern: extract a
  single shared control template (`transcript-more-control`) used by both
  the full-page render and every fragment, so there is exactly one
  definition, and add a Go test asserting `hx-target`/`hx-swap` match in
  both render paths (`TestTranscriptLoadMoreControlReplacesItself`). If a
  future bug looks like "the same htmx swap keeps re-firing with stale
  data", suspect a duplicated hand-copy of a partial before anything else.
- **`/homeless-shelter` can accumulate in the container and breaks every
  `devenv shell` invocation** with `home directory "/homeless-shelter"
  exists; please remove it to assure purity of builds without sandboxing`.
  Fix: `rm -rf /homeless-shelter` (needs approval — it's a root-path
  delete), then retry. Seen twice now; not project-specific, just a
  container quirk to recognize immediately rather than debug as a devenv
  config problem.
- **Playwright's bundled Node driver doesn't work out of the box in this
  container** — `python3 -m playwright install chromium --with-deps` fails
  to fetch a browser, and even after getting one via nix, Playwright's own
  vendored `driver/node` binary in the venv is broken/incompatible. Working
  recipe: `devenv --option packages:pkgs "nodejs chromium" shell --no-tui`,
  `pip install playwright`, then overwrite the venv's
  `playwright/driver/node` with the nix-provided `node` binary (`cp $(which
  node) <venv>/lib/python3.14/site-packages/playwright/driver/node`), launch
  chromium with `executable_path=<nix chromium path>` (find via `which
  chromium` in the same shell — the store path changes per generation,
  don't hardcode it) and `args=["--no-sandbox"]`. This is real browser
  verification, not a Go-test substitute — reach for it when a bug is only
  visible at runtime (JS execution, htmx swaps, CSS layout), same as this
  session's two landmines above. Clean up any scratch scripts/binaries
  afterward — none of this belongs in the repo.
- **`contentFor` (singular) and `ContentForRequests` (batched, plural) now
  answer DIFFERENT questions on purpose — don't unify them.** `contentFor`
  backs `ContentForRequest`, used by the public `/admin/request/{id}` JSON
  endpoint and `lanePreview`: it returns every captured block for an owner,
  unfiltered, because those callers need the full set. `ContentForRequests`
  has exactly one caller (the transcript page) and pushes "only the newest
  resent message per direction, or any role=system row" into its SQL — the
  same filter `newestRequestMessage` already applied in Go, just earlier,
  before the content body JOIN runs instead of after. The first
  implementation attempt wrongly pushed that filter into the SHARED
  `contentFor`, which broke `TestContentIsReassembledInOrder` and
  `TestToolDefinitionsRoundTripThroughStore` by silently dropping blocks
  those callers needed — caught by running the full suite, not by the two
  new store tests (which only exercise the batched path). If you touch
  either function, run `go test ./internal/store/...` in full, not just the
  tests that look adjacent.
- **A stateless chat API resends the whole prior conversation on every
  request, so `content_refs` holds one row per resent message per turn —
  most of a session's rows are old messages restated by a later turn, never
  actually displayed.** Confirmed on a real 100-turn production page: 26,301
  rows / 53MB of body text fetched to keep 315 (~1.2%), before this
  session's SQL push-down fix. Any future query over `content_refs` for
  display purposes should ask "do I actually need every resent copy, or
  just the newest one" before assuming a full scan is required — it usually
  isn't.
- **`search_files`/`Grep` can silently return 0 matches for a pattern plain
  shell `grep -rn` finds, on this container's backend** (seen again this
  session on a Go symbol lookup: `grep -rn` needed `--include=*.go` dropped
  and a plain path, since the bundled busybox grep rejects GNU-only flags
  like `--include`/`--exclude-dir`). Re-check any surprising 0-match result
  with plain `grep -rln`/`grep -rn` before concluding a symbol is absent.
- **A synthetic in-process benchmark result ("2x faster") does not describe
  real page-load time and should never be reported as if it does.** This
  session initially reported store-level batching as "2x faster" from an
  isolated Go benchmark; the user correctly asked "compared to what?" —
  real end-to-end load time had gotten WORSE at that point (2s→3-4s) despite
  the synthetic win, because the actual bottleneck (content over-fetch) was
  still unmeasured. Only trust an end-to-end number measured against a real
  request (see the `.bench/` pattern in §6) for a page-load claim.
- **`affinity_pins` now has a composite primary key `(session_key,
  prompt_hash)`, not `session_key` alone.** Any future code that reads or
  writes that table directly (not through `store/affinity.go`'s
  `SavePin`/`LoadPin`/`DeletePin`) must supply both. `session_key` in
  `affinity_pins` is still the same raw `X-Session-Id` value used in
  `requests.session_key` for grouping — it was NOT changed or mangled; only
  a sibling `prompt_hash` column was added. Don't assume the two tables'
  `session_key` columns mean different things now — they don't.
- **The pin's `prompt_hash` is computed from `req.SystemPrompt` BEFORE
  pre-guardrails run**, same timing rule as the existing derived
  `SessionKey` fallback (`docs/clients.md`'s "computed before
  pre-guardrails" point applies to both now). Hashing after a guardrail
  injected its own text would mix Arbiter's own prompt into the key and
  invalidate every pin the moment that guardrail's text changes.
- **`session_affinity.no_pin` is validated against a computed set of known
  `request_kind` strings** (`knownRequestKinds` in `internal/config/config.go`,
  collected from every alias's `request_kind` plus every classifier's
  declared `request_kind`) — NOT a hardcoded enum. Adding a new alias or
  classifier with a new `request_kind` automatically makes that value valid
  for `no_pin` in the same config; don't go looking for an enum to extend.
- **`Pipeline` gained a `SetNoPin([]string)` setter, following the existing
  `SetCaptureContent`/`SetConfigEpoch` pattern** — wiring-time policy is
  injected after construction, not threaded through `NewPipeline`'s
  constructor (already 17 positional args before this). If you need to add
  more config-driven pipeline behavior, use a setter, not another
  constructor parameter.
- **`content_hash_stats.sessions`/`.requests` only ever go UP — the rollup
  does not decay when `content_ttl` sweeps old `content_refs`.** Confirmed
  this session: a from-scratch live aggregation over the current DB agreed
  exactly with the rollup right after backfill, but that's only true because
  the backfill happened *after* the user lowered `content_ttl`. As TTL keeps
  trimming old content going forward, the rollup's counts will drift
  *higher* than what a fresh live query would show for the same window,
  monotonically, forever — there is no compensating decrease anywhere in the
  design. Accepted by the user for now (drill-down re-queries exactly,
  approximate counts are fine) but worth remembering if a number looks
  implausibly large in a few weeks: check whether it's genuine traffic or
  accumulated drift before treating it as a bug.
- **The Discovery "hide ignored" checkbox is inverted from its old form —
  it's now `show_ignored` (opt-in), not `hide_ignored` (opt-out) — because
  an unchecked HTML checkbox submits nothing at all, so "absent" had to mean
  the safe default (hidden).** If you're adding another boolean toggle to
  this page, use the same opt-in-when-checked pattern, not the mirror image;
  an opt-out toggle can never be un-defaulted from an unchecked box.
- **Don't infer current behavior from `build/livecheck.db`.** It's dev
  history — things tried, reverted, changed — not a snapshot of what current
  code does. A prior session got the #44 mechanism wrong once by reasoning
  from two different eras of data in that DB as if they were simultaneous
  facts. Verify against code + direct test execution instead.
- **`gh` needs devenv**, and **`gh issue create --body` with backticks/
  quotes in the body mangles it** (the shell eats them — #64/#65 were created
  with garbage bodies this way). Use `--body-file` with an absolute path —
  same applies to `gh issue close --comment`/`gh issue edit --body`, write
  the text to a temp file first if it has apostrophes or backticks. There is
  **no `UI` label** in the repo (or there wasn't; it was created 2026-09-28
  for #67 — check `gh label list` rather than assuming).
- **`git commit -m "..."` with apostrophes in the message breaks shell
  quoting (exit 2).** Write the message to a temp file and use
  `git commit -F <file>` instead.
- **The pre-commit hook needs `go` on PATH and fails outside devenv (exit
  1).** Run `git commit` itself inside `devenv shell --no-tui -- bash -c
  '...'`, not just the build/test/lint gates before it.
- **`request_kind` on an alias is FORCE-ONLY — the shapes are exclusive,
  enforced at config load.** A pinned/group alias + `request_kind` is a
  rejected config ("a pinned alias only says where the request goes, so it
  must not also say what the request is"). The user's rule verbatim: *"it's
  EITHER pinned to a model, OR a group, OR it gets metadata attached"* — do
  not relax this into "allowed on any shape" again; the first implementation
  did and the user rejected it as mixing concerns.
- **`force:` is `map[string][]string` (axis → value list).** A scalar under
  `force:` dies at YAML decode (`cannot unmarshal !!str ... into []string`)
  before validation can explain anything. `request_kind` is a SIBLING of
  `force:`, not a key inside it.
- **The matcher's request-kind key is `request_kind:` everywhere now**
  (classifier `match:` and router `when:`). A config still spelling `kind:`
  fails at load with an error naming both keys. Clean break, user-decided.
- **Stamp sites for alias-declared `request_kind`:** `applyForceAlias` (turn
  1, overrides a classified kind) and the affinity pin (turn 2+ — the pin's
  requested model IS the alias name). A stamp at only turn 1 "works once
  then silently vanishes" — the failure shape `alias_kind_test.go` exists to
  pin. The pinned/group short-circuit stamp site is now unreachable from
  config (request_kind can't be declared there) but stays in the plumbing;
  don't delete it as dead code.
- **A stale local file rewrite looks identical to a legitimate in-progress
  edit — check `git diff HEAD -- <file>` and ask before overwriting either
  way.** A prior session `git checkout HEAD --`'d the user's own uncommitted,
  freshly-cleaned-up `PICKUP.md`, destroying real work that was never staged
  and is unrecoverable (confirmed via `git fsck --dangling`). The fix isn't
  "never touch it" — it's: diff first, and when a file already looks
  intentionally edited, ask before restoring anything.
- **`request_kind` is a plain freeform `string`, no enum, anywhere in the
  codebase** (`pkg/types/models.go:61`). Don't assume or invent a closed set
  of valid values.
- **`RequestKind` is deliberately NOT a routing axis** — no confidence, not
  in `types.KnownAxes`, not overridable via `Force`'s existing axis
  switch. It needs its own field wherever it's set, not a case bolted onto
  the axis machinery.
- Full list of older landmines (title-request session-key behavior,
  `content_refs` addressing scheme, git-stash-swallows-devenv.yaml, etc.)
  lives in the findings file and earlier issue threads — this file doesn't
  repeat them all; search `support/notes/refactor-findings-2026-09-27.md`
  and closed-issue comments when something looks like it might be a known
  trap.

## 8. Session id and recovery

This session: `20260929_170132_63c693` (#73 — implemented bidirectional
lazy-load pagination for the transcript admin UI: `SessionClientPageBefore`,
30-turn window, centered `?seq=` jump, shared inspector-shipping fragments,
`IntersectionObserver` auto-fill; found and fixed two bugs only visible in a
real browser — IntersectionObserver `root` must be the scrolling ancestor
not the viewport, and `session.html` carried a stale hand-copied button that
caused an infinite re-fetch loop; set up a throwaway Playwright+devenv
recipe to verify live, then tore it down; committed as `b603b74`, closed
#73 on the tracker with item 3/search explicitly deferred at the user's
request — "i want server-side, but not now... i want to rethink search").
Previous: `20260929_095119_fea606` (#59 — first attempt landed as
`e366348` with a design the user rejected (stale top-level button, wrong
place, N+1 regression); redesigned per-request per the user's own
"button + modal per turn" call; diagnosed a real modal-scroll bug and a
real page-load regression through 3 more commits; user called out the
change-stacking loop and a misleading "2x faster" claim; root-caused the
actual bottleneck — `content_refs` over-fetch from stateless-API resends,
not query count or payload size — measured it for real against a copy of
the live DB, fixed it, then squashed all 5 commits into `8345dff` on the
user's request. Also: filed #73 for the separately-scoped pagination
redesign the user asked to defer, not fold in). Before that:
`20260928_231902_c1e18e` (session-affinity root-cause
investigation → #69/#70/#71/#72 filed → #70, #71, #72 implemented, tested,
committed (`0885d1e`, `dec5f70`) and closed). Before that:
`20260928_170153_78f051` (Discovery rollup design + build +
hide-ignored-by-default; landed as `e78e4f9`). Before that:
`20260928_150016_d4b925` (the #44 `request_kind` work — design debate, three
user corrections, the squash to one commit). Before that:
`20260927_120906_ea11f1` (Sessions-page work, findings file).
Recover exact quotes/commands via `session_search(query='...', session_id=...)`
for anything this file doesn't carry in enough detail — e.g. the full
`#69` root-cause evidence table (specific request ids, timestamps), the
exact wording of the user's design decisions on `no_pin`'s default, this
session's exact benchmark numbers/queries for #59, or #73's Playwright
verification transcripts/screenshots.

**Last updated**: 2026-09-29, after #73 landed as `b603b74` and was closed
on the tracker with item 3 (search) explicitly deferred pending the user
rethinking search as its own piece. #59 landed as `8345dff` (squashed from
5 commits) and #73 was filed; #59 deliberately left open on the tracker
pending live-deploy verification (see §5/§6), same as #69/#44 below it.
