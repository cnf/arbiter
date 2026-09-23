# PICKUP.md — read this first

A new session should be able to start work from this file **plus the board**, without
reading the README, REQUIREMENTS.md, `feedback.md`, or git history end to end.

This file is a **map, not a source of truth**. Anything here that can drift (branch
state, issue state, row counts) is given as a *command to run*, not as a fact to
trust. The board is authoritative for work items; the code is authoritative for
behaviour.

Written 2026-09-20, at `develop` = `0327600`; §5 and §9 updated 2026-09-21 at
`develop` = `6ef15bf`; §5, §6, and new §10 updated 2026-09-21 at `develop` =
`3124670`; §6 and new §11 updated 2026-09-22 at `develop` = `f55d60d`; §6 and
new §12 updated 2026-09-22 at `develop` = `e796405`; #43 landed. §6 and
new §13 updated 2026-09-22 at `develop` = `84d917c`; #5 and #34 landed. §6 and
new §14 updated 2026-09-22 at `develop` = `f8a43d4`; #8 part 3 landed (part
1 still open). New §15 added 2026-09-22 at `develop` = `94ed761` — scoping
only, no code landed; #11 is the next target. New §16 added 2026-09-22e at
`develop` = `635644c` — #11's routing half shipped (grouping half still
open). New §17 added 2026-09-23 at `develop` = `fa41aa4` — #11's grouping
half piece 1 (the `ParentSessionForTitle` query) shipped; pieces 2 (UI) and 3
(degraded-mode signal) still open. New §18 added 2026-09-23 at `develop` =
`28aa378` — piece 2 (nest a title line under its resolved parent in the UI)
also shipped; only piece 3 remains. New §19 added 2026-09-23 at `develop` =
`ee71cb4` — piece 3 shipped; **#11 is fully closed**. New §20 added
2026-09-23 at `develop` = `0fc2617` — #11 closed on the board; README.md and
the sample `arbiter.yaml` caught up to piece 1-3's doc gap; a ticket-less
startup version-logging feature shipped. New §21 added 2026-09-23e at
`develop` = `b8907e8` — #45 (admin UI rebuild) scoped and split into 5 native
GitHub sub-issues (#46-#50); #46 (phase 1: tokens/layout/popover) shipped and
closed. Run `git log --oneline -1` for the truth.

---

## 1. Orient — run these three, don't read files

```bash
git status -sb               # branch, ahead/behind, dirty files
git log --oneline -12        # what landed recently (commit bodies carry the "why")
gh issue list --state open --limit 40
```

`git` works directly. **`gh` needs devenv** (see §3) — the host has no `gh`. The board
is `github.com/cnf/arbiter`.

**Do not trust** `README.md`, `REQUIREMENTS.md`, `feedback.md`, or any status/plan
document for *current* state. They are accurate about design intent and drift on
"what is done". `feedback.md` in particular is a historical session log, partly
superseded — much of its "Open" section is now filed as issues. Nothing in this repo
records a formal handoff; the board plus commit bodies are the continuity.

---

## 2. What this project is

Arbiter — a from-scratch Go LLM proxy/gateway. Single binary, SQLite event store,
web UI for inspecting traffic.

**Its one job, in the user's words:** *"the point is to see what is happening"* and
*"nothing should be invisible. if it happened, it should be shown."* That is the
priority lens for judging any change here — a request, a classifier call, a failure,
and a rejection are all *requests* and all should leave a row.

**Scale, and why it matters for design calls:** single-user deployment. One user,
single-digit concurrency at worst, ~1 client at a time. Real clients are **Hermes**
and **opencode** — neither sends session metadata (no `session_id`, no
`prompt_cache_key`). Claude Desktop runs in 3p mode against LiteLLM, *not* Arbiter.
Collision/sharding/guardrail machinery sized for multi-tenant traffic is pure loss
here — say so when you see it proposed, including by yourself.

---

## 3. Environment — non-negotiable

- **The host has no Go toolchain and no C compiler.** Every build, test, lint,
  format and commit runs inside devenv:

  ```bash
  devenv shell --no-tui -- bash -c '<cmd>'
  ```

  Use `--no-tui`: plain `devenv shell -- cmd` renders a progress TUI that buries real
  errors in rendering noise. The git hooks run either way — `--no-tui` is about
  readable output.
- **Secrets:** commands that touch secretspec need `SECRETSPEC_REASON="..."`. Do not
  open `.secrets/`, `devenv.yaml`'s secret config, or any key file to obtain a
  credential — ask instead.
- **One test needs an env var.** `TestShippedArbiterYAMLLoads` fails on a bare
  `go test ./...` with `provider "litellm": missing endpoint`, because the shipped
  `arbiter.yaml` interpolates `$LITELLM_URL`. This is **pre-existing and not your
  change**. Either set it or expect exactly this one failure:

  ```bash
  devenv shell --no-tui -- bash -c 'LITELLM_URL=http://localhost:4000/v1 go test ./...'
  ```
- **Git has no identity in this container.** Set it repo-local (never `--global`)
  from the author already in the log:

  ```bash
  git config user.name  "$(git log -1 --format=%an)"
  git config user.email "$(git log -1 --format=%ae)"
  ```
- **Do not `git stash push -u`.** It swallows `devenv.yaml`, which breaks devenv
  entirely (`File devenv.nix does not exist`). It has cost a session once already.

- use the Codebase Search MCP to read/search through source files.

### Hard boundaries

- **Work only inside this repo.** Never write to `/tmp`, `~/.config`, or anywhere else
  on the host. Scratch files (commit messages, SQL probes, issue bodies) go in the repo
  — `build/` is gitignored and is the usual home.
- **Never touch `/data/arbiter`.** That is the **live** instance's config and store,
  outside the repo. Reads are fine and useful — open the DB read-only:
  `sqlite3 "file:/data/arbiter/arbiter.db?mode=ro"`. Never write to it, never restart
  anything.
- **Never start, restart, reload, or send traffic through a running instance.**
  Reaching a live process or a real upstream requires asking first.

---

## 4. How work is done here

- **Start only on an explicit go.** The user plans across parallel threads and most
  ideas are thought experiments. "Let's design X" is exploration. Wait for "let's
  start".
- **Tight scope.** The user says so explicitly ("move phase 7 to the front, nothing
  else"). Do not add polish, refactors, or adjacent fixes that weren't asked for.
- **Separately shippable phases**, not one big commit. Commit bodies carry the *why* —
  write them properly, they are how the next session recovers intent without you.
- **A test that passes with the feature deleted proves nothing.** Before reporting
  done, revert the fix and confirm the test fails *for the right reason*. A failure
  that blames the wrong component is worse than no test. (This trap has bitten here
  at least twice — see #15, #29.)
- **Enumerate every reader before widening a config field.** A classifier's `config:`
  is an untyped map read in `internal/config` (validation), `cmd/arbiter` (construction)
  and every test that builds the old shape. Missing one is silent: validation accepts
  what the builder drops, or the builder handles what validation rejects.
- **Check the data before the code.** Every wrong conclusion recorded in this repo's
  history came from reasoning about a component in isolation. The findings that held
  up came from querying the live store and driving the real `Execute` path.

---

## 5. Verified state as of `6ef15bf`

Landed (all on `develop`, **unpushed** — `git status -sb` says how far):

| commit | what |
|---|---|
| `6ef15bf` | UI: a run of streamed turns folds into one line with a count (#25), and the live tail places a row into its line |
| `da32072` | PICKUP.md itself |
| `0327600` | test for the config-key→builder seam |
| `06c32e2` | classifiers read the **first** user turn with text, skip the upstream call entirely when there is nothing to classify, and cap the input via `max_input_chars` |
| `3dfe2d2` | `types.Ellipsize` extracted from `internal/pipeline` (pipeline imports classifier, so the classifier could not reach it) |
| `4369a52` | `Signals.RequestKind` + `kind:` in a classifier's `match:`; the literal-model path classifies a not-yet-in-a-session request **for the record only** — routing never moves |

Closed on the board: **#3**, **#30**, **#15**, and now **#25**, **#19**, **#16**, **#10**
(the last three were answered/verified from the store — see the closing comments, and §9
for #16's real finding).

Working tree is dirty with **pre-existing, deliberate** local changes — `.gitignore`,
`devenv.nix`, `devenv.yaml` (secretspec switched to the `file://` provider because a
container has no D-Bus session bus; Claude notification hooks disabled; `gh`/`sqlite`
added to packages). These are the user's environment adaptations, **not part of any
feature**. Leave them alone; stage only the files your own change touches.

**The classifier work is deployed and running.** `/data/arbiter/arbiter.yaml` has the
classifiers active (146 classifier rows in the store, newest minutes old) and carries
all three title patterns — verified by loading the deployment's config through the
real parser and compiling each pattern against the real prompts. **Also fixed** is
`request-kind` — its shape now uses `kind: "title"` with no axis, as it should.

**What is still unobserved: a non-NULL `request_kind`.** No row has one, and that is
expected rather than a defect — **zero title-gen requests have arrived since the
patterns were added** (newest is id 3865, 2026-09-20 20:00; ordinary traffic continues
past 2026-09-21 06:49). The label appears on the first title request after a client
starts a new session. Do not read the NULLs as a matching failure: check for a title
request *after* the config first, the way this file's §4 says to check the data before
and code.

**Consequence for #3/#30/#31:** their acceptance is still live confirmation, which needs
a fresh title request rather than a code change.

---

## 6. Open work, in priority order

Run `gh issue list --state open` for the live list. As of `b8907e8`
(2026-09-23e, §21), open: **#4, #8, #9, #14, #17, #18, #20, #21, #22,
#23, #24, #26, #36, #38, #40, #41, #44, #45, #47, #48, #49, #50**. Closed
since §5/§10 were last written: **#27, #28, #31, #37, #39, #13, #43, #42,
#5, #34, #11, #46** (see §11–§13, §20, §21).

**Admin UI rebuild (#45), in flight — see §21 for full detail:**
5 native GitHub sub-issues of #45, fixed build order (foundation must land
before anything that reuses its tokens/popover):
1. **#46 — foundation** (tokens, layout chrome, popover component) — **shipped, closed**, `b8907e8`.
2. **#47 — requests page** (routing chain, kind chips, parent/child threading) — next up, not started.
3. **#48 — session transcript** (rail layout, tool rendering, guardrail diff) — not started.
4. **#49 — sessions index** (search-first, fixed-width columns) — not started, lowest priority of the five.
5. **#50 — discovery** (dedupe by session, filter chips, UA clustering + visual pass) — not started, bundles backend work so it's deliberately last.

**High priority, real gaps:**
- **#4** — META umbrella for the visibility goal. **#5** and **#34** (its
  two biggest formerly-open children) are now closed (§13); **#8/#9** are
  still open children. **#8 is now half-done** — its part 3 (visible
  trace_id parent/child tie) shipped in `f8a43d4`; part 1 (sort by
  arrival/request time instead of finish time) is not started (§14).

**Labels worth filtering on:** `anthropic-client` marks every ticket touching
the Anthropic-facing wire interface specifically (currently #22, #23, #36,
#38) — useful when deciding what's in/out of scope for that surface, which the
user has said he doesn't use today (he does use the Anthropic **upstream**,
i.e. routing to Claude models — that's a different, unaffected path; see §10).

**Medium/low**, no change in status: #8 (see above — half done), #9,
#14, #17, #18, #20, #21, #22, #23, #24, #26, #36, #38, #40, #41, #44.

**#17** — Phase D (`min_confidence`) is now *unblocked* (its blocker #6
closed, see §10) but still **unbuilt** — don't confuse unblocked with done.
Phases E (`score` primitive) and F (subagent detection) also unbuilt.

---

## 7. Landmines

- **`affinity.get` vs `affinity.pinned`** — see above. Using the wrong one causes
  per-turn re-classification that looks like a config problem.
- **A title-gen request never pins**, by construction: its session key hashes the
  system prompt plus the first user message, and a title prompt's user message is
  the **conversation being titled**, which differs every call. So it classifies on
  **every** call. That is correct, not a dedup bug — 8/8 measured in distinct
  sessions. #30's acceptance line about "row count stops tracking request count"
  reads the other way for title requests.
- **...which is why the `request-kind` match must be declared FIRST and
  `decisive`.** A title call that is identified pays for no classification call at
  all; the expensive thing is not the extra row, it is the model call on every
  title request. See §9 for the real prompts to match against.
- **`content` is content-addressed** via `content_refs` (`owner_kind` ∈
  `{request, rejected}`, keyed by `hash` — not a `content_hash` column, and there is
  no `block_index`; it is `direction, msg_index, position`). To ask "was this text
  stored", join through `content_refs`, don't read `content.text` directly.
- **`kind` vs `request_kind`** — `kind` says *who sent it* (`client` | `classifier`);
  `request_kind` says *what it is* (`title`, later `subagent`). Do not overload
  `kind`.
- **`RequestKind` is not an axis.** Not in `KnownAxes`, no confidence, no force-alias
  target. A request's kind is a fact no rule should match on.
- **Multi-line SQL through `devenv shell` collapses newlines.** Write the query to a
  file in the repo and pipe it: `sqlite3 "file:...?mode=ro" < tmp_q.sql`. Delete the
  file afterwards — keep `git status` clean.
- **`execute_code` does not work in this environment** (no Python in the terminal
  container). Use `patch` / `write_file` / `terminal`.
- **`build/` is gitignored** — a good place for commit-message and issue-body files
  you stage nothing from.
- **Commit with a message file**, not `-m`, for anything with a real body:
  `git commit -F build/commit-msg.txt`.

---

## 8. Querying the live store (read-only, and useful)

The single highest-value investigative tool here. Read-only URI, from the repo root
(devenv needs `devenv.nix`, so never `cd` into `/data/arbiter`):

```bash
devenv shell --no-tui -- bash -c 'sqlite3 "file:/data/arbiter/arbiter.db?mode=ro" "<SQL>"'
```

Useful starting points:

```sql
.schema requests            -- note: status_code + error, NOT `status`
SELECT kind, request_kind, count(*) FROM requests GROUP BY 1,2;
SELECT id, ts, kind, request_kind, domain, confidence,
       substr(routing_rationale,1,90)
  FROM requests ORDER BY id DESC LIMIT 30;
```

`requests` has **no `status` column** (it is `status_code` + `error`) — check `.schema`
rather than assuming, and prefer small `LIMIT`ed probes over broad scans. Old rows may
predate the newest columns, so a `NULL` there means "written before the column existed",
not "unset".

**Content search is slow and needs the right column.** `content.body` is a **BLOB**, so
cast it (`cast(body AS TEXT)`) and always scope the query — a `LIKE` across the whole
`content` table (11.9k rows, 1.6M refs) times out. Scope by joining `content_refs` on a
small id set first. `content_refs` carries `role`/`block_type` denormalized precisely so
it can be filtered before the blob is read.

---

## 9. Title-generation signatures — the real prompts

Read out of the live store, not invented. Each is the opening of that client's
system prompt; the store holds the full text.

| client | signature (system prompt) | position | user-agent |
|---|---|---|---|
| Hermes | `You name chat sessions. Given the user's opening message, write a title that lets them find this conversation again in a list.` | start of block | — |
| opencode | `You are a title generator. You output ONLY a thread title. Nothing else.` | start of block | `opencode/...`, `opencode/1.15.10` |
| Claude Code CLI | `Generate a concise, sentence-case title (3-7 words) that captures the main topic or goal of this coding session.` | **~165 bytes in** | `claude-cli/2.1.223` |

**Claude Code CLI's prompt opens with `x-anthropic-billing-header: ...` and `You are
Claude Code, Anthropic's official CLI for Claude.`** The title instruction is behind
both — which is why `prefix` mode cannot reach it.

**Modes, and the trap.** `exact | prefix | regex` (`pkg/types/textmatch.go`). `prefix`
means the text must **START** with the pattern (`Find`, `MatchPrefix`), it is *not*
"contains". `exact`/`prefix` are case-insensitive and whitespace-trimmed; **`regex` is
case-sensitive**, so a case-insensitive regex needs `(?i)`.

**A loose pattern here is silent and lands anywhere.** Measured on the 79 distinct
system prompts in the store:

| pattern | prompts matched |
|---|---|
| `(?i)title` | **50 of 79** — every Hermes agent prompt carries `title-generation grouping in the UI is deferred` in its memory text |
| `(?i)title generation` | **0** |
| the three patterns shipped in `arbiter.yaml` | **4** — exactly the right prompts, no false positives |

Neither extreme is visible in the UI (no reason is shown for a non-match). **Read the
corpus before writing a pattern**, and check a new one against it:

```sql
-- The inventory is small (79 rows at 2026-09-21) and cheap to eyeball.
SELECT length(cast(body AS TEXT)) AS len, cast(body AS TEXT)
  FROM content
 WHERE hash IN (SELECT hash FROM content_refs WHERE role='system' AND owner_kind='request');
```

`internal/config/title_signature_test.go` pins the shipped patterns: it loads
`arbiter.yaml` through the real parser and asserts each loads as the intended regex
(a single backslash in YAML double quotes loads fine and silently never matches),
compiles, hits its own prompt, and does **not** hit the Hermes agent prompt.

**Not yet observed live:** the deployment carries these same three patterns (verified by
loading `/data/arbiter/arbiter.yaml` through the real parser and compiling each one
against the real prompts), but no row has a non-NULL `request_kind` — because **zero
title-gen requests have arrived since the patterns were added**. Newest title request is
id 3865 (2026-09-20 20:00) while ordinary traffic runs past 2026-09-21 06:49. The label
appears on the first title request after a client starts a new session; a NULL column is
not evidence of a matching failure until that has happened.

## 10. Session 2026-09-21b — Anthropic empty-replies fix, then audit/park

**What shipped (6 commits, `develop`, unpushed — `7ac694c`..`3124670`):** fixed
#33, "Claude via the auto router returned empty replies". Two real causes:
`max_tokens` silently forced to 4096 for OpenAI-shaped clients (Hermes,
opencode) that don't send it, and thinking/tool-call stream deltas relayed as
empty chunks. **User confirmed live: Hermes → Arbiter → Anthropic upstream
works.** #33 is closed.

**Then found and fixed 3 more defects** while confirming: `data: null` frames
on the wire (nil marshalled), Anthropic SSE missing `event:` lines (Anthropic
SDKs dispatch on `sse.event`, so frames were silently discarded), and
`stop_reason`/block-stop index/content-array shape bugs. These are filed as
**#35** (consolidated, tagged `anthropic-client`), not shipped as fixes —
deliberately parked, see below.

**A `choices[].index` bug was found and filed separately as #36** (not #35):
`NormalizedToOpenAIStreamEvent` stamps `BlockIndex` onto `choices[].index`, so
a reply with reasoning on block 0 + text on block 1 emits `choices[0]` twice.
Independent of the anthropic-client work; filed on its own per the user's
rule ("if it's out of scope of #35, it needs a new ticket").

### The 4-part mental model, and why it matters for scoping

Every Anthropic-touching change sits in one of four parts:
**anthropic-client↔arbiter**, **anthropic-LLM(upstream)↔arbiter**,
**openai-client↔arbiter**, **openai-LLM(upstream)↔arbiter**. `format` in the
DB is the *client* side. **`internal/translator/stream.go` is the shared codec
for all four** — every commit in this session touched it, so cleanly
separating "did this touch anthropic-client vs anthropic-upstream" by file is
not possible; it has to be reasoned about function-by-function.

**Key finding from that reasoning:** the claude-cli 502 (`context canceled`)
comes from `readSSEStream` in `internal/upstream/stream.go:23` (error surfaces
at line 90) — **the anthropic-UPSTREAM socket reader, not a client-shaping
function.** Verified per-commit: zero of the six commits' hunks land inside
that function (`fa388bd`'s one hunk in that file is in `sendAnthropicStream`'s
header logic, a different function). Zero tests exercise it
(`httptest.NewServer` count is 0 in the repo).

**This is why parking #35/claude-cli does NOT put the Anthropic-upstream path
at risk.** Hermes and claude-cli hit the **exact same** `sendAnthropicStream`
→ `readSSEStream("anthropic", ...)` call when routed to a Claude model — the
route is decided by which model Arbiter targets, not which client asked. The
confirmed-working Hermes traffic proves that shared path is sound for the
request shape Hermes sends; only claude-cli's own request shape trips the
502. **User confirmed: he needs Anthropic-upstream, does not use Anthropic
clients today** — so #35 (the client-shaping defects) and the claude-cli 502
are both correctly parked, while nothing about Anthropic-upstream routing is
at risk.

### A false-positive audit finding, corrected — read before trusting any "dead code" claim

Audited the six commits for technical debt (user's ask: "was any code
introduced... because we were chasing this bug?"). Initially concluded
`NormalizedRequest.Thinking`/`OutputEffort` (added by `fa388bd`) were dead
fields nobody populated, based on `grep "Thinking =\|OutputEffort ="` finding
nothing. **This was wrong** — the fields are populated via struct-literal
syntax (`Thinking: req.Thinking,`), which uses `:` not `=`; grep for `=`
cannot see it. Verified by driving the real ingress path
(`DefaultTranslator.ToNormalized`) with a throwaway probe test: a real Claude
Code body survives parse → normalized → outbound wire intact. **No removal
was made.** Full retraction is on #33's thread.

**The actual lesson, worth keeping:** a grep for absence is not evidence of
absence. Verify "is this populated/dead" claims by driving the real entry
point (here, `ToNormalized`, not a hand-built struct), not by grepping for an
assignment operator that may not be the one used.

**Real (minor) debt found and left as a design note, not removed:**
`ClientBeta` in `internal/upstream/client.go` is one ad-hoc per-header field
carrying `anthropic-beta`; there's no general per-request-header-forwarding
mechanism, so a second header would need a second field. Noted on #33, not
acted on — small, not urgent.

### Ticket hygiene done this session

- Created label **`anthropic-client`**, applied to #22, #23, #35, #36 (NOT
  #34 — that's store-side tool-call recording, not the client interface).
- **Closed #33** (empty replies — fixed and confirmed), **#6** (Jev/System One
  — found to be already live in `/data/arbiter/arbiter.yaml`, not a proposal;
  the ticket's own NOT-STARTED framing was stale), **#12** (its two open
  questions are answered by shipped code in `classifyLiteral`, `pipeline.go:840`,
  via #30).
- Updated **#17**: Phase D unblocked now that #6 confirmed real confidences
  exist in production, but D/E/F remain unbuilt — don't confuse unblocked with
  done.
- `build/*.md` audit note: it's gitignored, so those write-ups exist only on
  disk, not in git. Comments were back-filled onto #8 (a `trace_id`/session-key
  observation) and #30 (a criterion-by-criterion acceptance walkthrough) that
  existed in `build/` but not in the tickets — check `build/*.md` against the
  board before assuming a note made it into a ticket.

### Before merging develop → main

Nothing above blocks a merge — tree is clean, tests green
(`devenv shell --no-tui -- bash -c 'go test ./...'`, `develop` ahead of
`origin/develop` by 15, nothing pushed yet). The six new commits are real
fixes plus test coverage, not scaffolding. Only outstanding non-blocking items:
`readSSEStream` still has zero test coverage (pre-existing gap, not
introduced this session), and the `ClientBeta` design note above.

---

## 11. Session 2026-09-22 — #31 verified closed, #42 filed, a config gap noted

**#31 closed, with live evidence, not just "the commit landed."** Queried the
store for traffic since the fix-bearing binary went live (built 21:21
2026-09-21, containing `06c32e2`): zero rows with an empty classifier input
(the `input ""` signature), zero recurrences of the old burst pattern
(12+ identical `"none of the options fit"` verdicts at 0.7–1.0 confidence,
seconds apart, one session). One post-deploy escape verdict exists (a real
context-compaction handoff, confidence 0.41) — that's the classifier working
correctly on genuinely ambiguous input, not the bug. Full evidence is on the
issue's closing comment.

**Also found while checking (not #31, don't conflate them):** one session made
19 separate decision-classifier calls in ~90 minutes, all correct
high-confidence verdicts on real content — but the session never pins, so
every retry re-classifies from scratch. This is `#11`'s territory (title/
subagent grouping never pinning), not a regression of #31's fix.

**#42 filed: `type: "llm"` classifiers have no config-load check that their
`alias:` resolves to a concrete model.** This is *why* `domain-llm` has sat
commented out in the deployed config — traced and reproduced, not guessed:

- `validateLLMClassifiers` (`internal/config/config.go:408`) only checks that
  `alias:` names *some* configured alias (`c.Aliases[alias]`) — never what kind.
- `AliasResolver.resolve()` (`internal/router/alias.go:113-119`) explicitly
  rejects a force-alias as a resolution target **at runtime**: `"alias %q is
  a force-alias and selects no provider/model"`.
- The natural alias to point a "let the classifier pick a model" `llm`
  classifier at (`llm-arbiter: { force: {} }`) is exactly a force-alias — so
  it validates cleanly at load time and only fails once a real request drives
  it.
- The `decisions` classifier type already has the right shape of check —
  `validateDecisionsAlias` (`config.go:627`) walks the alias (through group
  members) and requires the resolved provider to be of type `"decisions"`.
  `llm` needs the equivalent: walk-and-check that it resolves to a
  pinned/group alias, not a force-alias.
- Verified by loading the real deployed `arbiter.yaml` through `config.Load()`
  directly, with `domain-llm` uncommented and pointed at `llm-arbiter`:
  validation passes with no error. The failure is real but invisible until
  runtime.

**A related, more general gap surfaced by this (user's observation, not yet
filed as its own ticket):** there is no way in the config for a classifier's
`fallback:` — or anything else — to mean "give up and return an error" rather
than "fall through to another classifier." Every fallback chain terminates in
*something that produces an answer* (a heuristic, at minimum). That absence is
part of why classification loops/burst patterns like #31's are possible in
the first place: nothing in the config vocabulary can express "this input is
bad, stop and surface a failure" as a valid terminal outcome — the system is
structurally required to always answer. Worth a ticket of its own once scoped
(does "error" mean skip classification and let the request through
unclassified, or reject the request outright, or something else — needs the
user's design input before filing).

**Housekeeping:** `PICKUP.md` had a stale, truncated local rewrite in the
working tree (67 lines, missing this file's landmines/store-cookbook/session
sections, and factually wrong — claimed #28 open when it was closed). Restored
from `HEAD` (this file) and updated §6/§11 in place rather than continuing
from the truncated version. If a future session finds `PICKUP.md` unexpectedly
short again, `git checkout HEAD -- PICKUP.md` recovers the real one — check
`git diff HEAD -- PICKUP.md` before trusting an on-disk copy that looks thin.

---

## 12. Session 2026-09-22b — #43 shipped (both parts), PICKUP written

**#43 shipped, both parts, on `develop` (unpushed).** `git status -sb` says
how far ahead. The ticket ("Escape verdicts unmatchable + no config way to
stop/return an error") is CLOSED — shipped in the three commits below, closed on
2026-09-22 (see the board note under "Board as of this session"). Three commits,
in order:

| commit | what |
|---|---|
| `19005dd` | **#43 part 1**: escape verdicts fill their axis with a reserved `unmatched` sentinel — previously they filled nothing (empty string), so no `when:` rule could target them and merges silently lost them. Capabilities axis deliberately excluded (additive set, not a contested value). Merge precedence: real value beats `unmatched`; `unmatched` fills only an empty axis. |
| `31c801d` | **#43 part 2**: a `target: {stop: {error, message}}` rule target. New `StopError` type (`pkg/errors`) so `ChainedRouter` can't swallow it as a routing miss; `resolveRoute` passes it unwrapped (never into `RoutingError` → 500); `writeArbiterError` maps it to its configured status + message. Stop works in normal and catch-all positions. |
| `e796405` | **docs**: README routing section + example config show the stop target. |

**State for the next session:**

- `feedback.md` at repo root is **untracked, pre-existing, not mine** — leave
  it alone.
- PICKUP.md itself is **modified in the working tree** (this §12 + the §6 #42
  correction + header line). It is part of this handoff, not a feature — a
  fresh session edits/commits it as its own handoff.
- Last verification: `devenv shell --no-tui -- bash -c 'LITELLM_URL=http://localhost:4000/v1 go test ./...'` is **green**; `go vet ./...` clean; `gofmt -l` shows only three pre-existing unrelated files (`internal/config/title_signature_test.go`, `internal/ui/grouping_tail_test.go`, `internal/ui/grouping_test.go`). README/PICKUP changes need no Go checks.
- **#43's test discipline was followed** (see §4): before calling part 2 done I
  reverted the `resolveRoute` change and confirmed
  `TestExecutePropagatesStopErrorUnwrapped` fails *for the right reason* (the
  stop became a bare `RoutingError`); same for `TestChainedRouterDoesNotSwallowStop`.
- **One subtlety worth remembering:** `ArbiterError.Unwrap()` means
  `errors.As` can see a `StopError` *nested inside* a `RoutingError`. That's
  why the pipeline test asserts with a direct type assertion (`err.(*StopError)`),
  not `errors.As` — a wrapped stop would still pass `errors.As` and the test
  would prove nothing about unwrap-vs-wrap.

**Board as of this session** (`gh issue list --state open`): #43 was **open at the
time the PICKUP draft was written** — shipped but unclosed — and has since been
closed by the user's request (this session, closing comment + `gh issue close 43`,2026-09-22T18:08Z; verify with `gh issue view 43 --json state`). #42
is closed (by `4591e98`, commit title "Closes #42"). Still open — **#5**
(route failures / guardrail rejections get no requests row — the biggest
visibility hole,, still the top open gap; PICKUP §6 has the design pointer),
**#34** (streamed tool calls leave no record), **#41** ("full block" link
wired to wrong endpoint — no page shows an untruncated block body)...
**#40** (prompt_rewrite prefix/exact anchor to whole field — silent no-op in
multi-source prompts), **#38** (provider headers applied after per-request
headers, so static config overrides the client's `anthropic-beta`), **#36**
(OpenAI stream chunks stamp block index onto `choices[].index`),
**#26/#24/#21/#20/#18/#17/#14/#11/#9/#8/#4** (formerly "medium/low";
see §6 for the write-ups — nothing changed this session on those). §6's stale
#42 listing corrected above.

**README now documents** the policy rule targets: alias (`target: "…"`),
provider/model, or terminal refusal `target: {stop: {error, message}}` — with
the status/message-validation rule and that a stop short-circuits the whole
router chain (most useful in a catch-all position).

**This session also wrote a skill** (user-local, not in this repo):
`~/.hermes/skills/cnf/session-handoff/` encodes how to write a handoff doc —
distilled from PICKUP.md's own conventions. Not tracked in git. It will not
be loadable until a future session (hermes caches skill list at startup).

---

## 13. Session 2026-09-22c — #5 and #34 shipped, both closed on the board

**#5 and #34 both shipped, on `develop` (unpushed).** `git status -sb` says
how far ahead. Two commits, in order:

| commit | what |
|---|---|
| `2db3665` | **#5**: a pre-guardrail rejection and a routing failure (including a `stop` refusal) now get a real `requests` row instead of a content-only stub under the old `owner_kind="rejected"`. Both call sites build a full `store.Event` and go through the same `p.record(...)` path the upstream-failure case already used — copied, not redesigned. Status codes centralized into one `pkg/errors` helper shared by the HTTP layer and the pipeline (previously only `writeArbiterError` had the mapping — avoids recreating the `status_code=0` bug fixed earlier). Every client-facing error body now prefixed `arbiter: ` at the single `writeError` choke point. **Dead-code removal folded into this same commit** (user's explicit choice, not deferred): `recordRejected`/`rejectionID`/`RecordRejected`/`ContentRecorder` and the `owner_kind="rejected"` write path are gone from `pipeline.go`/`writer.go`; `schema.sql`'s comment documents `'rejected'` as historical-only, kept so pre-#5 rows still read. |
| `84d917c` | **#34**: a streamed tool call now leaves the same evidence the non-streaming path already produced. New `toolCallAccum` reassembles a tool call's id/name/args fragments beside the existing `orderedText` accumulator in `executeStream`'s per-event loop (same by-index concatenation an OpenAI client is already contractually required to do). `Event.ToolCalls` is now set on the streaming record call (`streamedToolCallNames`, same shape `toolCallNames` already produces for non-streaming — reader/UI need no changes). `capturedStreamBlocks` now also emits `tool_use` blocks, positioned past the text index range (`ToolCallIndex` is a distinct index space from `BlockIndex` on the OpenAI-origin wire, so using it as-is would collide with a text block's position). Malformed/truncated argument JSON degrades to a nil input rather than dropping the call. |

**Both issues closed on the GitHub board this session** (`gh issue close 5`,
`gh issue close 34`), each with a comment noting the fix is on `develop` and
not yet pushed to origin — the board says closed but the code is only in this
repo's local `develop` until someone pushes.

**State for the next session:**

- `feedback.md` at repo root is **untracked, pre-existing** — its #5 design
  sketch is now historical (the design it sketched shipped in `2db3665`, close
  enough to what was built that no rewrite is needed); leave the file alone.
- PICKUP.md itself is **modified in the working tree** (this §13 + the header
  line + §6 rewrite). Part of this handoff, not a feature — a fresh session
  commits it as its own handoff, same convention as every prior session here.
- Last verification (both commits): `devenv shell --no-tui -- bash -c
  'export LITELLM_URL=http://localhost:4000/v1; go build ./... && go vet
  ./... && go test ./... && golangci-lint run ./...'` all green; `gofmt -l`
  shows only the same three pre-existing unrelated files noted in §12.
- **§4's test discipline was followed for both.** #5: stashed the four
  production files, confirmed `capture_test.go`/`stop_test.go` failed for the
  old-behavior reason, popped the stash. #34: reverted each of the fix's three
  parts (fragment accumulation, `Event.ToolCalls` on the record call,
  `capturedStreamBlocks` emitting `tool_use`) one at a time and confirmed the
  new test (`stream_tool_calls_test.go`) failed for that part's own reason
  each time, then restored.
- **One design note worth keeping:** `capturedStreamBlocks`'s tool-call
  `Position` is deliberately `len(orderedText) + index`, not the raw
  `ToolCallIndex` — Anthropic's stream sets `ToolCallIndex == BlockIndex`
  (they're the same number), but on the OpenAI-origin wire path
  `ToolCallIndex` numbers parallel tool calls independently of `BlockIndex`
  (which is always 0 for a single candidate). Using it unoffset would have
  collided a tool-call block's position with a text block's.

**Board as of this session** (`gh issue list --state open --limit 40`): 17
open — **#4** (meta, still open — #8/#9 are its remaining open children now
that #5/#34 are closed), **#38, #36** (bugs, no priority label, no design
gate), **#9, #8** (medium, UI client column / sort order), **#14, #11**
(medium, design-gated), **#41, #40, #23, #22, #18** (low, bugs), **#26, #24,
#21, #20, #17** (low, design-gated features). No priority:high bugs remain
open other than the #4 tracker itself.

**Not done, and deliberately not started:** nothing was pushed to origin.
`develop` is 29 commits ahead of `origin/develop`, same as every session
before this one — pushing was never asked for and PICKUP has never
recommended it unprompted.

---

## 14. Session 2026-09-22c — #8 part 3 shipped (visible trace_id tie); part 1 still open

**#8 is a two-part ticket; only part 3 shipped this session.** One commit,
on `develop` (unpushed):

| commit | what |
|---|---|
| `f8a43d4` | **#8 part 3**: a classifier row now always renders immediately adjacent to the client request it belongs to (matched on `trace_id`), regardless of which one's `ts` sorts first. Presentation only — `ts`/`latency_ms`/the `ORDER BY`/keyset paging are untouched. New `attachTraceChildren` (`internal/ui/requests.go`) groups fetched rows by `trace_id` after the existing streamed-run folding and nests each non-client row under its parent client line as a `Children` entry. Orphaned classifiers (parent not on the page) stay top-level rather than being dropped. Matches against a folded run's own head, so a classifier tied to an older, now-collapsed turn still finds its parent. `reqrow.html`'s `req-line` renders a line's `Children` *before* its own `<tr>` — the page is newest-first top-to-bottom, and the parent is fixed as chronologically first regardless of actual `ts`, so it renders lower on the page (after its children in document order). Also bundled: default kind filter changed to `"all"` (both absent `?kind=` and the literal value now mean no filter) since that's the view used day to day; `filters.html`'s select reordered so "all" is first/default. |

**Not done: #8 part 1** (sort by arrival/request time instead of finish
time) — deferred at the user's request ("do #3 first, then reevaluate"),
not started. The ticket's own landmine still applies: `ts` is TEXT and
keyset paging is `(ts, id) < (?, ?)`, so that part is a query/cursor-layer
change in both `ListRequests` (`internal/store/reader.go`) and
`ListRequestsAfter` (`internal/store/live.go`) — deliberately separate query
sites, not just a template edit. **#8 was left OPEN on the board**, with a
comment on the issue recording what part 3 shipped and that part 1 remains
(`gh issue comment 8`, not `gh issue close`) — this is the first ticket in
this repo's history updated-but-not-closed; don't read "commented" as
"closed" for #8 specifically.

**A real bug was caught and fixed mid-session, worth remembering as a
pattern:** the first cut of the `reqrow.html` change put a line's `Children`
*after* its own `<tr>` in document order, which — because this page is
newest-first top-to-bottom — rendered the parent row *above* its classifier
child, i.e. visually **as if the cause were later than the effect**, exactly
the bug #8 exists to fix. The user caught it by reasoning through the
top-to-bottom chronology explicitly ("chronology is bottom to top... you put
newer next to the parent... but the parent can not be newer than the
child"). Fixed by moving the `{{range .Children}}` call to before the
`<tr>`. A dedicated regression test
(`TestRequestListRendersClassifierAboveItsParent`, `internal/ui/ui_test.go`)
was added and verified against both orderings — fails with the user's exact
symptom when the old (broken) render order is restored, passes with the fix.
**Lesson for future page-ordering work in this UI:** "renders first in
markup" and "appears first chronologically" are opposite claims whenever the
page sorts newest-first — say explicitly which one is meant, in code
comments and in conversation, or re-derive the actual on-screen row order
before asserting it.

**State for the next session:**

- `feedback.md` at repo root is **untracked, pre-existing** — untouched this
  session; leave it alone.
- PICKUP.md itself is **modified in the working tree** (this §14 + the
  header line + §6 update) — commit it as its own handoff, same convention
  as every prior session here.
- Last verification: `devenv shell --no-tui -- bash -c 'export
  LITELLM_URL=http://localhost:4000/v1; go build ./... && go test ./...'`
  green, both before and after the `reqrow.html` fix. §4's test discipline
  followed twice: `attachTraceChildren` reverted to a no-op and confirmed
  the new grouping tests fail for the right reason before restoring; the
  `reqrow.html` render order reverted to the (buggy) original and confirmed
  `TestRequestListRendersClassifierAboveItsParent` reproduces the user's
  exact symptom before restoring the fix.
- Tests added: `internal/ui/grouping_test.go` —
  `TestAttachTraceChildrenNestsUnderItsParent`,
  `TestAttachTraceChildrenLeavesOrphansAtTopLevel`,
  `TestAttachTraceChildrenMatchesAnyTurnOfAFoldedRun`. `internal/ui/ui_test.go`
  — `TestRequestListRendersClassifierAboveItsParent`, and
  `TestRequestListDefaultsToClientKindAndTagsOthers` updated for the new
  default-is-"all" behavior (was default-is-"client").
- **Effort-estimate note for whoever scopes #8 part 1 next:** user has
  flagged (more than once, this session included) that this assistant's own
  time estimates for this repo run high — treat "a day" style estimates
  given in earlier sessions as upper bounds, not predictions.

**Board as of this session** (`gh issue list --state open --limit 40`): 17
open, same set as §13 — **#8 is the only status change**, and it's a partial
completion, not a close. Priority ordering otherwise unchanged from §13.

**Not done, and deliberately not started:** part 1 of #8 (above). Nothing
pushed to origin — `develop` is 31 commits ahead of `origin/develop` as of
`f8a43d4`.

---

## 15. Session 2026-09-22d — #11 scoped, next target; nothing built this session

**No code changed this session.** It was pure scoping/recovery: catch up
(§14 was read, not re-derived), then the user asked what's left on #11.
`develop` is at `94ed761`, same as §14 left it, 32 ahead of
`origin/develop`. This §15 + the header line is the only diff, same
handoff-commit convention as every prior session.

**#11's real status, verified against code + the live store, not just the
ticket's own prose:**

- **Detection is genuinely built and working.** `arbiter.yaml`'s `title`
  classifier (~line 191, a decisive `match:` signature on the client's
  system prompt — opencode's *"You are a title generator"*, Claude Code's
  *"Generate a concise, sentence-case title"*) writes
  `request_kind = "title"`. Confirmed live:
  `sqlite3 "file:/data/arbiter/arbiter.db?mode=ro" "SELECT id, ts, request_kind FROM requests WHERE request_kind='title';"`
  → 3 real rows, correctly tagged. `reqrow.html:109/140` renders the tag.
- **Routing to cheap/fast models is NOT built.** `PolicyCondition`
  (`internal/router/policy.go:16-33`) has `Domain`, `Effort`, `Capabilities`,
  `CostClass`, `RequiresInputModalities` — **no `RequestKind` field**, so no
  routing rule can target title requests today. Confirmed live: the 3 real
  title rows routed to 3 *different* models
  (`auto/best-free`, `claude/claude-sonnet-5`, `openrouter/free` —
  `SELECT id, provider, model, alias_used FROM requests WHERE request_kind='title'`)
  — whatever the client happened to ask for, no override applied anywhere.
- **Grouping with the parent session is NOT built.** The ticket claims this
  is derivable read-side via `content_refs` hash matching with no schema
  change — architecturally right, and the pattern already exists:
  `RequestsForContent` (`internal/store/discovery.go:36`) already joins
  `content_refs` by hash to find every request containing one block, built
  for the discovery/boilerplate feature. Nothing wires that pattern into
  title-gen grouping specifically — no code path ties a title request to
  its parent session today.
- **The two design docs the ticket cites are unreachable from this
  container:** `~/.claude/plans/title-gen-labeling-and-stream-stacking.md`
  and a "DEFERRED: subagent / title-generation grouping" section in a
  `project_arbiter_status.md` both live on the host, not in this repo or
  this sandbox — `find / -maxdepth 4 -iname project_arbiter_status.md`
  found nothing, `~/.claude/plans/` doesn't exist here. **Don't assume
  either is readable next session either** unless the environment changes;
  treat the ticket body + the code-verified findings above as the
  authoritative scope instead of chasing those paths again.
- **#30's already-closed decision is the reason detection is cheap and
  reliable** — worth not re-deriving: a title request's session key hashes
  conversation text that changes every call, so it never pins, so it's
  always a "first request" and always gets classified under #30's rule.
  That's *why* the 3 live rows all classified correctly with no extra work.

**Concretely, what #11 needs (this is the scope handed to the user, and
what the next session should build):**

1. **Routing** — add a `RequestKind` field to `PolicyCondition`
   (`internal/router/policy.go`), wire it into `Matches`, then add an
   `arbiter.yaml` rule routing `request_kind: "title"` to a cheap/fast
   alias (`cheap-claude` already exists as a candidate target,
   `arbiter.yaml:305`). Smaller of the two — one struct field, one matcher
   branch, one config rule.
2. **Grouping** — reuse the `content_refs` hash-join pattern from
   `RequestsForContent` to link a title request to the session it named,
   surface the tie in the UI, and make the "capture_content off" case
   **visibly degraded** rather than a silent "no subagents" (the ticket's
   hard constraint). Larger of the two — UI + query work, not just a
   config change.

**Hard constraint restated from the ticket, worth keeping visible:** *"a
title request is a request, it should be shown. NO request is ever
hidden."* Exclusion may only ever filter the *view*, never drop the record
— same principle #5 (closed) already implemented for rejected requests;
don't regress it while building either half of #11.

**Not done, and deliberately not started:** all of #11 — nothing has
landed on it yet, this session was scoping only. Nothing pushed to origin.

---

## 16. Session 2026-09-22e — #11 routing half shipped, grouping half still open

**One commit, on `develop` (unpushed): `635644c`.** Ships exactly the
smaller half §15 scoped — nothing else.

| commit | what |
|---|---|
| `635644c` | **#11 routing half**: `PolicyCondition` (`internal/router/policy.go`) gains a `RequestKind` field, matched by exact string equality like every other condition (empty = wildcard). `policyRules` (`cmd/arbiter/main.go`) parses a new `request_kind` key in a rule's `when:` clause. `arbiter.yaml`'s commented policy-router example and doc comment, plus README's Routing section, both gain a `when: { request_kind: "title" }` → `cheap-claude` example. |

**Deliberately scoped to `title` only, per the user's explicit instruction
this session** — `RequestKind` is a plain freeform `string` (no enum, no
constant list anywhere in `types`/`classifier`/`store`/`router`), so there
was no "add `subagent` as an option" step to take: the field and the new
`PolicyCondition.RequestKind`/`policyRules` parsing are value-agnostic
already. Only `title` has a classifier that ever sets it; `subagent`
detection is unbuilt (that's #17 Phase F, a separate ticket) and nothing
about this change needs revisiting when it lands.

**Not shipped, still `arbiter.yaml`'s commented-out example only** — the
routing capability itself is not turned on in the deployed config, because
there's no live `policy` router block at all yet (only the commented
template). Turning it on for real (uncommenting a `policy` router, adding
the `request_kind: "title"` rule for real, verifying against live traffic)
is a config change for the user to make, not a code change — deliberately
left as such rather than editing the live `/data/arbiter/arbiter.yaml`,
which this repo's hard boundary forbids touching.

**Test discipline followed:** reverted `policy.go` + `main.go`, confirmed
`TestPolicyConditionMatchesRequestKind`,
`TestPolicyConditionSkipsRuleOnRequestKindMismatch`, and
`TestPolicyRulesParsesRequestKind` fail to **compile** (missing struct
field) rather than just fail — the right kind of RED, since a compile
failure proves the test actually exercises the new field. Restored, full
suite green: `go build`, `go vet`, `go test ./...`, `golangci-lint run
./...` all clean.

**State for the next session:**

- `internal/classifier/match_test.go` has an **unrelated, on-hold debug
  session's uncommitted change** (a `TestKindOnlyMatcherFillsRequestKindNot-
  Domain` regression test + a comment cleanup) — user's explicit instruction
  this session was to set it aside and restore it at the end, which was
  done via `git stash`/`git stash pop` around the #11 commit. It is
  **still uncommitted in the working tree**, on purpose — that debug
  session resumes after this one. Don't commit it as part of any future
  #11 work without checking with the user first.
- `feedback.md` at repo root is **untracked, pre-existing** — untouched
  this session; leave it alone.
- PICKUP.md itself is **modified in the working tree** (this §16 + the
  header line + §6 update) — commit it as its own handoff, same
  convention as every prior session here.
- **#11's grouping half is entirely unstarted** — reuse
  `RequestsForContent`'s `content_refs` hash-join pattern
  (`internal/store/discovery.go:36`) to tie a title request to its parent
  session, surface it in the UI, and make `capture_content: false`
  **visibly degraded** rather than silently "no subagents" (the ticket's
  hard constraint, restated in §15). This is the larger of #11's two
  halves — UI + query work, not just a config change — and is the natural
  next session on #11 if the user wants to keep going on it.
- Board unchanged this session (`gh issue view 11` still shows OPEN — the
  ticket covers both routing and grouping, so it should stay open until
  the grouping half also lands; not closed this session).

**Not done, and deliberately not started:** #11's grouping half. Nothing
pushed to origin — `develop` is 34 commits ahead of `origin/develop` as of
`635644c`.

---

## 17. Session 2026-09-23 — #11 grouping half, piece 1: `ParentSessionForTitle`

**One commit, on `develop` (unpushed): `fa41aa4`.** Ships piece 1 of the
grouping half's three pieces (§16's scoping): the query that ties a
`request_kind='title'` request back to the client session it titled.
Pieces 2 (UI surfacing) and 3 (visible degrade when `capture_content` is
off) are still unstarted.

| commit | what |
|---|---|
| `fa41aa4` | **#11 grouping half, piece 1**: `Reader.ParentSessionForTitle` (`internal/store/discovery.go`), a two-tier match. Tier 1 — exact: if the title request's own `session_key` is shared by any real client row, that's the parent (indexed lookup, no fuzzy matching). Tier 2 — fallback, only tried when tier 1 finds nothing: joins on shared `role='user'` content-ref hashes against other requests, excludes other `request_kind='title'` rows (guards the title-vs-title self-collision case), groups by `session_key`, returns the top match by count. Returns `(ParentSession{}, false, nil)` — not an error — when neither tier matches. |

**Design turn this session, before any code:** a live-store investigation
(triggered by the user asking "does Arbiter capture a client's own session
id at all?") found this repo's own assumption — "neither Hermes nor
o‍pencode sends session metadata" (in this assistant's memory, and
implicitly in §15's scoping) — **was stale, not evergreen**. Both clients
*have* sent a session-affinity header historically:

- o‍pencode: `X-Session-Id` / `X-Session-Affinity: ses_<b62>`, live
  2026-09-16 → 2026-09-19, then stopped.
- Hermes: `X-Session-Id: <hermes-format-id>`, live only 2026-09-21
  07:10–07:37 (27 minutes), then stopped.
- Confirmed live via `headers_json` on the actual stored rows, not
  inferred from the `session_key` shape alone.

Arbiter already supports this: `session_affinity.header` (default
`X-Session-Id`) is read in `internal/http/handler.go`, and when the header
is present its value **is** `session_key` directly — no hashing, no
separate column. It's config-gated and was off in the live deployment
until the user turned it on **during this session** (confirmed: this
session's own requests, from `9260` onward, carry `X-Session-Id:
<hermes-conversation-id>` and their `session_key` is that literal value).

**Correction, worth propagating:** "Hermes sends `X-Session-Id`" — not
`X-Hermes-Session-Id`. The header *name* is whatever
`session_affinity.header` is configured to (default `X-Session-Id`);
Hermes' own docs call the concept `session_affinity_header` but that's the
*setting name* on Hermes' side, describing "the header that carries
Hermes' conversation id on every request to that provider" (verbatim,
covers `chat_completions`/`anthropic_messages`/`codex_responses` main
turns plus auxiliary calls — compression and **titles** explicitly named).
That's exactly the mechanism piece 1's tier 1 exists to exploit: for
Hermes specifically, the title call and the real turns should carry the
*same* session id by construction, not by coincidence — tier 1 isn't a
cheap-path nicety for Hermes, it's the primary, designed-for match.

**This changed the design from §15/§16's plan.** The originally-scoped
query (pure content-hash join, now tier 2) is still needed as a fallback —
for o‍pencode-style clients whose header only fires sometimes, for any
client that never sends one, and for the case already found in the live
data where the title call's stored text and the real call's stored text
differ byte-for-byte (a wrapper Hermes adds client-side before the real
send but not before the title-gen send) — but it is no longer the primary
mechanism. Tier 1 is.

**Live data note:** as of this session, **zero** `request_kind='title'`
rows exist with a header-carried `session_key` yet — the header was only
just turned on, and no title-gen call has landed since. Tier 1 is
implemented and unit-tested against a seeded store (four new tests in
`internal/store/discovery_test.go`, one per case: session-key match wins
over a decoy content-hash match, content-hash fallback fires when
session_key gives nothing, title-vs-title self-collision is excluded, and
a genuine no-match returns `ok=false` not an error) but **not yet observed
against a real title request** — worth confirming once one arrives,
the same "verify against real traffic, not just a mock/fixture" distinction
this repo's process already insists on elsewhere.

**Test discipline followed:** reverted tier 1's `WHERE` clause to an
always-false condition, confirmed
`TestParentSessionForTitlePrefersSessionKeyMatch` fails for the right
reason (falls through to tier 2, reports `content_hash` instead of
`session_key` — proving the test actually distinguishes the two tiers,
not just checking *a* match exists), restored, full suite green again.
`golangci-lint run ./internal/store/...` clean, `gofmt -l` clean.

**`internal/ui` test suite has 5 pre-existing failing tests**
(`TestOverviewPivotsAndRanks`, `TestOverviewExplainsSingleValuedDimension`,
`TestOverviewEpochShowsPerRequest`, `TestSeriesEndpointShape`,
`TestSessionIndexAndTranscript`) — confirmed **unrelated to this session's
change**: reproduced identically with this commit's files fully stashed
out and the test cache cleared. Not investigated further this session;
flagging here so the next session doesn't mistake them for a regression
from `fa41aa4`. Worth a dedicated look — possibly connected to the
"stream stacking" UI breakage §16/prior sessions already flagged, but not
confirmed.

**State for the next session:**

- `internal/classifier/match_test.go` still has the **unrelated, on-hold
  debug session's uncommitted change** — set aside via `git stash`/`git
  stash pop` around this session's commit too, same as §16. Still
  uncommitted in the working tree on purpose.
- `feedback.md` at repo root is **untracked, pre-existing** — untouched.
- PICKUP.md itself is **modified in the working tree** (this §17 + the
  header line + §6 update) — commit it as its own handoff, same
  convention as every prior session.
- **Live config**: `session_affinity` is now genuinely on in
  `/data/arbiter/arbiter.yaml` with header `X-Session-Id` — this was a
  user-side config change made mid-session, not something this assistant
  edited (the repo's hard boundary on touching `/data/arbiter/*` was not
  crossed).
- **#11's remaining scope**: piece 2 (surface the parent-session link in
  the UI — likely reuses #8's `attachTraceChildren`/`Children` pattern in
  `internal/ui/requests.go`, and may overlap with the "stream stacking"
  breakage already flagged) and piece 3 (make `capture_content: false`
  visibly degrade tier 2 rather than just returning "no match" — tier 1
  doesn't need capture_content at all, so piece 3 is really "distinguish
  tier-2-unavailable from tier-2-tried-and-found-nothing" in whatever the
  UI shows). Neither started.
- Board unchanged (`gh issue view 11` still OPEN — correctly, since UI/
  degrade pieces remain).

---

## 18. Session 2026-09-23b — #11 grouping half, piece 2: UI nesting

**One commit, on `develop` (unpushed): `28aa378`.** Ships piece 2 of §17's
remaining scope. Only piece 3 (visible degrade when `capture_content` is
off) is left on #11.

| commit | what |
|---|---|
| `28aa378` | **#11 grouping half, piece 2**: new `attachTitleChildren` (`internal/ui/requests.go`), run after `foldRequestLines`/`attachTraceChildren` in `RequestsHandler`. Nests a `request_kind="title"` line under the client line for the session `Reader.ParentSessionForTitle` resolved to — same `Children`/`IsChild` rendering `attachTraceChildren` already uses for a classifier call (#8), reused because it's the established pattern, not because the matching logic is shared. |

**Why this isn't just a call to `attachTraceChildren`:** that function keys
on `trace_id` equality, which only exists between a request and a sub-call
*it itself spawned* (a classifier call inside the same `pipeline.Execute`).
A title-gen request is a separate, later, top-level HTTP call with no
`trace_id` in common with the session it titles — the only link is what
piece 1's query inferred. So `attachTitleChildren` takes a `resolveParent`
callback (backed by `ParentSessionForTitle` in the handler, a plain map in
tests) and keys on the *session_key of the resolved parent* instead. It runs
as its own pass, after the trace-based one, and the two compose (tested
directly: a page with both a classifier child and a title child under one
parent nests both).

**Self-collision guarded at this layer too, redundantly with piece 1's
query:** `attachTitleChildren`'s own session index (`bySession`) is only
built from non-title client lines, so even if two title lines somehow
shared a session_key, neither could become the other's parent. Belt and
braces — piece 1 already excludes this in SQL, but the UI layer doesn't
trust that as its only guard.

**User's framing going in, confirmed correct:** "piece 2 ... it'll have the
same quirk as classifier calls visually, but it'll be solved with the
grouping thing later on. It's only a visual thing." That's the actual shape
of what shipped — a title line nested via `Children` inherits whatever the
existing stream-collapsing/grouping bug already does to a nested classifier
line, unchanged and unaddressed here. Not investigated or touched this
session; still tracked as the separate, already-known "stream stacking"
issue.

**Test discipline followed:** flipped `attachTitleChildren`'s `bySession`
filter to admit title lines (`Kind != "client"` only, dropping the
`RequestKind == "title"` exclusion), confirmed
`TestAttachTitleChildrenDoesNotNestUnderAnotherTitle` fails for the right
reason (two titles nest into one line instead of staying at two), restored,
full suite green again. `golangci-lint run ./internal/ui/...` clean. `gofmt
-l` flags `grouping_test.go`/`grouping_tail_test.go`/`requests.go` — all
three **pre-existing** on `develop` before this session's edits (confirmed
via `git stash`, same file set §16 already flagged); this session added no
new gofmt violations.

**`internal/ui` still has the same 5 pre-existing failing tests** flagged
in §17 (`TestOverviewPivotsAndRanks` and siblings) — reproduced identically
with this commit's files stashed out, unrelated to this change, still not
investigated.

**State for the next session:**

- `internal/classifier/match_test.go` still has the **unrelated, on-hold
  debug session's uncommitted change** — set aside via `git stash`/`git
  stash pop` around this session's commit too. Still uncommitted on
  purpose.
- `feedback.md` at repo root is **untracked, pre-existing** — untouched.
- PICKUP.md itself is **modified in the working tree** (this §18 + the
  header line + §6 update) — commit it as its own handoff.
- **#11's only remaining scope is piece 3**: make `capture_content: false`
  visibly degrade tier 2 of `ParentSessionForTitle` rather than reading
  identically to "tier 2 ran and found nothing" — tier 1 (session_key
  match) doesn't need `capture_content` at all, so the UI needs to
  distinguish three states, not two: parent found, parent-search ran and
  found nothing, and parent-search (tier 2 specifically) unavailable
  because content isn't captured. Not started; likely needs
  `ParentSessionForTitle` (or a caller) to also report whether
  `capture_content` was on, so the template can render the right one of
  the three.
- Board unchanged (`gh issue view 11` still OPEN — correctly, piece 3
  remains).
- **Still unverified against real traffic**: no `request_kind='title'` row
  has landed with a header-carried `session_key` since `session_affinity`
  was turned on (§17). Worth checking once one arrives — that's the first
  live proof either tier actually nests a real title under a real parent
  on the page, not just in the seeded-store tests.

---

## 19. Session 2026-09-23c — #11 grouping half, piece 3: capture-off is now visible — #11 CLOSED

**One commit, on `develop` (unpushed): `ee71cb4`.** Ships piece 3, §18's
remaining scope — the last piece of #11. **All three grouping pieces plus
the routing half are now shipped; #11 is fully closed.**

| commit | what |
|---|---|
| `ee71cb4` | **#11 grouping half, piece 3**: `attachTitleChildren` now takes a `captureContent bool` and, for any title line it cannot nest, records one of three `TitleParentState` values on `requestLineView` — `titleParentFound` (resolved to a real session, just not one with a line on this page — a paging fact), `titleParentNotFound` (both tiers ran, neither matched), or `titleParentNotFoundCaptureOff` (tier 1 found nothing and tier 2 could not run because `storage.capture_content` is off). `reqrow.html` renders a "no parent shown" tag with the reason in its `title` attribute whenever a title line's state is non-empty. `ui.Handler` gained `SetCaptureContent`/an `atomic.Bool` field mirroring `pipeline.Pipeline`'s existing setter of the same name — the two packages don't otherwise share this config value, so the UI needed its own copy. Wired at both call sites config flows through: `main.go`'s startup wiring and `reload.go`'s `reload()` (which now also takes `adminUI *ui.Handler`, threaded through `watchConfig` and the admin-reload closure). |

**Why a three-way split, not two:** before this, an unnested title line
looked the same whether tier 2 (content-hash join) genuinely searched and
found nothing, or never ran at all because `capture_content` was off. Only
tier 1 (session-affinity header) needs no capture. A reader with capture off
seeing every title line unnested had no way to tell "the join tried and
missed" from "the join was never possible" and could reasonably read the
whole feature as broken. This is exactly the ticket's hard constraint from
§15/§18: capture-off must **visibly degrade**, not silently look identical
to "no match found".

**`reload()`'s signature grew a parameter** (`adminUI *ui.Handler`) because
`adminUI` is built once at startup and never rebuilt on reload (unlike the
pipeline, which `buildPipeline` reconstructs from scratch every reload) — so
its capture_content copy needed its own explicit update call on the same
path. `nil` is accepted (guarded with an `if adminUI != nil` check) so tests
that don't exercise the admin UI aren't forced to construct one.

**Test discipline followed:** added
`TestAttachTitleChildrenLeavesUnresolvedCaptureOff` alongside the existing
`TestAttachTitleChildrenLeavesUnresolvedAtTopLevel`, asserting the two now
produce different `TitleParentState` values from the same `resolveParent`
result (`ok=false`) — one exercises `captureContent=true`, the other
`captureContent=false`. Also asserted the nested-parent test's child line
keeps `TitleParentState == titleParentNested` (the zero value), which is
what caught golangci-lint's `unused` complaint on that constant before this
was added. Full suite green: `go build`, `go vet`,
`go test ./internal/ui/... -run "TestAttachTitleChildren|TestAttachTraceChildren|TestFoldRequests|TestNoUnsafeContentConversions"`
(all pass), then the full `go test ./...` — same 5 pre-existing `internal/ui`
failures flagged in §17/§18 (`TestOverviewPivotsAndRanks` and siblings),
still unrelated, still not investigated. `golangci-lint run
./internal/ui/... ./cmd/arbiter/...` clean. `gofmt -l` flags the same three
pre-existing files §18 already flagged (`grouping_test.go`,
`grouping_tail_test.go`, `internal/ui/requests.go`) — reconfirmed via
`git stash` that they're dirty at baseline too, no new violations added.

**State for the next session:**

- `internal/classifier/match_test.go` still has the **unrelated, on-hold
  debug session's uncommitted change** — set aside via `git stash`/`git
  stash pop` around this session's commit too. Still uncommitted on
  purpose.
- `feedback.md` at repo root is **untracked, pre-existing** — untouched.
- PICKUP.md itself is **modified in the working tree** (this §19 + the
  header line + §6 update) — commit it as its own handoff.
- **#11 is fully shipped in code.** The board (`gh issue view 11`) was still
  OPEN as of this session's start — **close it** (`gh issue close 11`) once
  this handoff is confirmed, the same convention #5/#34/#43 followed: don't
  leave a ticket open once every piece is on `develop`.
- **Still unverified against real traffic**: no `request_kind='title'` row
  has landed with a header-carried `session_key` since `session_affinity`
  was turned on (§17) — carried forward from §17/§18, still true. Worth
  checking once one arrives; not a blocker for closing #11, since all three
  pieces are covered by seeded-store unit tests.
- **Deployed config still needs a manual step** the repo cannot take:
  `/data/arbiter/arbiter.yaml` has no live `policy` router rule routing
  `request_kind: "title"` to a cheap alias yet (§16's routing half shipped
  the capability, not the config) — that's the user's config change to
  make, same boundary noted in §16.

---

## 20. Session 2026-09-23d — #11 closed on the board; doc gap fixed; ticket-less version-logging

**Four commits, all on `develop` (unpushed):**

| commit | what |
|---|---|
| `c0ef3c4` | PICKUP.md §19 (piece 3's handoff — this file lagged its own commit by one turn last session) |
| `8671cfa` | **README.md**: new section next to the existing `request_kind` explanation, documenting the two-tier `ParentSessionForTitle` match, the UI nesting, and the three `TitleParentState` values — README had never been touched by any of #11's three grouping pieces |
| `2bd3fdd` | **`arbiter.yaml`** (sample config): `capture_content`'s and `session_affinity`'s doc comments gained a paragraph each explaining their role in title-gen parent linking — same gap as the README commit, same fix |
| `0fc2617` | **Ticket-less**: startup now logs `revision`/`modified`/`build_time` read from Go's auto-embedded VCS stamp (`runtime/debug.ReadBuildInfo()`), right after "Arbiter starting" |

**Board:** `gh issue close 11` run this session (was still OPEN despite being
fully shipped in code since `ee71cb4`) with a closing comment summarizing all
four pieces. Verified via `gh issue view 11 --json state` → `CLOSED`.

**Why the doc-gap commits exist:** the user asked "did you update the
documentation for this?" after piece-3 shipped. Checking found PICKUP.md
(session handoff, not user-facing) was the *only* place any of #11's three
grouping pieces were documented — README.md and the sample `arbiter.yaml`
both predate #11 entirely on this topic. Same gap existed for #8 part 3
(classifier nesting) — confirmed via `git show <its-commit> --stat`, never
fixed, not in this session's scope to fix retroactively.

**Version-logging feature (no ticket — quick add, user's own words):**
"when starting, show/log the version from vcs.revision and vcs.time, so i
can see which version is running." Added `buildVersion()` in
`cmd/arbiter/main.go`, called once at startup. Key facts, verified by
actually building and running the binary (not just building):

```
Arbiter version revision=2bd3fdde0ccb modified=true build_time=2026-09-23T09:17:29.000Z
```

- Only a `go build` run from inside a git checkout carries `vcs.*` settings.
  Confirmed by building `go version -m` against a real built binary (present)
  vs. a `go test -c` binary (absent, no `vcs.*` keys at all). `go run` also
  doesn't stamp it. `buildVersion()` fails soft — empty string / `false` /
  zero `time.Time` — rather than assuming the stamp exists, since a `go test`
  binary running `TestBuildVersionDoesNotPanic` is exactly that case.
- Revision truncated to 12 chars (full 40-char SHA is unreadable in a log
  line; this repo's own git usage already trains the eye on short hashes).
- New test: `cmd/arbiter/version_test.go`, `TestBuildVersionDoesNotPanic` —
  deliberately does not assert non-empty content (can't fake a VCS stamp
  from inside `go test`), just that the function never panics and the
  12-char cap holds.
- Scope was kept to exactly what was asked: a startup log line. No admin/UI
  version endpoint was added — not requested.

**Test discipline:** `go build ./...`, `go vet ./...` clean. Full
`go test ./...` (via `devenv shell -- env LITELLM_URL="http://localhost:4000"
go test ./...`) — same 5 pre-existing `internal/ui` failures flagged since
§17 (`TestOverviewPivotsAndRanks` and siblings), zero new failures.
`golangci-lint run ./cmd/arbiter/...` clean. `gofmt -l cmd/arbiter/main.go`
clean (no output). Pre-commit hooks (gitleaks, golangci-lint, ripsecrets,
trufflehog) passed on the version-logging commit.

**State for the next session:**

- `internal/classifier/match_test.go` still has the **unrelated, on-hold
  debug session's uncommitted change** — stashed/popped around each of this
  session's commits that touched tracked files (the README/arbiter.yaml/
  PICKUP.md-only commits didn't need it; the version-logging commit did).
  Confirmed restored untouched via `git diff --stat` after each pop. Still
  uncommitted on purpose — that debug session is still on hold.
- `feedback.md` at repo root is **untracked, pre-existing** — untouched.
- PICKUP.md itself is **modified in the working tree** (this §20 + the
  header line + §6 update) — commit it as its own handoff, per this
  session's convention.
- **No open work generated this session.** #11 is closed. The version-log
  feature has no ticket and needs none — it's done.
- **Still unverified against real traffic**: no `request_kind='title'` row
  has landed with a header-carried `session_key` since `session_affinity`
  was turned on (§17) — carried forward, still true, still not a blocker
  for anything (all three grouping pieces are covered by seeded-store unit
  tests).
- **Deployed config still needs the same manual step** noted in §16/§19:
  `/data/arbiter/arbiter.yaml` has no live `policy` router rule routing
  `request_kind: "title"` to a cheap alias — the user's config change to
  make, not this repo's.
- **Next deploy of the built binary will show its version at startup** —
  no action needed, this is automatic once `develop` is deployed and built
  with `go build` (not `go run`) from inside the git checkout.

---

## 21. Session 2026-09-23e — #45 scoped and split; #46 (foundation) shipped and closed

**Two commits, both on `develop` (unpushed):**

| commit | what |
|---|---|
| `930e217` | `DESIGN.md` — token spec (color/typography/spacing/radii) + page-pattern prose for the from-scratch admin UI rebuild (already on disk at session start, see prior session) |
| `b8907e8` | **#46**: `app.css` rebuilt against `DESIGN.md`'s tokens (renamed/re-derived custom properties — `--fg`→`--ink`, `--card`→`--surface`, `--accent`→`--secondary`, plus new `--primary` and `--kind-*` channels for #47), `layout.html` chrome updated, and the full-screen popover component (`popover.js` + CSS) built once for reuse by #48/#49/#50 |

**#45 scoped into 5 native GitHub sub-issues**, not one bundled ticket (user's
explicit preference — "sub phases sounds fine"). Filed with `gh issue create
--parent 45`, linkage verified via `gh api graphql`'s `subIssues` query, not
just body-text mention:

1. **#46 — foundation** (tokens, layout chrome, popover) — **shipped, closed**, `b8907e8`.
2. **#47 — requests page** (routing chain requested→routed→actual, unified `kind-*` chips, parent/child threading — child row above parent, indent only the time column, cost column pinned via CSS `order`, stream-stack grouping breaking on subagent interruption).
3. **#48 — session transcript** (rail-layout metadata, full-screen popover with background-scroll lock — now built in #46 — content-first tool-call rendering, inline line-level guardrail diff). Heaviest of the five.
4. **#49 — sessions index** (search-first box, first-message preview column, fixed-width trailing columns — auto-width was a real caught bug, hard requirement). Smallest; ranked 4th not 5th because it only reapplies #46-#48's conventions.
5. **#50 — discovery** (dedupe by session not raw request, role/block-type filter chips, User-Agent clustering, plus visual pass). Deliberately last: only phase bundling backend/query changes with the visual rebuild.

Fixed build order: **1 → 2 → 3 → 4 → 5**. Foundation first because 2-5 all
depend on it (popover built once in #46, reused three times). Discovery last
because it is the only phase with non-UI dependencies that shouldn't block
the visual rebuild of the rest.

**#46 scope, what actually changed:**
- `internal/ui/static/app.css` — every custom property renamed/expanded per
  `DESIGN.md`. No page-specific selector shape changed; confirmed via grep
  that no template or JS reads a CSS custom property directly (`grep -rn
  -- "--fg\|--bg\|..." internal/ui/templates internal/ui/static/*.js` — zero
  hits), so the rename is fully contained to `app.css` and the one test
  asserting its literal content.
- `internal/ui/static/popover.js` — new file. Full-screen popover: locks
  background scroll while open, closes on Escape/backdrop-click/close
  button, restores focus to the trigger on close. Two trigger modes:
  `data-popover-text` (static, inserted via `textContent` only — same
  never-mark-stored-content-safe rule every existing template already
  follows) and `data-popover-src` (htmx-fetched).
- `internal/ui/templates/layout.html` — one new `<script defer>` line
  wiring in `popover.js` alongside htmx/uplot/chart.js/live.js.
- `internal/ui/ui_test.go` — `TestStaticAssetsAreServedAndVersioned`'s
  content check updated from `--fg` to `--ink` (the renamed token); the
  only test coupled to `app.css`'s literal variable names.

**Verified, not just claimed:**
- `go build ./...`, `go vet ./...` clean.
- `golangci-lint run ./internal/ui/...` — 0 issues.
- `gofmt -l internal/ui/ui_test.go` — clean (no output after `-w`).
- `go test ./internal/ui/...` — same 5 pre-existing failures flagged since
  §17/§20 (`TestOverviewPivotsAndRanks` and siblings). Confirmed identical
  with and without this session's changes via `git stash` compare (stash
  didn't actually move anything since `popover.js` is untracked, so this
  was really a rerun against the pre-change tree at `930e217` — same 5
  failures either way). Zero new failures. Every test that reads
  `app.css`/`layout.html`/`popover.js` content
  (`TestStaticAssetsAreServedAndVersioned`, `TestPageReferencesVersionedAssets`,
  `TestEveryPageRendersWithZeroData`, `TestNoUnsafeContentConversions`) passes.
- Manually rendered the requests page against a seeded store (scratch test,
  deleted after) and read the actual HTML output — confirmed the popover
  script tag and new page structure render correctly.
- Pre-commit hooks (gitleaks, golangci-lint, ripsecrets, trufflehog) passed
  on the `b8907e8` commit.

**Board:** `gh issue close 46` run this session with a closing comment
summarizing what shipped and how it was verified.

**State for the next session:**

- `internal/classifier/match_test.go` still has the **unrelated, on-hold
  debug session's uncommitted change** — untouched this session, confirmed
  via `git --no-pager diff --no-ext-diff` showing only that session's own
  content. Still on hold, still not this repo's active work.
- `feedback.md` at repo root is **untracked, pre-existing** — untouched.
- **#47 is next.** User asked whether to start it in this session or a
  fresh one — open question, not yet decided as of this handoff. Whichever
  session picks it up: the requests page needs the `kind-*` CSS channel
  (already shipped in #46) and `internal/ui/templates/partials/reqrow.html`
  / `pages/requests.html` (not yet read this session — read those plus
  `DESIGN.md`'s requests-page pattern section before scoping #47's diff).
- No other open work generated this session — #45's shape is now fully on
  the board as #46-#50, exactly mirroring §6's summary above.

---
