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
closed. New §22 added 2026-09-23f at `develop` = `76fb7ea` — #47 (phase 2:
requests page — routing chain, kind chips, threading) shipped and closed.
New §23 added 2026-09-23g at `develop` = `0cd748f` — #48 (phase 3: session
transcript) shipped and closed. New §24 added 2026-09-24 on branch `newui`
(not `develop`) — session-transcript **mockup** redesign settled
(`design/mockups/transcript-D-merged.html`), nothing landed in `internal/ui`
yet; see `design/REDESIGN.md` §8, not this file, for the design narrative.
New §25 added 2026-09-24 (later same day) — Discovery page (#50) mockup
design thread started and **parked mid-exploration** (ledger direction,
not yet validated against real use); see `design/REDESIGN.md` §9, not this
file, for the design narrative. New §26 added 2026-09-24 (later still),
branch `newui`, HEAD still `35a0262` (nothing committed this session
either) — **Overview page (no prior ticket) designed and settled**
(`design/overview-mockups/f3-overview-styled.html`), and **the board
actually changed**: #45 and #49 closed (superseded), #52/#53/#54/#55
opened. Run `git log --oneline -1` and `gh issue list --state open` for
the truth — §6's open-work table below predates all four new issues.
New §27 added 2026-09-24g on branch `newui` (HEAD still `35a0262`) — **#55
implemented**: token vocabularies reconciled into one canonical set, the
shared header/footer/store-disabled shell ported, and the **entire old
HTML/CSS/JS UI deleted** (all page templates and partials gone; Go handler
code untouched and will 500 at render time until #50/#52/#53/#54 port real
templates back in — see §27 for the exact note). Nothing committed yet.
New §28 added 2026-09-25 on branch `newui`, HEAD still `35a0262` (nothing
committed this session either) — **#52 implemented**: the Sessions page
(lanes + persistent 380px detail panel) ported from
`design/mockups/arbiter-redesign-5-lanes-realdata.html` into
`internal/ui/templates/pages/sessions.html` +
`internal/ui/templates/partials/laneRow.html` + `SessionsHandler`; app.css
gained the page's own rules; **ticket intentionally left open** — user is
doing #50/#52/#53/#54 in one uncommitted batch, then testing, then closing
them together. New §29 added 2026-09-25b, same branch, HEAD still
`35a0262` — feedback round 2: toolbar chrome fixes (pagehead/lede removed,
errors/all toggle bug fixed), and the nav's "N active" Sessions stat wired
to a real cache-TTL-derived query (`affinity_pins`) instead of a mockup
placeholder. New §30 added 2026-09-25c, same branch, HEAD still `35a0262`
— feedback round 3: the lane list's flex-sizing chain fixed so it scrolls
vertically in a bounded region instead of growing the page, and long
sessions' timeline nodes compress to the lane's own width
(`--node-scale`, `laneTimeline.js`) instead of forcing horizontal scroll.
New §31 added 2026-09-25d, branch `newui`, **now at `77c37b5`** — §28-30's
work and all three feedback rounds **committed in one commit**; ticket
**still intentionally left open** per the user, who is batching
#50/#52/#53/#54 before testing and closing. Branch has no upstream
configured yet (`git push -u origin newui` needed on first push). Run
`git log --oneline -1` and `gh issue list --state open` for the truth.
New §32 added 2026-09-25e, branch `newui`, **now at `9faebe6`** — #53
(session transcript) implemented and committed: server-rendered
turn-by-turn page, 5 live bugs found and fixed via a headless-Chrome/
CDP verification pass against the real prod DB (not just tests) — see
§32 for the full bug list, including the CSS-comment-swallows-a-rule
landmine. New §33 added 2026-09-25f — handoff refresh only, nothing
new built, HEAD unchanged at `99bac9a`. New §34 added 2026-09-25g,
branch `newui` — #50 (Discovery) implemented end-to-end
(cross-session repeated-block ledger, seen/ignored state, drill-down),
**nothing committed that session**. New §35 added 2026-09-26, branch
`newui`, **now at `5c3b8c7`** — §34's Discovery work plus three
same-thread follow-ups (in-memory TTL cache cutting the ledger query
from ~14s to 5ms on reload; workspace mode now shows the full stored
body untruncated, not the old 8KB-capped/200-char-preview version;
`hide_ignored=1` toolbar toggle) **committed** as `c836c9b`, handoff as
`5c3b8c7`. Progress comment posted on #50; **ticket still intentionally
left open** — #50/#52 (shipped)/#53 (shipped)/#54/#55 close together
after the user's own full-UI pass. #54 (Overview) is the only page in
the batch left unported. Branch `newui` still has no upstream
configured. New §36 added 2026-09-26 (later), **now at `aedb979`** —
**#54 (Overview) implemented and committed** across five commits
(`325b03b` store layer, `dbacc38` burst-collapse fix, `ecc6299`
summaries/compare, `26c358c` the page + the pivot/series/uPlot rip-out,
`aedb979` the toolbar fix from the user's first real click-through);
`aedb979` also carries a full documentation refresh. **Every page in
the batch is now built** — the user's own full-UI pass is the only
thing left before the batch closes. Two real defects surfaced during
the doc pass and are **unfixed, no ticket**: every `flatLineHref` link
on the Sessions page 404s, and the live tail's endpoint has no page
mounting it. See §36. Run `git log --oneline -1` and `gh issue list
--state open` for the truth.

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

Run `gh issue list --state open` for the live list — this section is
stale (last reconciled `b8907e8`, 2026-09-23e, §21, before #50-#55
existed) and kept only for historical framing of the older items below.
For the `newui` admin-UI-rebuild batch, trust §35 (and `git log`), not
this table.

**Admin UI rebuild batch (#45's successor issues #50/#52-#55), as of
`5c3b8c7` (§35, 2026-09-26) — do not trust the table below for this
batch, it predates all of it:**
1. **#46 — foundation** (tokens, layout chrome, popover) — shipped, closed.
2. **#47 — requests page** — shipped, closed.
3. **#48 — session transcript (old markup)** — shipped, closed, later
   superseded by #53's redesign.
4. **#52 — Sessions page (lanes + detail panel)** — shipped (`77c37b5`),
   **ticket open**, batched.
5. **#53 — session transcript (redesign)** — shipped (`9faebe6`),
   **ticket open**, batched.
6. **#50 — Discovery (cross-session repeated blocks)** — shipped
   (`c836c9b`), **ticket open**, batched.
7. **#54 — Overview page** — **shipped** (`26c358c` + `aedb979`, branch
   `newui`); the toolbar was fixed against the user's first real use of it.
   **Ticket open**, batched. This was the last page in the batch.
8. **#55 — CSS token reconciliation** — shipped, **ticket open**, batched.

All of #50/#52/#53/#55 (shipped, not yet closed) wait on #54 landing
**before the user does one full-UI pass and closes the batch together** —
see §35. #54 has now landed, so every page in the batch is built and that
pass is the next step. #46/#47/#48 already shipped and closed earlier in
the batch.

As of `b8907e8` (2026-09-23e, §21), open: **#4, #8, #9, #14, #17, #18, #20, #21, #22,
#23, #24, #26, #36, #38, #40, #41, #44, #45, #47, #48, #49, #50**. Closed
since §5/§10 were last written: **#27, #28, #31, #37, #39, #13, #43, #42,
#5, #34, #11, #46** (see §11–§13, §20, §21).

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

## 22. Session 2026-09-23f — #47 (phase 2: requests page) shipped and closed

`develop` = `76fb7ea` at write time.

Second phase of #45's admin UI rebuild. Per the greenfield decision settled on
#45 ("act as if the old ui just does not exist... if what you do here makes any
of the old ui stop working, that is fine"), this is a from-scratch rewrite of
`internal/ui/templates/partials/reqrow.html` against `DESIGN.md`'s "Request
rows" spec — not a port of the old `<table class="grid">` markup. Old
markup/class assertions in the test suite were rewritten to match the new
markup, not preserved.

**#47 scope, what actually changed:**
- `internal/ui/templates/partials/reqrow.html` — rewritten from scratch: `.reqrow`
  CSS grid rows (`3px status-bar | 84px time | 1fr chain | auto session | auto
  cost`, cost/latency pinned last via CSS `order`, not markup position).
  Chain rendering, threading (`Children`/`IsChild`), grouping/count badge, and
  the flat-mode row body all live in this one file, sharing the row shape
  between `req-line` (grouped) and `req-rows-body` (flat/tail).
- `internal/ui/requests.go` — one new helper, `requestRowView.RoutingChain()`
  (line ~119): collapses `AliasUsed`/`Model`/`ActualModel` into `"literal: X"`
  / `"alias: X → Y"` chain segments, dropping a redundant `X → X`. No
  schema/query changes — reuses fields `pipeline.go` already populates at
  record time.
- `internal/ui/static/app.css` — `table.grid` rules replaced with
  `.reqlist`/`.reqrow`/`.reqhead` grid rules; wired the `--kind-classifier`/
  `--kind-title`/`--kind-subagent` tokens #46 staged (comment said "wired up
  by #47") into a single `.tag.kind-*` chip shape shared by both `Kind`
  (Arbiter-internal client/classifier/title/subagent) and `RequestKind`
  (classifier/title/subagent axis) — one chip shape, color-only distinction,
  per spec. Added `.sesschip` (bordered chip + dot-marker for session id, per
  spec) and `.c-cost`/`.axes`/`.chain` column rules.
- `internal/ui/static/live.js` — `buildSeen`/`append`/`bumpLine`/
  `makeCountBadge` updated from `tr[data-id]`/`tbody` to
  `.reqrow[data-id]`/`.reqbody`. Same placement logic (grouped vs. flat,
  dedupe by id, bump-and-reorder on group key match) against the new DOM
  shape. Integration contracts kept as-is, unchanged: `data-id` per row,
  `#rows` + `data-tail-*` attrs for the poll, template names (`req-line`,
  `req-rows`, `req-rows-body`) called by string from `requests.go`/`live.go`.
- Test assertions on old markup rewritten (not preserved) in
  `grouping_test.go`, `grouping_tail_test.go`, `live_test.go`, `ui_test.go` —
  `<tr`/`<tr data-id="` counting replaced with `data-id="` counting;
  `class="tag kind"` replaced with `class="tag kind-classifier"` (the actual
  new class); the tail-markup sanity checks (`<tr` presence) replaced with
  `class="reqrow` presence.

**Verified, not just claimed:**
- `go build ./...`, `go vet ./...` clean.
- `go test ./...` clean except the same 5 pre-existing `internal/ui` failures
  flagged since §17/§20/§21 (`TestOverviewPivotsAndRanks`,
  `TestOverviewExplainsSingleValuedDimension`, `TestOverviewEpochShowsPerRequest`,
  `TestSeriesEndpointShape`, `TestSessionIndexAndTranscript`) — confirmed
  pre-existing at `b8907e8^` (before #46 landed) earlier this session via
  stash+checkout+restore. Zero new failures; every #47-affected test
  (`TestFlatPageRendersEveryRow`, `TestGroupedAndFlatProduceTheSameRequests`,
  `TestTailRowsCarryTheirGroupWhenGrouped`, `TestTailRowCarriesItsGroupEvenAlone`,
  `TestTailFirstPollReturnsTheNewestFirstAndACursor`,
  `TestTailAppendsOnlyWhatArrived`, `TestTailHonoursTheFilters`,
  `TestTailReportsTruncation`, `TestRequestListDefaultsToClientKindAndTagsOthers`)
  passes against the new markup.
- Manually rendered the requests page against a seeded fixture (scratch
  `_test.go`, deleted after) and read the actual HTML output — confirmed:
  single-segment chain for a literal model, two-segment chain for a
  divergent `ActualModel`, `kind-classifier` chip on an Arbiter-internal row,
  `s-err` status bar on a 500, session-less row rendering "none", `data-key`
  present on a streamed/grouped row.
- Pre-commit hooks (gitleaks, golangci-lint, ripsecrets, trufflehog) passed
  on the `76fb7ea` commit (run inside `devenv shell`, since `go` is not on
  PATH outside it and golangci-lint needs it).

**Board:** `gh issue comment 47` + `gh issue close 47` run this session with a
closing comment summarizing what shipped and how it was verified.

**State for the next session:**

- `internal/classifier/match_test.go` still has the **unrelated, on-hold
  debug session's uncommitted change** — untouched again this session
  (stashed earlier this session via `git stash push -- internal/classifier/match_test.go`,
  not popped back — check `git stash list` before assuming the tree is clean
  of it).
- `feedback.md` at repo root is **untracked, pre-existing** — untouched.
- **#48 is next** (per #45's phase order — check `gh issue list` for its
  exact scope; not yet read this session). The requests page's row/chip/chain
  patterns (`.reqrow`, `.tag.kind-*`, `.sesschip`, `.axes`) are now the
  reference implementation for "reuse verbatim elsewhere" per DESIGN.md's
  cross-page-conventions section — whichever phase touches the session
  transcript or sessions index should read `reqrow.html`/`app.css`'s new
  rules before inventing new conventions.
- No other open work generated this session.

---

## 23. Session 2026-09-23g — #48 (phase 3: session transcript) shipped and closed

**Scope (DESIGN.md "Session transcript"):** no chat bubbles (full-width block,
colored left border for direction — user=secondary, assistant=primary);
metadata in a rail beside the blocks, not a row between them; no nested
scrollboxes (gradient-fade + "show more" instead of `overflow:auto`);
full-screen popover reuse (#46) for very long content; content-first tool
call/result rendering (summarized command/pattern/path per known tool name,
raw `{id,name,input}` JSON behind a toggle); a collapsed "modified by
guardrail" `warn` chip that fetches its line-level diff on demand rather than
showing it inline.

**What actually changed:**
- `internal/store/reader.go` — `ContentBlock.GuardrailTouched` (set in
  `filterRequestDirection` by comparing each kept block's hash against its
  counterpart in the dropped direction at the same `(msg_index, position)` —
  free, since both directions are already fetched per request) and
  `Reader.GuardrailDiff(id, msgIndex, position)` (fetches the before/after
  text for one touched block).
- `internal/store/diff.go` — hand-rolled line-level LCS diff (`LineDiff`/
  `DiffOp`: `"eq"`/`"del"`/`"add"`). No new dependency — `go.mod` still has
  only fsnotify/uuid/gorilla/yaml.
- `internal/ui/toolcall.go` — decodes a `tool_use`/`tool_result` block's
  canonical JSON body (see `store.blockBody`) into a content-first view:
  `terminal`/`bash`/`shell`/`exec` → command string, `grep`/`search`/`find` →
  pattern+path, `edit`/`write`/`patch`/`read` → file path; unrecognized tool
  names get a generic `key=value` fallback, not a hard failure. Raw JSON is
  always computed and available behind a toggle.
- `internal/ui/requests.go` — `GuardrailDiffHandler` at
  `GET /admin/ui/requests/{id}/guardrail-diff?msg=&pos=`, an htmx fragment,
  registered alongside `RequestContentHandler`'s existing pattern.
- `internal/ui/sessions.go` — `transcriptBlock` now carries the parsed
  `ToolCall`/`ToolResult` view (computed once per block in the handler, not
  in the template).
- `internal/ui/templates/pages/session.html`,
  `internal/ui/templates/partials/session-turns.html`,
  `internal/ui/templates/partials/guardrail-diff.html` — new, from scratch.
  Old `internal/ui/templates/partials/turn.html` **deleted** — it was
  pre-#45 markup (`.turnmeta`/`.block`/`.blockmeta`/`.preamble` as originally
  named) and the greenfield directive from #45/#47 treats it as not existing,
  same as `reqrow.html`'s predecessor was treated in #47.
- `internal/ui/static/showmore.js` — new: the gradient-fade "show more"
  toggle, reused by any long block text or tool result. Delegated click
  listener, height-measured on load and after any htmx swap, same
  self-contained IIFE style as `popover.js`.
- `internal/ui/static/app.css` — new `.transcript`/`.turn`/`.turn-rail`/
  `.block`/`.preamble`/`.toolcall`/`.toolresult`/`.diff`/`.chip`/`.showmore`
  rules, reusing #46/#47's tokens (`--tool`, `--warn`, `--kind-*`) and
  `.tag`/`.sesschip`/`.axes` conventions rather than inventing new ones.

**Tests:** `sessions_test.go`'s markup assertions updated to the new
structure — `id="turn-N"` kept (added alongside the new `data-turn`
attribute, since the test's turn-order/scoping checks use it and there was
no reason to break them), `.preamble` section class, the `<pre>` regex
loosened to `<pre[^>]*>` to match the added `class="blocktext"` attribute —
same "rewrite assertions against new markup" precedent #47 set. New
coverage: `internal/store/diff_test.go` (LCS correctness: pure
insert/delete, adjacent replace, identical-input no-op, empty-string edges),
`internal/ui/toolcall_test.go` (known tool-name families, unknown-name
fallback, canonical-JSON decode + malformed-body degrade),
`internal/ui/toolcall_transcript_test.go` (content-first rendering end to
end through the transcript handler), `internal/ui/guardrail_diff_test.go`
(chip renders only when touched, diff fragment renders del/add/eq).

**Verified, not just claimed:**
- `go build ./...`, `go vet ./...` clean.
- `go test ./...` clean except the same pre-existing failures: the 5
  `internal/ui` ones §17/§20/§21/§22 already named
  (`TestOverviewPivotsAndRanks`, `TestOverviewExplainsSingleValuedDimension`,
  `TestOverviewEpochShowsPerRequest`, `TestSeriesEndpointShape`,
  `TestSessionIndexAndTranscript`) plus 2 in `internal/config`
  (`TestShippedArbiterYAMLLoads`, `TestShippedTitlePatternsAreTheIntendedRegexes`
  — the shipped `arbiter.yaml` is missing a litellm endpoint, unrelated to
  UI work). Confirmed pre-existing by `git stash -u` back to `1b66d93`
  (pre-#48) and re-running the same failing tests — identical failure set,
  before any of this session's files existed.
  `TestSessionIndexAndTranscript` specifically fails only on its
  sessions-index assertions (checking `/admin/ui/sessions`, i.e. #49's
  scope, not yet built) — every assertion against the transcript page itself
  passes.
- Pre-commit hooks (gitleaks, golangci-lint, ripsecrets, trufflehog) passed
  on the `0cd748f` commit (run inside `devenv shell`).

**Board:** `gh issue close 48` run this session with a closing comment.

**State for the next session:**

- `internal/classifier/match_test.go` still carries the **unrelated, on-hold
  debug change** — not touched, not re-checked this session; `git stash
  list` still shows the same `stash@{0}` from §22 (never popped).
- `feedback.md` at repo root is untracked, pre-existing — untouched.
- **#49 (sessions index) or #50 (discovery) is next** per #45's phase order —
  neither read this session; check `gh issue list` for exact scope before
  starting. The transcript's new conventions
  (`.transcript`/`.turn`/`.block`/`.chip`/`.showmore`/`showmore.js`) are now
  the reference implementation for anything else that needs to show long
  captured content or a tool call, per the same "reuse, don't reinvent" rule
  #47 set for `.reqrow`/`.tag.kind-*`.
- No other open work generated this session.

---

## 24. Session 2026-09-24 — session-transcript mockup redesign settled (branch `newui`, nothing landed in code)

**This section is a pointer, not a replay** — full narrative, decisions, and
open items live in `design/REDESIGN.md` §8 (mockup-only work belongs there,
not here, per that file's own header). This section exists so a session
starting from PICKUP.md alone doesn't miss that the transcript-page design
thread moved since §23.

**Branch/state (exact, this turn):**
```
$ git status -sb
## newui
 M design/REDESIGN.md
?? cmd/previewserver/
?? design/mockups/Caddyfile
?? design/mockups/arbiter-redesign-7-transcript.html
?? design/mockups/arbiter-redesign-8-transcript-dense.html
?? design/mockups/arbiter-redesign-9-transcript-realdata.html
?? design/mockups/data-v2/
?? design/mockups/data/
?? design/mockups/devenv.lock
?? design/mockups/transcript-B-ledger.html
?? design/mockups/transcript-C-timeline.html
?? design/mockups/transcript-D-merged.html

$ git log --oneline -3
35a0262 Sessions: finalize lane-row design, grounded in real data
607e542 DESIGN.md: reconcile with merged-Sessions redesign exploration (newui)
2f3a740 PICKUP.md: session handoff — #48 shipped and closed (phase 3 of #45)
```
**Nothing was committed this session.** `design/REDESIGN.md`'s modification
and every untracked mockup file above are this session's (and the prior
session's, for B/C) uncommitted work — commit/discard is the user's call,
same as every prior mockup round.

**What's actually done:** the session-transcript page's *design* is settled
(`design/mockups/transcript-D-merged.html`), fitted into the site chrome
(`header.top`/`footer.appfoot` from file 9, look-and-feel from file 5,
`DESIGN.md`'s page-pattern prose explicitly demoted to non-authoritative —
see REDESIGN.md §8 point 3 for the exact user quote establishing that).
**Nothing in `internal/ui/*` changed this session** — this was 100%
mockup/design work, same separation of concerns #45's own issue body
describes ("design and build kept in separate sessions").

**Next steps, in order:**
1. Port `transcript-D-merged.html` into `internal/ui/templates/*` +
   `internal/ui/static/app.css` + whatever Go handler changes it needs
   (new ticket — none exists yet for this specific port; #48 is already
   closed and was scoped against the old, now-superseded rail design).
   File a fresh issue before starting, don't silently reopen #48.
2. **Discovery page (#50) design — start a new session for it.** The user
   asked explicitly ("should i start a new session for the discovery page?
   your context is filling up") and the answer given was yes, to keep this
   session's now-stale B/C/D/extractor exploration out of unrelated work.
   #50's scope (dedupe-by-session, role/block-type filter chips, User-Agent
   clustering) has real backend/query changes bundled with the visual pass
   — read the issue body in full before drafting anything.
3. `DESIGN.md`'s "Session transcript" and "Discovery" page-pattern sections
   are both stale (transcript describes the pre-D rail design; discovery
   was never touched by this redesign thread at all). Batch their
   reconciliation into one pass once Discovery's design is also settled,
   per REDESIGN.md §8 point 4 — don't do it piecemeal.

**Landmines carried from this session** (full detail in REDESIGN.md §8):
- `DESIGN.md` page-pattern sections are **not authoritative** right now —
  only its top-of-file token block is "probably" still good. Don't cite
  `DESIGN.md`'s prose as a requirement for transcript or discovery work
  without checking it against the mockup files first.
- The extractor's old 6000-char `trunc()` was a real bug (not just a modal
  issue) — if `internal/ui`'s own read path has an equivalent length cap
  anywhere, check it against the same "don't truncate, the modal already
  scrolls" instruction before porting D's markup.

---

## 25. Session 2026-09-24 (later same day) — Discovery page (#50) mockup design started, PARKED mid-exploration (branch `newui`, nothing landed in code)

**This section is a pointer, not a replay** — full narrative, decisions, and
open items live in `design/REDESIGN.md` §9 (mockup-only work belongs there,
not here, per that file's own header, same convention §24 used for the
transcript thread). This section exists so a session starting from
PICKUP.md alone doesn't miss that a second design thread (Discovery) ran
this same day, after §24's transcript thread settled.

**Branch/state (exact, this turn):**
```
$ git status -sb
## newui
 M PICKUP.md
 M design/REDESIGN.md
?? cmd/previewserver/
?? design/build_data.py
?? design/discovery_export.json
?? design/inline_data.py
?? design/mockups/Caddyfile
?? design/mockups/arbiter-redesign-7-transcript.html
?? design/mockups/arbiter-redesign-8-transcript-dense.html
?? design/mockups/arbiter-redesign-9-transcript-realdata.html
?? design/mockups/data-v2/
?? design/mockups/data-v3/
?? design/mockups/data/
?? design/mockups/devenv.lock
?? design/mockups/discovery-A-feed.html
?? design/mockups/discovery-B-ledger.html
?? design/mockups/discovery-C-drift.html
?? design/mockups/fonts.conf
?? design/mockups/transcript-B-ledger.html
?? design/mockups/transcript-C-timeline.html
?? design/mockups/transcript-D-merged.html

$ git log --oneline -3
35a0262 Sessions: finalize lane-row design, grounded in real data
607e542 DESIGN.md: reconcile with merged-Sessions redesign exploration (newui)
2f3a740 PICKUP.md: session handoff — #48 shipped and closed (phase 3 of #45)
```
**Nothing was committed this session.** Every untracked/modified file above
is uncommitted design-thread work (both the transcript thread from §24 and
this Discovery thread) — commit/discard is the user's call, same as every
prior mockup round.

**Note for a reader landing here from §26: this section (§25) is itself
historical** — Discovery stayed parked exactly as described below through
the Overview session that produced §26; nothing here changed since.

**What's actually done:** three fresh Discovery proposals were built
(`discovery-{A,B,C}-*.html`); A was rejected, C is undecided, B (ledger) was
iterated through several rounds — 3-state unseen/seen/ignored model,
arrow-key nav, tightened variants panel, and an enterable "workspace mode"
for reading long injected prompts line-by-line. **The user parked the
thread before validating B against real use**: *"i don't know how much
further we'll get with this without actually using it, so i think we'll
park it for now."* **Nothing in `internal/ui/*` changed this session** —
100% mockup/design work, same separation of concerns as every prior round.

**Next steps, in order:**
1. **Don't resume with more visual polish.** The user's own stated reason
   for parking is that further iteration needs real use to surface real
   friction points — resume once there's a concrete complaint from actually
   using `discovery-B-ledger.html` against live data, not before.
2. When resumed, start from `design/REDESIGN.md` §9 in full — it has the
   complete round-by-round history (3-state model, visual passes v2-v6, the
   workspace-mode feature and the real double-click bug found/fixed in it)
   so the next session doesn't redo verification already done.
3. Port `transcript-D-merged.html` into `internal/ui/*` (§24's open item 1)
   is still the nearer-term actionable item — it's marked *settled*, unlike
   Discovery which is explicitly parked mid-exploration. File a fresh issue
   before starting, don't reopen #48.
4. `DESIGN.md`'s "Session transcript" and "Discovery" page-pattern sections
   are both still stale (per §24's own item 3) — don't reconcile either
   until Discovery is actually settled, not just parked; batch both into
   one pass per that item's own instruction.

**Landmines carried from this session** (full detail in REDESIGN.md §9):
- Any inline-JSON mockup (the `<script type="application/json">` +
  `document.getElementById(...).textContent` pattern this thread uses
  throughout) MUST have its data `<script>` blocks appear **before** the
  logic `<script>` that reads them in document order — all three Discovery
  mockups initially rendered completely empty from exactly this ordering
  bug. `design/inline_data.py` has the fix pattern if a new mockup needs it.
- A row-based UI that calls a full re-render (`innerHTML` rebuild) on
  single-click selection will silently break native double-click detection
  — the first click's re-render swaps out the DOM node before the second
  click lands. Use `classList.toggle` for lightweight selection state
  instead, and bind any dblclick handler via event delegation on a stable
  parent, not per-row.
- `pgrep -fa "http.server 8090"` / `pgrep -fa chromium` before assuming the
  design/mockups static server or the headless-Chromium CDP session from
  this session are still running — don't assume either survives into a new
  session, but also don't assume they need restarting from scratch without
  checking first.

---

## 26. Session 2026-09-24 (later still) — Overview page designed and settled; board reconciled (#45/#49 closed, #52-#55 opened). Branch `newui`, nothing landed in `internal/ui`.

**This section is a pointer for the design narrative, but the board change
is real and lives here, not in `design/REDESIGN.md`** — this session's
mockup work never touched that file (it's a fresh design thread, Overview
had zero prior narrative to append to), but it did close/open real GitHub
issues, which is new relative to §24/§25's "100% mockup, nothing on the
board changed" pattern.

**Branch/state (exact, this turn):**
```
$ git status -sb
## newui
 M PICKUP.md
 M design/REDESIGN.md
?? cmd/previewserver/
?? design/build_data.py
?? design/discovery_export.json
?? design/inline_data.py
?? design/mockups/Caddyfile
?? design/mockups/arbiter-redesign-7-transcript.html
?? design/mockups/arbiter-redesign-8-transcript-dense.html
?? design/mockups/arbiter-redesign-9-transcript-realdata.html
?? design/mockups/data-v2/
?? design/mockups/data-v3/
?? design/mockups/data/
?? design/mockups/devenv.lock
?? design/mockups/discovery-A-feed.html
?? design/mockups/discovery-B-ledger.html
?? design/mockups/discovery-C-drift.html
?? design/mockups/fonts.conf
?? design/mockups/transcript-B-ledger.html
?? design/mockups/transcript-C-timeline.html
?? design/mockups/transcript-D-merged.html
?? design/overview-mockups/

$ git log --oneline -3
35a0262 Sessions: finalize lane-row design, grounded in real data
607e542 DESIGN.md: reconcile with merged-Sessions redesign exploration (newui)
2f3a740 PICKUP.md: session handoff — #48 shipped and closed (phase 3 of #45)
```
**Nothing was committed this session** — same as §24/§25, every mockup file
above is uncommitted, commit/discard is the user's call. `PICKUP.md`
modification is this section plus the header-line update; `design/REDESIGN.md`'s
modification is carried from §24/§25 (untouched this session — Overview's
narrative lives only in this PICKUP.md section and in the mockup files
themselves, since there was no existing REDESIGN.md thread for a page that
never had prior design work).

**What's actually done, in the order it happened:**

1. **Explicit instruction at session start: do not look at any existing
   Overview code, the shipped `internal/ui` templates, or prior Overview
   discussion before designing** — genuine blank-slate exploration, only
   `README.md` and read-only queries against `/data/arbiter/arbiter.db`
   allowed going in. This is the same "don't anchor on the existing UI"
   discipline `design/REDESIGN.md` §2 names as the lesson from the
   session-1 "feels" A/B/C failure — applied here from the start instead
   of learned the hard way.
2. Queried the live store for real numbers (row counts, date range, daily
   volume/cost by provider, session-level classification coverage split by
   explicit-vs-classified routing, alias→provider→model flow, per-model
   cache-hit rates, content_refs/content byte-length breakdowns by
   role/block_type). Every number in every mockup traces to one of these
   queries, not invented placeholder data.
3. Built and reviewed 4 initial directions in
   `design/overview-mockups/`: `a-compare.html` (before/after window
   picker), `b-calm-vitals.html` (rejected — "doesn't show anything
   important" as a full page), `c-flow.html` (routing-flow sankey — liked),
   `d-scorecard.html` (rejected — same problem as b, "not really discover
   or evaluate things").
4. User's actual verdict, verbatim and load-bearing: likes C's flow view
   and asked whether it generalizes to other metrics; likes A's *idea*
   (before/after window selection) not its visual presentation; confirmed
   the claude cost figures are **API-equivalent estimates, not real
   billing** (real billing is a flat $20/mo plan with a 5h/weekly quota,
   calculation method unknown, so API-cost is used as a stand-in) — this
   must never be blended with real metered cost into one undifferentiated
   number; wants cost-per-1M-tokens over cost-per-request (request sizes
   fluctuate too much for cost/request to mean anything); wants a
   delta-toggle between absolute change and volume-normalized change;
   **cache-hit percentage must be prominent** — a cache miss on a hot route
   is wasted spend and surfacing that is a primary exploration goal, not a
   nice-to-have; explicitly said "information dense... doesn't mean
   visually dense or stacked on top of each other."
5. Checked whether the sankey view generalizes past routing: yes for error
   flow (empty_response errors concentrate on claude-sonnet-5, 217/288;
   client_canceled spreads across providers; all_targets_skipped isolates
   to one route) — a stretch for cache economics, which want a
   gauge/percentage, not a flow.
6. Iterated `e-flow-compare.html` (two side-by-side diagrams + stat panels
   + delta strip), then `f-unified-flow.html` (single diagram, compare via
   ribbon re-tint instead of two diagrams, click-to-drill-down drawer per
   node replacing parallel stat panels, per-node cache-hit chips). User
   liked the information-wise direction of f but not its "as a whole"
   presentation — could not articulate why beyond "not easy to explore."
7. **`f2-unified-flow-live.html`**: user pointed out none of the clickable
   nodes actually did anything (decorative only) and the SVG only used
   half the window width. Fixed both for real — every alias/model node
   click-drills into a real per-node drawer (rate/efficiency, cache-hit
   gauge, content-mix bar where data exists, recent-errors list), full
   sankey geometry recomputed for the full panel width, compare-mode
   toggle wired to real delta figures already in the DOM.
8. **User introduced 3 external "already decided" mockups from a prior
   session this session had explicitly not been allowed to see beforehand**
   (`design/mockups/arbiter-redesign-5-lanes-realdata.html`,
   `design/mockups/transcript-D-merged.html`,
   `design/mockups/discovery-B-ledger.html`) plus `DESIGN.md`/
   `design/REDESIGN.md` as informative-not-authoritative — and asked for a
   **look-and-feel-only** pass: keep f2's structure, swap in file 9's
   `header.top`/`footer.appfoot` chrome and file 5's token vocabulary/
   spacing conventions. Produced **`f3-overview-styled.html`** — same SVG
   geometry, same click handlers, same drawer content, restyled chrome +
   colors + typography only. **This is the settled Overview design.**
9. **The $1M question, asked and answered**: build on top of #45-#50's
   shipped/mockup work, or roll back and restart? Verified via git log +
   diff (not guessed) that #46/#47/#48 shipped real, tested, lint-clean
   Go-side logic (`RoutingChain`, `foldRequestLines`/`attachTraceChildren`,
   `store.RequestDetail`, `internal/ui/toolcall.go`, `store.GuardrailDiff`)
   that no later mockup round invalidates — the thing that changed was the
   *template/CSS* layer, not the data-shape layer underneath it. Verdict:
   **build on top, port page by page, keep the commits as real history**;
   rollback would cost real working code for zero benefit.
10. **Sanity-checked that verdict against the actual data shapes** (not
    just asserted it) before acting on it — read `store.RequestRow`,
    `store.SessionSummary`, `foldRequestLines`, `attachTraceChildren`,
    `store.RequestDetail`, `transcriptBlock` directly. Confirmed: #47/#48's
    query/data shape holds up untouched under both merged-Sessions and
    transcript-D. Overview itself is a **real backend gap, not a rebuild**
    — `internal/store/pivot.go`'s `Dimension`/`Metric`/`PivotRow` has no
    cache-hit metric, no cost-per-1M metric, and no alias→model flow
    aggregate query anywhere in the codebase today.
11. **Reconciled the board against that verdict** — closed **#45** and
    **#49** (each with a comment explaining exactly what superseded it and
    pointing at the replacement, so the commit history under them stays
    legible — this was a deliberate choice over silently abandoning them),
    opened **#52** (port merged Sessions/lanes), **#53** (port
    transcript-D — explicitly does not reopen #48), **#54** (Overview:
    design sign-off + new backend metrics + port).
12. **User caught a real gap before ending the session**: file 5's token
    vocabulary (`--ink`/`--muted`/`--faint`/`--line`/`--secondary`) and
    file 9's (`--text`/`--text-dim`/`--text-faint`/`--border`/`--accent`)
    have never been reconciled into one shared set, and a *third*
    vocabulary already exists in shipped `internal/ui/static/app.css`
    (post-#46: `--ink`/`--surface`/`--secondary`). Filed **#55** to pick
    one canonical vocabulary (leaning file 9's, since Overview already
    extends it) and update `DESIGN.md` + `app.css` to match, landing
    before or alongside #52/#53/#54 since all three consume it.

**Board state at end of session** (verified via `gh issue list` this
turn, not carried from memory):

| # | state | what |
|---|---|---|
| 45 | **closed** | umbrella — superseded, see its closing comment |
| 49 | **closed** | sessions index — superseded by #52, see its closing comment |
| 50 | open, unchanged | discovery — still real backend work, untouched by this session |
| 52 | **open, new** | port merged Sessions (lanes) page |
| 53 | **open, new** | port session-transcript redesign (transcript-D) |
| 54 | **open, new** | Overview: design sign-off + backend metrics (cache-hit, cost/1M, flow query, window-compare query) + port |
| 55 | **open, new** | reconcile file-5/file-9/app.css token vocabularies — should land before or alongside #52/#53/#54 |

**Mockup files this session added** (all in
`design/overview-mockups/`, none committed): `a-compare.html`,
`b-calm-vitals.html`, `c-flow.html`, `d-scorecard.html`,
`e-flow-compare.html`, `f-unified-flow.html`, `f2-unified-flow-live.html`,
**`f3-overview-styled.html` (the settled one)**.

**Verified, not just claimed:**
- Every number in every Overview mockup traces to a live query against
  `/data/arbiter/arbiter.db` (via `devenv shell --no-tui -- sqlite3
  "file:/data/arbiter/arbiter.db?mode=ro" ...`), not placeholder data —
  same discipline §4 of `design/REDESIGN.md` established for the earlier
  mockup rounds, just not written down there since this thread doesn't
  live in that file.
- `#46/#47/#48`'s data-shape reuse claim (item 10 above) was checked by
  reading the actual struct/function definitions
  (`internal/store/reader.go`, `internal/ui/requests.go`,
  `internal/ui/sessions.go`), not inferred from commit messages or
  PICKUP.md's own prior summaries of those sessions.
- `gh issue list --state open`, `gh issue view 45`/`49`/`50` run live this
  turn before deciding what to close vs. keep vs. supersede — not assumed
  from this file's own (already-stale) §6 table.
- #45/#49 closes and #52-#55 creates all confirmed via live `gh issue
  list` output pasted into this section, not narrated from memory.

**Landmines / state for the next session:**
- **`internal/ui/*` is untouched this session, same as §24/§25** — 100%
  design work again. The Go-side port for any of #52/#53/#54 is still
  fully unstarted.
- **Two background processes from earlier mockup work may still be
  running**: a Python `http.server` on port 8090 serving
  `design/mockups/` (confirmed running via `pgrep` this turn, PID group
  under a `devenv shell` wrapper) and a headless Chromium instance on CDP
  port 9224 (also confirmed running, `--user-data-dir=/tmp/chromehome`).
  Check with `pgrep -fa "http.server 8090"` / `pgrep -fa chromium` before
  assuming either needs restarting — same caveat §9 (`design/REDESIGN.md`)
  already carries for the Discovery thread's own instance of this pattern,
  now also true for Overview's.
- **§6's open-work table above this section is stale** — it predates
  #45/#49's closure and #52-#55's creation entirely. Always run `gh issue
  list --state open` fresh rather than trusting that table now.
- **Pick #55 (token reconciliation) before or alongside starting #52,
  #53, or #54's Go port** — building any of their CSS against an
  unreconciled token set means redoing that page's CSS once #55 lands.
  User's own words on scoping this: "yes please, file that as a new
  ticket. it's gonna be fun figuring out that first one :P" — i.e.
  acknowledged as a real, somewhat annoying decision (which vocabulary
  wins) rather than a mechanical rename.
- **`design/REDESIGN.md` was not extended this session** — Overview's
  design narrative lives only in this PICKUP.md section and in the
  `design/overview-mockups/*.html` files themselves (in commit-message-
  style comments where present, mostly in the mockup content/data
  directly). If a future session wants the same level of round-by-round
  detail `REDESIGN.md` gives the transcript/discovery threads, it isn't
  there for Overview — this section is the only record.
- No ruling was made this session on whether `design/overview-mockups/`
  should be renamed into `design/mockups/` or kept separate — both
  directories currently exist side by side, untracked.

---

## 27. Session 2026-09-24g — #55 implemented: token reconciliation, shared shell, and a full old-UI rip-out (branch `newui`, nothing committed yet)

**This is the first session with real `internal/ui` code changes on this
branch.** Everything in §24-26 was design/mockup only; this session executed
#55 and, per explicit user direction, went further than the ticket's literal
scope — it deleted the *entire* old UI (HTML/CSS/JS, not Go logic) in one
pass rather than staging it behind the later page tickets.

**Branch/state:** `newui`, HEAD still `35a0262` (nothing committed this
session — same as every prior `newui` session). Run `git status -sb` and
`git log --oneline -5` for the current truth before trusting anything below.

**User's governing instructions this session (verbatim, load-bearing):**
- "i want NOTHING left of the old UI (i don't mean the go side, but the
  html/htmx/css/js side)."
- "no incremental changes, a file gets replaced, not edited... it's all or
  nothing. nothing gets deployed until the ENTIRE ui is done."
- "i DO NOT care about breaking the old ui. to me it already IS broken."
- Shared header/footer/store-disabled banner folded into #55 (it's the one
  piece every later page ticket would otherwise duplicate).
- Naming/values of new CSS tokens are the agent's call — user judges only
  the resulting look, not token names.

**What actually landed (uncommitted, on disk):**
1. `internal/ui/static/app.css` — **wholesale replacement** (not a diff,
   not a new file) with a single reconciled token vocabulary. Canonical
   names follow the *later* mockups (`transcript-D-merged.html`,
   `f3-overview-styled.html`), since Overview is already built against
   them: `--text/--text-dim/--text-faint`, `--border` + two flavors
   (`--border-soft` faded hairline, `--border-strong` solid, new — sourced
   from file 5's `--line-strong`), `--accent/--accent-strong`, plus new
   tokens `--panel-3`, `--accent-dim`, `--purple`, `--user*`,
   `--tab-active-ink`. Light theme repainted to the later mockups' warm
   neutrals (`#F5F4F1` family), replacing the old cool `#FBFBFC`.
2. `internal/ui/templates/layout.html` — rewritten with the settled shared
   shell (header/footer/store-disabled banner), ported verbatim from the
   later mockups per user's explicit fold-in request.
3. **All old page templates and partials deleted**: everything under
   `internal/ui/templates/pages/*.html` and `internal/ui/templates/partials/*.html`
   is gone. Nothing new was written to replace them — that's #50/#52/#53/#54's
   job, not #55's.
4. `internal/ui/ui.go` — `pageFiles` emptied to a comment-only slice so
   `parseTemplates()` (`template.Must` + `readFile`) doesn't panic at
   handler construction and `go build`/`go test` keep running.
5. Render-dependent tests deleted or trimmed across the package (not
   skipped — user's explicit choice) so `go test ./...` is green modulo
   the **5 pre-existing, unrelated failures** that predate this session
   (verified via `git stash`): `TestShippedArbiterYAMLLoads`,
   `TestShippedTitlePatternsAreTheIntendedRegexes` (both in
   `internal/config`, a `litellm` endpoint config issue), and
   `TestSeriesEndpointShape` (in `internal/ui`, pre-existing chart-data
   assertion failure unrelated to the template rip-out). One test
   (`TestStaticAssetsAreServedAndVersioned`) was fixed rather than deleted
   — it asserted the literal string `--ink` in `app.css`, which is gone
   under the new vocabulary; changed the assertion to `--text`.
   Files fully or partially touched: `discovery_test.go` (deleted),
   `sessions_test.go` (deleted — every remaining test in it turned out to
   render through the now-gone `session`/`sessions` templates),
   `toolcall_transcript_test.go` (deleted), `request_guardrailed_toggle_test.go`
   (deleted), `session_guardrailed_test.go` (deleted),
   `grouping_tail_test.go`, `grouping_test.go`, `guardrail_diff_test.go`,
   `live_test.go`, `overview_test.go`, `ui_test.go`, `series_test.go`,
   `cmd/arbiter/admin_route_test.go` (each trimmed of just the
   render-dependent cases, non-render tests kept intact).
6. `go build ./...`, `go vet ./...` both clean. `go test ./...` matches the
   pre-existing baseline exactly (same 3 failures as `git stash` shows on
   unmodified `35a0262`) — **no new test failures were introduced.**

**⚠️ THE ONE LOAD-BEARING NOTE FOR THE NEXT SESSION — read before touching
`internal/ui/*.go` handlers:**

> **Go handlers that render the now-deleted templates will break at render
> time.** `pageFiles` is empty and every page/partial template file is
> gone, but the handler code in `internal/ui/{requests,sessions,session,
> overview,discovery,block}.go` (and whichever others call `h.render`/
> `ExecuteTemplate` against a page name) is untouched and will still try to
> execute a template set that no longer has that page in it. At runtime
> this surfaces as `html/template: "<page>" is undefined` (already visible
> in this session's own `go test ./...` output, logged via the error
> pipeline during `internal/ui` package tests that exercise the tail/live
> path) rather than a compile error, because Go does not validate
   `html/template` bodies at build time — only `template.Must`'s *parse*
  step is checked eagerly, and an empty `pageFiles` means there's nothing
  left to even attempt parsing.
>
> **This is expected and intentional for the current state of the branch,
> not a regression to fix.** #55 was scoped as tokens+shell+shared chrome
> only; wiring real templates back in per page is #50 (discovery),
> #52 (sessions/requests/detail), #53 (transcript), #54 (overview). Do not
> "fix" the broken handlers by writing throwaway templates just to make
> `go test`/manual smoke-testing quiet — that would be exactly the kind of
> incremental intermediate state the user explicitly ruled out ("nothing
> gets deployed until the ENTIRE ui is done"). The correct fix is to build
> that page's real template as its own ticket's actual deliverable.

**User's green light closing this session:** "the one note, and then you
have a green light `Go handlers that render the now-deleted templates will
break at render time` yes, just make sure it is noted so the session that
picks those up knows. thats it, go ahead." — i.e. this note *is* the
condition of the green light, not an FYI to skim.

**Next steps, in order:**
1. Review/commit this session's changes (nothing is committed yet — that's
   the user's call, same convention as every prior `newui` session).
2. Pick up #50, #52, #53, or #54 in any order — each ports one page's real
   template + Go view wiring against the now-canonical token set and shell.
   Until at least one of them lands, the admin UI's page routes 500 on
   render (see the note above) — this is fine for a branch, not for `develop`.
3. Nothing else from #55 is outstanding; treat it as functionally done
   pending the commit itself.

**Landmines carried from this session:**
- `internal/ui/static/{live.js,popover.js,showmore.js,chart.js,
  THIRD_PARTY.md,LICENSE.uplot}` (old JS assets) were **not touched** —
  rip-out candidates for whichever ticket first needs to replace or drop
  them; #55 only covered CSS/HTML per its own scope.
- The exact border-token split (`--border-soft` vs `--border-strong`) has
  a real semantic difference (faded hairline vs. solid interactive-control
  outline) — don't collapse them back to one token in a later page's CSS
  without checking which the mockup for that specific element actually used.

---

## 28. Session 2026-09-25 — #52 implemented: Sessions page (lanes + detail panel) ported (branch `newui`, nothing committed yet)

**Branch/state (exact, this turn):** `newui`, HEAD `35a0262` (same commit
§27 left it at — this session's work, like every prior one in this thread,
is uncommitted). `git status --short` shows the full old-UI-deletion diff
from #55 (untouched, carried over) plus this session's additions:
`internal/ui/sessions.go`, `internal/ui/ui.go`,
`internal/ui/templates/layout.html`, `internal/ui/templates/pages/sessions.html`,
`internal/ui/static/app.css` modified; `internal/ui/templates/partials/laneRow.html`
and `internal/ui/static/laneDetail.js` new; `internal/ui/sessions_test.go` new.

**User's instruction this session (verbatim, governs #50/#52/#53/#54 as a
batch):** "this is a complete redesign. nothing of the old UI remains. you
build on what #55 prepared, and what is in the prototypes. if you have a
question, ask me. at the end dont close the ticket. i will do all of them
until i habe a working ui, then test, then come bsck for detsils." —
**do not close #52** (or any of #50/#53/#54) even though the port below is
functionally complete; the user closes them together after testing the
whole UI.

**What landed:**
- `internal/ui/templates/pages/sessions.html` + new
  `internal/ui/templates/partials/laneRow.html` — ported from
  `design/mockups/arbiter-redesign-5-lanes-realdata.html` verbatim in
  layout/behavior; token names translated to app.css's post-#55 vocabulary
  (`--panel`/`--border`/`--text-dim` etc, not the mockup's own `--muted`/
  `--line` names).
- `internal/ui/sessions.go`'s `SessionsHandler` rewritten: still queries
  `Reader.Sessions` for the aggregate list and `Reader.ListRequests` +
  `foldRequestLines`/`attachTraceChildren` per session for that lane's
  timeline — **no new store query**, exactly as #52's issue body scoped it.
  Added `laneRow` (wraps `store.SessionSummary` + folded `Lines` +
  `SatelliteCount` + `Preview`/`PreviewNote`) and `lanePreview` (finds the
  session's earliest `kind="client"` row and its first user-role content
  block via the existing `ContentForRequest`, capped at `previewBytes`).
  The old `sessionRow`/`sessionsView` shape from before #55's UI rip-out is
  gone; this is a clean rebuild, not a restoration — per the user's
  "nothing of the old UI remains" instruction, historical templates
  (`0cd748f:.../sessionrow.html`, `16c9af2:.../pages/sessions.html`,
  `76fb7ea:.../reqrow.html`) were read for reference only, never restored.
- **Detail-panel interaction is fully client-side, no new endpoint.** Every
  node (`.node.client`, `.node.sat`, `.node.stack`) carries its own facts as
  `data-*` attributes, populated from the same `requestRowView`/
  `requestLineView` the timeline already renders — nothing the panel shows
  is fetched a second time. New `internal/ui/static/laneDetail.js`
  (same delegated-listener pattern as `popover.js`/`showmore.js`) swaps
  `#detailCol`'s innerHTML on a node click/Enter/Space, restores the
  default aggregate view on Escape or a background click, and toggles
  `.lane-row.selected`/`.node.is-selected` to match the mockup's visual
  state. This was a **design fork resolved without waiting on an answer**:
  asked the user whether node clicks should navigate to the existing
  request-detail page instead (less new code, breaks the mockup's "never
  navigates away" panel) or use a client-side data-attribute swap (matches
  the mockup exactly, zero new server surface) — no reply came, so the
  second option shipped since it requires no new endpoint and is strictly
  closer to the prototype. **Revisit this if the user actually wanted
  server-rendered panel content** (e.g. richer facts than what the
  timeline already carries per row).
- `internal/ui/static/app.css` gained every Sessions-page rule (`.lanes-col`,
  `.toolbar`, `.lane-row`/`.lane-head`/`.lane-timeline`, `.node` and its
  `client`/`sat`/`stack` variants, `.detail-col`/`.default-panel`/
  `.node-panel`, `.legend`, `.empty`/`.capped`) — status-color classes reuse
  `statusClass`'s existing `s-ok`/`s-warn`/`s-err`/`s-note` (format.go)
  rather than inventing new ones, so a node's ring color is driven by the
  same function the flat requests page already used pre-#55.
- `internal/ui/ui.go`'s `pageFiles` now `[]string{"sessions"}` (was `[]string{}`);
  comment updated to describe incremental greenfielding instead of the
  empty-slice state.
- `internal/ui/templates/layout.html` gained a `<script>` tag for
  `laneDetail.js`, loaded on every page (cheap, delegated, inert until a
  `.node` exists in the DOM) rather than only on Sessions — same pattern
  `popover.js`/`showmore.js` already use.
- New `internal/ui/sessions_test.go`:
  `TestSessionsLaneRendersRequestsAndSatellites` (seeds a client+classifier
  pair sharing a trace_id plus a second, separate errored lane; asserts
  both session ids render, the satellite gets `node sat classifier`, the
  client gets `node client`, the errored lane's `s-err` status class
  appears, and the default panel renders) and
  `TestSessionsLanePreviewNoteWhenCaptureOff` (capture defaults off on a
  fresh test Handler; asserts the lane header shows the "content capture is
  off" note rather than an empty preview).

**Verified, not just written:**
```
$ devenv shell --no-tui -- go build ./...          # clean
$ devenv shell --no-tui -- go vet ./...            # clean
$ devenv shell --no-tui -- go test ./...
ok    .../cmd/arbiter
ok    .../internal/classifier
FAIL  .../internal/config   (2 pre-existing failures — see below, not from this session)
ok    .../internal/guardrail
ok    .../internal/http
ok    .../internal/pipeline
ok    .../internal/router
ok    .../internal/store
ok    .../internal/translator
FAIL  .../internal/ui       (1 pre-existing failure — TestSeriesEndpointShape — see below)
ok    .../internal/upstream
ok    .../pkg/types
```
The two `internal/config` failures (`TestShippedArbiterYAMLLoads`,
`TestShippedTitlePatternsAreTheIntendedRegexes`, both
`CONFIG_ERROR: provider "litellm": missing endpoint`) and the one
`internal/ui` failure (`TestSeriesEndpointShape`, "no x values") **predate
this session** — confirmed present before any of this session's edits by
running the full suite first; unrelated to Sessions/#52. The
`html/template: "req-line" is undefined` log lines are expected render-path
noise from the still-deleted requests page (`req-rows`/`req-line` partials
land with #53/#54's own port) — not a new regression, and not present for
the sessions page itself, which now renders cleanly (see the new
sessions_test.go passing).

**Next steps, in order (per the user's "i will do all of them" plan):**
1. Pick up #50 (Discovery), #53 (transcript), or #54 (Overview) next, in any
   order — each ports one more page the same way #52 just did.
2. **Do not close #52.** The user is batching #50/#52/#53/#54, testing the
   whole UI once all four land, then closing them together — this was
   stated explicitly this session.
3. Once all four page ports land, revisit the node-click design fork noted
   above if the user has an opinion on it by then (asked, no answer yet).
4. Nothing else outstanding from this session — build/vet/test all verified
   clean modulo the three pre-existing failures documented above.

## 29. Session 2026-09-25b — #52 feedback round 2 addressed: toolbar chrome, real "N active" stat (cache-TTL-derived)

**Branch/state:** `newui`, still HEAD `35a0262`, still nothing committed.
Continues §28 directly (same open ticket, same "don't close" instruction).

**User feedback this round (verbatim, three items):**
1. "also remove the Sessions word, not just the paragraph."
2. "i can switch between errors, and all... but client only still behaves
   different... and unexpected."
3. "no, that was intentional placeholder during the mockup. now that is for
   this session. not the entire bar. JUST the sessions count." — re: the
   nav bar's `3 active` chip, previously a hardcoded mockup placeholder.
   Follow-up, focused: "if you are making a new query, an 'active' session
   is one where the last query was within it's cache ttl, if that is
   possible."

**What landed:**
- **Item 1** — removed the entire `pagehead` block (`<h1>Sessions</h1>` +
  the lede paragraph) from `sessions.html`; deleted the now-dead
  `.pagehead`/`.pagehead h1`/`.pagehead .lede` CSS rules from `app.css`.
  The page now opens directly on the toolbar, matching "nothing of the old
  UI remains" — no vestigial page title anywhere.
- **Item 2 (toggle bug)** — root cause: `sessions.go`'s `ErrorsOnly` was
  `q.Has("errors")`, which is `true` for *both* radio states, since the
  toolbar's "all" radio still submits `errors=` (present, empty value) not
  an absent key. Fixed to `q.Get("errors") == "1"`. Added
  `TestSessionsErrorsFilterRoundTrips` (round-trips all → errors-only → all
  again) as a regression test.
  - **`client_only` itself was re-verified correct** via a standalone debug
    binary + `wget` probes (browser tools can't reach the loopback debug
    server — blocked as a private address): direct HTTP comparisons of
    `?client_only=1` vs default across every `errors`×`client_only`
    combination show the server-side filter and satellite-node hiding are
    both correct. **However, this dig surfaced a real, separate bug**:
    `Reader.Sessions` hard-filters its own top-level query to
    `kind = 'client'` (reader.go:481) — a session whose *only* row is a
    `classifier`-kind request (no client row at all) **never appears in the
    session list, with or without `client_only`**. This wasn't what the
    user reported, but it's adjacent and worth a look if "client only
    behaves unexpected" persists — **not fixed this session**, flagging for
    next pass. Repro: seed a session with a lone `kind="classifier"` event,
    no `kind="client"` sibling; it's invisible on `/admin/ui/sessions`
    regardless of `client_only`.
- **Item 3 — "N active" is now a real, live count**, wired through a chain
  of changes:
  - `Handler.base(ctx, active)` gained a `context.Context` parameter (was
    just `active string`) so it can run a real query per page render. All
    five call sites (`discovery.go`×2, `overview.go`, `requests.go`×2,
    `sessions.go`×2) updated to pass `r.Context()`; test call sites in
    `ui_test.go` updated to pass `context.Background()`.
  - **Definition settled with the user, specifically**: "active" = a
    session whose most recent query is still inside its **prompt-cache
    TTL** — i.e. still pinned to the same provider/model by
    `internal/pipeline/affinity.go`'s routing-affinity mechanism, so the
    next request on that session would reuse the cache. This was chosen
    over the naive "≥1 request in the last 24h" definition (rejected as
    arbitrary and unrelated to what "active" should mean here).
  - `internal/store/reader.go`'s `ActiveSessionCount` rewritten to count
    live rows in `affinity_pins` (`expires_at > now()`, one row per pinned
    session — see `internal/store/affinity.go`), replacing whatever
    placeholder/window-based logic it had before. Doc comment updated to
    explain the cache-TTL framing and point at `pipeline.affinityStore`.
  - **Scope respected**: only the Sessions nav item's stat changed — "not
    the entire bar." The other nav items' `$X/24h`-style stats are
    untouched.
  - New tests: `TestActiveSessionCount` (store-level — seeds a live pin and
    an expired pin, asserts the count is 1, not 2) in
    `internal/store/affinity_test.go`; `TestNavSessionsStatReflectsActivePins`
    (UI-level — seeds one live pin via a real `SQLiteWriter`, opens a fresh
    reader against that DB, renders `/admin/ui/sessions`, and asserts the
    nav markup literally reads `<span class="stat">1<span class="u">active</span></span>`)
    in `internal/ui/sessions_test.go`.

**Verified, not just written:**
```
$ devenv shell --no-tui -- go build ./...          # clean
$ devenv shell --no-tui -- go vet ./...             # clean
$ devenv shell --no-tui -- go test ./...
```
Same three pre-existing failures as §28 (`internal/config`×2,
`TestSeriesEndpointShape`) — nothing new. New/changed tests
(`TestSessionsErrorsFilterRoundTrips`, `TestActiveSessionCount`,
`TestNavSessionsStatReflectsActivePins`) all pass individually with `-v`.

**Cleanup done this session:** removed scratch debug binaries/DBs
(`cmd/uidebug`, `/tmp/seed*.db`, `/tmp/seed*.go`, `/tmp/uidebug*`,
`/tmp/arbiter-test.yaml`, `/tmp/tplcheck`) and killed the stray background
debug-server processes left running on `:8097`/`:8098`/`:8099`. No scratch
artifacts remain in the tree; `git status --short` shows only the real
diff (§28's files, unchanged file list, no new untracked debug code).

**Next steps, in order:**
1. **Do not close #52.** Still the user's explicit instruction — they batch
   #50/#52/#53/#54 and close together after testing.
2. Flagged bug (not fixed): `Reader.Sessions`'s hard `kind = 'client'`
   filter makes classifier-only sessions invisible outright, independent of
   `client_only`. Worth asking the user if this is part of what "client
   only... unexpected" meant, or a separate latent bug to fix regardless.
3. Node-click design fork from §28 (client-side panel vs. navigate-to-detail)
   still unanswered — revisit once the user circles back.
4. Continue with #50/#53/#54 page ports per the batch plan.

## 30. Session 2026-09-25c — #52 feedback round 3: lane-list scroll direction fixed (vertical bounded, horizontal compressed instead of clipped)

**Branch/state:** `newui`, still HEAD `35a0262`, still nothing committed.
Continues §28/§29 directly (same open ticket).

**User feedback this round (verbatim):** "the lanes area doesn't scroll
vertically if there are more lanes than fit on the screen. the lanes area
DOES scroll horizontally for long sessions, which it should not. a lane is
the width of the lanes area, all the events need to be compressed onto that
width."

**Root causes found (both are the same class of bug — a broken flex-sizing
chain, not a missing scroll rule):**
- **Vertical scroll dead:** `.lane-scroll { flex: 1; overflow-y: auto; }`
  only sizes correctly if its *parent* is a flex container so `flex: 1` has
  something to size against. Its actual parent, `#lane-list` (the whole
  htmx-swapped fragment — toolbar through the legend), was a plain `<div>`,
  not `display: flex`. So `.lane-scroll`'s `flex: 1` was inert, the list
  just grew `.lanes-col`/`.main`/the page to fit its content, and *the
  toolbar scrolled away with the lanes* instead of staying pinned above a
  bounded, independently-scrolling list.
- **Horizontal scroll present when it shouldn't be:** every timeline node
  (`.node.client`/`.node.sat`/`.node.stack`) had a fixed pixel width and
  right-margin, so a lane with many requests (the mockup's own 342-request
  example) simply grew wider than `.lane-timeline`'s box with nothing
  compressing it — `.lane-timeline` had no `overflow` rule at all, so the
  browser's default let the row overflow sideways instead of clipping or
  shrinking.

**What landed:**
- `internal/ui/static/app.css`:
  - `.lanes-col` gained `min-height: 0` (needed so a flex child can actually
    shrink below its content size — the classic flexbox scroll-container
    gotcha).
  - New `#lane-list { flex: 1; display: flex; flex-direction: column;
    min-height: 0; }` — makes the swapped fragment itself a flex column, so
    `.toolbar` (now explicitly `flex: 0 0 auto`) stays pinned and
    `.lane-scroll`'s `flex: 1; min-height: 0; overflow-y: auto` now has a
    real bounded parent to size against. This selector survives htmx's
    `outerHTML` swap on `#lane-list` unchanged (the id round-trips through
    every filter change — confirmed against `laneRow.html`'s own comment
    describing the swap).
  - `.lane-scroll` gained `overflow-x: hidden` as a hard backstop — even if
    the compression below is ever wrong for some pathological lane, the
    lane list itself must never grow a horizontal scrollbar.
  - `.lane-timeline` gained `max-width: 100%; overflow: hidden` (same
    backstop reasoning, one level down).
  - **Every node's size and margin is now `calc(Npx * var(--node-scale,
    1))`** instead of a bare pixel value (`.node.client`, `.node.sat`
    including its `::after` stem, `.node.stack`) — `--node-scale` defaults
    to `1` (a lane with few nodes renders at exactly its old, natural
    size), and a script sets it per-lane when the lane would otherwise
    overflow. This was chosen over just clipping/eliding nodes because the
    user's ask was explicit: "all the events need to be compressed onto
    that width," not "cut off the ones that don't fit."
- New `internal/ui/static/laneTimeline.js`: for every `.lane-row`, resets
  `--node-scale`, measures `.lane-timeline`'s natural width
  (`scrollWidth`, which ignores the new `overflow: hidden` clip) against
  its available width (`clientWidth`), and — only if natural > available —
  sets `--node-scale` to `available / natural` (floored at `0.28` so a
  session with hundreds of requests, like the mockup's 342-request
  example, shrinks to something dense-but-still-clickable rather than
  zero-size dots; past that floor the existing stream-collapsing in
  `foldRequestLines` is the real fix, not further compression). Runs on
  `DOMContentLoaded`/immediately, on every `htmx:afterSettle` (post-layout,
  matching `chart.js`'s own documented reasoning for why `afterSettle` and
  not `afterSwap`), and via a `ResizeObserver` on `.lanes-col` so a window
  or panel resize re-fits without a resize listener — same lifecycle
  pattern `chart.js` already established for htmx-swapped, measurement-
  dependent content.
- `internal/ui/templates/layout.html`: added the `<script>` tag for
  `laneTimeline.js`, loaded on every page like `laneDetail.js`/`popover.js`
  (cheap, inert until `.lane-row` exists in the DOM).

**Verified, not just written:**
```
$ devenv shell --no-tui -- go build ./...          # clean
$ devenv shell --no-tui -- go vet ./...             # clean
$ devenv shell --no-tui -- go test ./...
```
Same three pre-existing failures as §28/§29 — nothing new; existing Sessions
tests (`TestSessionsLaneRendersRequestsAndSatellites`,
`TestSessionsLanePreviewNoteWhenCaptureOff`,
`TestSessionsErrorsFilterRoundTrips`) all still pass unchanged, since this
round is CSS/JS-only (no Go, no template-data changes). Manually verified
via a standalone debug binary (`/tmp/uidebug4`, `wget` against
`127.0.0.1:8096`, deleted after use — see §28/§29 for why browser tools
can't reach loopback) that `laneTimeline.js` and the updated `app.css` are
actually served (embedded via `//go:embed static`, picked up automatically —
confirmed via `TestStaticAssetsAreServedAndVersioned` still passing) and
that a seeded 60-request session renders all 60 client nodes in the
server-side markup (`grep -c '"node client' `on the response = 60,
excluding satellites). **Not independently confirmed in an actual browser
paint** (no headless browser available in this devenv — flagged, not
skipped): the fit math and CSS chain were verified by direct inspection of
computed values (`scrollWidth`/`clientWidth`/`calc()` semantics), not a
rendered screenshot. If the compression still looks off visually, that's
the next thing to check.

**Next steps, in order:**
1. **Do not close #52.** Still the user's explicit instruction.
2. This round was purely a layout/CSS-JS fix — no design fork, no open
   question. If the compression math needs tuning (a different floor, or
   scaling font-size on the stack node's `.stackn` label along with the
   node itself, which was **not** touched this round), that's a follow-up.
3. Items carried over from §29, still open: the `Reader.Sessions` hard
   `kind = 'client'` filter (classifier-only sessions invisible outright),
   the node-click design fork (§28).
4. Continue with #50/#53/#54 page ports per the batch plan.




---

## 31. Session 2026-09-25d — §28-30's Sessions/#52 work committed (branch `newui`, now at `77c37b5`)

**Orient (run these, don't trust the numbers below):**
```bash
git status -sb          # newui, clean except design/REDESIGN.md (pre-existing, see below)
git log --oneline -3    # 77c37b5 is HEAD
gh issue list --state open --limit 5   # needs devenv; #52 still open
```

**What happened:** nothing new was built this session — this was purely
"commit what §28-30 already implemented and verified." The prior three
sessions (lane view port, feedback round 2, feedback round 3's scroll
fix) had accumulated on disk without a single commit across all of them.

**What's in `77c37b5`:** exactly the 16 files touched by §28-30 — lane
view model/handler/templates/CSS/JS, the `active`-stat and errors-toggle
fixes, the flex-sizing/node-compression scroll fix, `PICKUP.md` itself,
and the supporting store/test changes. Full list and rationale in the
commit body (`git show --stat 77c37b5`, `git show 77c37b5` for the full
message).

**Deliberately left out of the commit — do not assume these are related
to #52:** `design/REDESIGN.md` (modified) and a batch of untracked files
(`cmd/previewserver/`, `design/build_data.py`, `design/discovery_export.json`,
`design/inline_data.py`, `design/mockups/{Caddyfile,arbiter-redesign-7/8/9-*.html,data,data-v2,data-v3,devenv.lock,discovery-{A,B,C}-*.html,fonts.conf,transcript-{B,C,D}-*.html}`,
`design/overview-mockups/`) — these predate this session (exploration
artifacts for #50/#53/#54's own design threads, per §24-26) and are the
user's to commit or discard when those tickets land. Verify with
`git log -1 --format=%ci -- design/REDESIGN.md` before assuming otherwise
— it was last touched well before this session.

**One fix made during the commit attempt:** `golangci-lint` (run via the
repo's pre-commit hook, which needs `devenv shell` for `go` to be on
`PATH` — a bare `git commit` fails the hook with "go: executable file not
found") caught `whichProviderA` in `internal/ui/ui_test.go` as unused —
a leftover from §28's test-file rewrite that removed its last usage.
Deleted the dead const; re-ran build/vet/test (still only the same 3
pre-existing failures: `internal/config` litellm-endpoint ×2,
`TestSeriesEndpointShape`) before re-committing.

**Verified, not just written:**
```
$ devenv shell --no-tui -- go build ./...    # clean
$ devenv shell --no-tui -- go vet ./...      # clean
$ devenv shell --no-tui -- go test ./...     # same 3 pre-existing failures, nothing new
$ devenv shell --no-tui -- git commit ...    # pre-commit hooks all passed: gitleaks,
                                              # golangci-lint, ripsecrets, trufflehog
```

**State:** branch `newui` has **no upstream configured**
(`git rev-parse --abbrev-ref --symbolic-full-name @{u}` fails) — this
branch has never been pushed. First push needs
`git push -u origin newui`. Not done this session; pushing is the user's
call.

**Next steps, in order:**
1. **Do not close #52.** Still the user's explicit instruction — they're
   batching #50/#52/#53/#54, testing, then closing together.
2. Carried over, still open: `Reader.Sessions`' hard `kind = 'client'`
   filter hides classifier-only sessions outright (§29); the node-click
   interaction design fork, client-side panel vs. navigate-to-detail
   (§28); node-compression follow-ups like scaling the stack node's
   `.stackn` label font-size (§30).
3. Continue with #50/#53/#54 page ports per the batch plan — nothing
   landed on any of those three yet (confirm with `git log` before
   trusting that, per this file's own self-distrust rule).
4. Branch `newui` is unpushed; ask before pushing.


---

## 32. Session 2026-09-25e — #53 (session transcript) implemented, 5 live bugs fixed, committed `9faebe6`

**Orient (run these, don't trust the numbers below):**
```bash
git status -sb          # newui, should be clean of #53's own files
git log --oneline -3    # 9faebe6 should be HEAD
gh issue list --state open --limit 5   # needs devenv; #50/#52/#53/#54 still open (batched)
```

**What happened:** #53 (session transcript / detail page,
`GET /admin/ui/session?key=`) was implemented from scratch this session —
`internal/ui/transcript.go`, the three new templates, `transcript.js`,
~360 lines of `app.css`, plus store/format/toolcall support — then tested
against the user's **real production DB** (not synthetic fixtures) through
several rounds of live feedback. Five bugs got reported and fixed; see the
commit body of `9faebe6` (`git show -s --format=%B 9faebe6`) for the full
per-bug root-cause writeup — don't duplicate it here, but the short version:

1. **List pane wouldn't scroll** — not a flexbox issue. `app.css:553`'s
   comment literally contained `*/` (`s-*/dot`), closing the CSS comment
   early and silently dropping the very next rule (`.transcript`'s
   `display:flex`). **When a CSS rule visibly in the source has no effect
   in the browser, check comment balance before anything else.**
2. **"Show full" did nothing** — `max-height`/`overflow:hidden` were on
   the *unconditional* base rule, not scoped to the `.clamped` class the
   JS was toggling. **A class-toggle interaction is only as good as which
   rule actually reads that class.**
3. **List rows looked cramped vs. the mockup**, despite every measured
   CSS value (padding, grid columns, font-size) already matching the
   mockup exactly. The mockup's own rows render as 2-3 stacked lines
   because of a name collision — its page-level `.main` (`flex-direction:
   column`) accidentally leaks into `.row .main` (a list row's content
   span) since an isolated single-file mockup has no namespacing. Fixed
   by reproducing the *visual result* deliberately and scoped
   (`.entry .row .main { flex-direction: column }`), not by copying the
   mockup's colliding rule. **An approved mockup's rendered look and its
   source are different contracts — when they conflict, matching pixels
   without importing the mockup's own cross-scope bugs into shared
   vocabulary is the right call, and the user confirmed this explicitly.**
4. Right-side cost/latency column width jumped row to row — each `.row`
   is its own grid container, so an auto-sized `.side` track sizes to
   that row's own content. Pinned `.side` to a fixed width.
5. List showed an identical user-message preview on every row — the
   "what's new this turn" computation was wrong. Fixed via
   `newestRequestMessage`'s last-request-message-index approach.

**Also, twice this session:** an incorrect "stale binary" theory got
raised for early symptoms and the user shut it down hard both times
(**"I RECOMPILE EVERY FUCKING TIME"**). The setup is a **bind mount** —
compiles inside this container, runs the resulting binary on the host —
confirmed by MD5-matching a sandbox rebuild against the user's host
binary. **Never suggest staleness in this repo; verify the actual
rendering pipeline (parser → cascade → DOM) instead.**

**Tooling built this session, kept on disk but deliberately NOT
committed** (user's explicit call — useful for reuse, not part of the
shipped feature): `cmd/bigpreview/` (synthetic 20+-turn session
generator, for exercising pagination/scroll without real data) and
`cmd/realpreview/` (read-only webserver on `:8097` against the real
`/data/arbiter/arbiter.db` — safe because the store opens WAL mode, so a
read-only reader can run alongside the live writer). `cmd/previewserver/`
remains pre-existing/unrelated, per `design/REDESIGN.md`'s own note.

**CDP-based visual verification workflow** (new this session, worth
reusing for #50/#54): headless Chromium via
`devenv --option packages:pkgs "chromium fontconfig dejavu_fonts" shell --
 -- chromium --headless=new --remote-debugging-port=9222
 --remote-debugging-address=127.0.0.1`, driven with a small Python
`websockets` CDP client (`devenv --option packages:pkgs "…" shell --
python3 …`) to read actual `getComputedStyle`/`getBoundingClientRect`
values and dispatch real click/keyboard events — not just screenshots.
**The vision model was unreliable for exact pixel/color claims this
session** (self-contradicted on a background color across two calls);
`convert '%[pixel:p{x,y}]'` (ImageMagick, via
`devenv --option packages:pkgs "imagemagick"`) was the tiebreaker.
**Resource limit**: this container caps concurrent processes — more than
~10 Chromium instances running at once caused
`fork: retry: Resource temporarily unavailable` and forced the user to
manually kill a pile of leftover sessions. Keep concurrent Chromium ≤~10
and kill each one once its screenshot/measurement is captured; don't leave
background CDP browsers running across many rounds of iteration.

**Verified, not just written:**
```
$ devenv shell --no-tui -- go build ./...    # clean
$ devenv shell --no-tui -- go vet ./...      # clean
$ devenv shell --no-tui -- go test ./internal/ui/...  # only TestSeriesEndpointShape
                                              # fails — pre-existing, unrelated
                                              # (req-line template, overview chart)
$ devenv shell --no-tui -- git commit ...    # pre-commit hooks all passed: gitleaks,
                                              # golangci-lint, ripsecrets, trufflehog
```
All 5 bug fixes were also verified **live against the real DB**
(session `20260925_000937_c52312`) via the CDP driver, not just the test
suite — computed styles/DOM state read before and after each interaction.

**State:** branch `newui` still has **no upstream configured** — unpushed.
`9faebe6` is on top of `77c37b5`. `design/REDESIGN.md` §8 was extended
in the same commit with the full bug-hunt narrative (more detail than
here); §7/§9 (rejected transcript direction, Discovery/#50 parked thread)
were folded in from untracked working notes that predated this session —
unrelated to #53's own code, kept in the same design-log file. Design
exploration artifacts specific to #50 (Discovery) and #54 (Overview) —
`design/mockups/discovery-*.html`, `design/overview-mockups/`,
`design/build_data.py`, `design/discovery_export.json`,
`design/inline_data.py`, `design/mockups/data*/` — remain **untracked,
deliberately not committed**, same convention as `77c37b5`: they're the
user's to commit or discard when those tickets land.

**Next steps, in order:**
1. **Do not close #53 (or #50/#52/#54).** Still batched — user tests the
   whole UI, then closes together.
2. #50 (Discovery) and #54 (Overview) haven't been ported into the app
   yet — design threads exist in `design/REDESIGN.md` §9 (Discovery,
   parked mid-exploration per the user's own words) but nothing has
   landed in `internal/ui/` for either. Confirm with `git log` before
   trusting that, per this file's own self-distrust rule.
3. Carried over from §28-30, still open: `Reader.Sessions`' hard
   `kind = 'client'` filter hides classifier-only sessions outright; the
   node-click interaction design fork (client-side panel vs.
   navigate-to-detail).
4. Branch `newui` is unpushed; ask before pushing.


---

## 33. Session 2026-09-25f — handoff refresh only, nothing new built (branch `newui`, HEAD `99bac9a`)

**Orient (run these, don't trust the numbers below):**
```bash
git status -sb          # newui, no upstream; untracked-only (design/#50/#54 exploration + throwaway cmd/ tools)
git log --oneline -5    # 99bac9a is HEAD
gh issue list --state open --limit 10   # needs devenv
```

**What happened:** no code changed this session — §32's #53 work was
already committed (`9faebe6`, then PICKUP.md's own §32 addendum in
`99bac9a`) before this session started. This entry exists to correct one
thing §32 could mislead on and to re-verify state with live commands,
per this skill's own rule against trusting an earlier turn's numbers.

**Correction:** §27 and earlier turns treated #55 as closed. It is not —
`gh issue view 55` shows `"state":"OPEN"`. Same batching rule as
#50/#52/#53/#54: don't close it. (The `572443e` commit message uses
"#55" in its subject line, which is why this drifted — a commit
referencing a ticket is not the same as the ticket being closed.)

**Verified, live, this session:**
```
$ devenv shell --no-tui -- go build ./...   # clean
$ devenv shell --no-tui -- go vet ./...     # clean
$ devenv shell --no-tui -- go test ./...    # 3 pre-existing failures, nothing new:
    internal/config: TestShippedArbiterYAMLLoads, TestShippedTitlePatternsAreTheIntendedRegexes
      (both "provider litellm: missing endpoint" — arbiter.yaml's shipped
      sample config, unrelated to any UI work)
    internal/ui: TestSeriesEndpointShape ("no x values" — Overview/#54's
      series chart, not yet implemented; unrelated to #53)
```
All three were already documented as pre-existing in §31/§32 — re-run here
to confirm #53's commits introduced nothing new, not to re-diagnose them.

**Open work — do not trust the table in §6, it predates #55/#52/#53 all
landing (dated `b8907e8`).** Run `gh issue list --state open` instead.
As of this session: **#50, #51, #52, #53, #54, #55** are the design/newui
threads still open (all batched except #51, which is an unrelated feature
request); **#38, #40, #41, #44** are older unrelated bugs, still open.
#50 and #54 have settled designs in `design/REDESIGN.md` (§9 and an
earlier overview section respectively) but **nothing ported into
`internal/ui/` for either yet** — confirmed via `git log`, not assumed.

**Nothing new is in flight.** No uncommitted code, no partial work. The
untracked files (`cmd/bigpreview/`, `cmd/realpreview/`, `cmd/previewserver/`,
and the `design/mockups/`, `design/overview-mockups/`, `design/build_data.py`
etc. exploration artifacts) are unchanged from §32 — same disposition:
first two are throwaway dev tooling kept on disk by explicit user request,
not committed; `cmd/previewserver/` is pre-existing/unrelated; the design
artifacts are #50/#54's own working files, the user's to commit when those
land.

**Next steps, in order:**
1. Do not close #50/#52/#53/#54/#55 — still batched, pending the user's
   own full-UI test pass.
2. #50 (Discovery) or #54 (Overview) are the two remaining unported pages
   in the batch. Read `design/REDESIGN.md` §9 (Discovery, explicitly
   parked — *"i don't know how much further we'll get with this without
   actually using it"*, don't resume with more speculative polish) before
   picking either up.
3. Branch `newui` still has no upstream configured — ask before pushing.

## 34. Session 2026-09-25g — #50 (Discovery) implemented end-to-end (branch `newui`, nothing committed yet)

**Orient (run these, don't trust the numbers below):**
```bash
git status -sb          # newui, no upstream; #50's changes uncommitted
git log --oneline -5    # e4af022 is HEAD, unchanged by this session
gh issue view 50        # needs devenv: title+body now match what shipped
```

**What landed (all uncommitted):**

Store layer:
- `internal/store/schema.sql` — new `discovery_state` table (BLOB hash PK,
  `state` TEXT, `marked_at_last_seen` TEXT). `CREATE TABLE IF NOT EXISTS` is
  applied unconditionally, so **no migration/addColumn pass is needed** for a
  brand-new table (confirmed in `schema_test.go`).
- `internal/store/reader.go` — `RepeatedContent` now filters
  `cr.direction = 'request'` (was mixing client-original, guardrail-rewritten
  and response hashes into one grouping) and orders by
  `sessions DESC` **only** — request count is no longer a sort key (it only
  measures how long one conversation ran).
- `internal/store/discovery.go` — same direction filter added to
  `ContentHashCounts` so the two definitions stay in agreement
  (`TestContentHashCountsAgree` depends on that); new state API
  (`DiscoveryStates`, `SetDiscoveryState`, `ClearDiscoveryState`,
  `DiscoveryMark`, `PositionsForContent`, state constants, error sentinels).
  Read+write both live on `Reader` — it already owned write paths
  (`SweepContent`, `ForgetRequests`) and opens its own `*sql.DB`, so no new
  writer type. State changes are operator actions, not traffic, so they
  bypass the async `Writer.Record(Event)` queue on purpose.

UI layer:
- `internal/ui/discovery.go` — `repeatedBlockView.State`; state lookup joined
  into `DiscoveryHandler`; `blockRequestRowView` (embeds `requestRowView`, adds
  the diff-link coordinates from `PositionsForContent`);
  `DiscoverySetStateHandler` (the state-cycle POST).
- `internal/ui/templates/pages/discovery.html` (new) — ledger + detail pane.
- `internal/ui/templates/partials/discoveryRow.html` (new) — the
  `repeated-rows` / `repeated-row` fragment split, plus the
  `discovery-state-dot` named template.
- `internal/ui/templates/pages/block.html` (new, 13 lines) + 
  `internal/ui/templates/partials/blockRequests.html` (new) — the drill-down.
  Split because a full page renders from the page set and an htmx fragment
  renders from the partials-only set; the page just includes the fragment so
  the two shapes cannot drift.
- `internal/ui/templates/partials/guardrailDiff.html` (new) — **re-creates the
  `guardrail-diff` fragment that #55 deleted** along with the rest of the old
  admin UI. `GuardrailDiffHandler` had been left referencing a template name
  that no longer existed, so it 500'd; it is now live again and is Discovery's
  only caller (session.html computes its preamble diff eagerly in
  `transcript.go` and never reaches this handler).
- `internal/ui/static/discovery.js` (new) — selection, detail-pane render,
  arrow-key nav, click-to-cycle handled by the dot's own hx-post,
  Enter/double-click → workspace mode, Escape back out.
- `internal/ui/static/app.css` — Discovery table/state-dot/detail-pane rules.
  Reuses the existing `.toolbar`/`.lanes-col`/`.detail-col`/`.diff-*` classes
  rather than porting the mockup's duplicates.
- `internal/ui/ui.go` — `pageFiles` gained `discovery`/`block`.
- `internal/ui/templates/layout.html` — loads `discovery.js`.
- `cmd/arbiter/main.go` — `POST /admin/ui/content/repeated/state`, the first
  POST under `/admin/ui/`, registered `Methods("POST")` like `/admin/reload`
  so a fronting proxy can allow the GETs and deny this one specifically.
- Tests: `internal/ui/discovery_ui_test.go` (new, 8 tests),
  appended store tests in `internal/store/discovery_test.go`.

**State model, as shipped:** unseen / seen / ignored, hash-keyed, absence of a
row = unseen (untouched blocks cost nothing). `seen` reverts to `unseen` when
the block's `last_seen` advances past `marked_at_last_seen`; `ignored` never
re-flags. The state dot's swap is CSS `:has()`-driven off the button's own
class — the fragment response replaces only the `<button>`, never the row.

**What was verified live (not just unit-green):**
Built the real binary against a seeded sqlite DB and fetched it over HTTP.
- ledger groups on raw hash, `direction='request'` only, sorted by sessions;
  a block repeated 5× inside ONE session correctly does **not** appear at the
  default `min_sessions=2`, and does at `min_sessions=0`.
- the drill-down lists every request containing a block, each with a working
  "view diff" → the re-created fragment renders real `.diff-eq`/`.diff-del`/
  `.diff-add` spans.
- the state POST cycles unseen→seen→ignored→unseen and the mark survives a
  page reload.
- error paths: bad/missing hash → 400 naming the parameter; unknown-but-valid
  hash → normal empty page (content may have aged out); GET on the state route
  → 405; non-integer `msg`/`pos` → 400; htmx fragment request carries rows and
  no page chrome.
- `discovery.js` was exercised in its shipped form under a throwaway Node DOM
  shim (Node is in the Nix store, not on PATH), then the shim was proven real
  by breaking the shipped file and watching the harness fail.
- CSS comment balance checked (`42` opens, `42` closes) — an unbalanced
  comment silently swallows the next rule, which is a landmine this file has
  hit before.

**Known noise (unchanged, do not re-diagnose):**
```
devenv shell --no-tui -- go build ./...   # clean
devenv shell --no-tui -- go vet ./...     # clean
devenv shell --no-tui -- go test ./...    # same 3 pre-existing failures as §33:
    internal/config: TestShippedArbiterYAMLLoads, TestShippedTitlePatternsAreTheIntendedRegexes
    internal/ui: TestSeriesEndpointShape
```
Re-confirmed pre-existing this session by stashing **including untracked files**
(`git stash -u`) and re-running on the clean tree — plain `git stash` is not
enough evidence here, because it leaves the new untracked templates/JS in place
and the comparison is contaminated. That is the trap §31/§32 fell into.

**Deliberately out of scope (per the scoping conversation, encoded in #50):**
pattern-family clustering (the mockup's 9 labels came from a one-off Python
heuristic in `design/build_data.py`, not a live query), UA/client sort toggle,
smart substring search/column sort, and the mockup's pre-seeded ignore list.

**Next steps, in order:**
1. Do not close #50/#52/#53/#54/#55 — still batched, pending the user's own
   full-UI test pass.
2. #54 (Overview) is now the **only** remaining unported page in the batch.
3. The Discovery work is uncommitted. Ask before committing or pushing;
   branch `newui` still has no upstream.

## 35. Session 2026-09-26 — #50 (Discovery) follow-ups + committed (branch `newui`, commit c836c9b)

**Orient (run these, don't trust the numbers below):**
```bash
git status -sb          # newui, no upstream; c836c9b is #50's commit
git log --oneline -5    # c836c9b HEAD
gh issue view 50        # progress comment posted this session
```

Continues §34 (same session thread, different context window). Three
follow-ups landed on top of what §34 already shipped, then everything
was committed as one commit:

1. **Caching.** The live ledger query was ~14-21s on the real prod DB
   (1.1GB, 4M+ content refs) — too slow to be "usable now" per the user.
   Added an in-memory TTL cache (`discoveryCache` in
   `internal/ui/discovery.go`) fronting `RepeatedContent` +
   `ContentHashCounts`, keyed on `(since, min_requests, min_sessions,
   limit)`. Single-admin traffic → plain mutex + TTL is enough, no
   singleflight/sharding, no invalidation beyond expiry. Seen/ignored
   state is deliberately **not** cached (cheap query, and staleness
   would be user-visible/wrong) — writes through immediately even
   while the ledger itself is served stale. On-disk caching explicitly
   deferred by the user ("finetuning later... i just want it usable
   now"). Verified live: cold 14.4s, cached reload 5ms; different
   params still pay full cost once then cache; state POST reflected
   immediately through a cached ledger.
2. **Workspace-mode full text.** User: "when going into workspace
   mode, the text field should be as large as can fit in the
   workspace, and show the full text, untruncated." Workspace mode had
   been showing only the ledger row's 200-char SQL preview stretched
   into a bigger CSS box — not actually more text. Added
   `GET /admin/ui/content/block/body?hash=` (`DiscoveryBlockBodyHandler`,
   fragment-only), which `discovery.js`'s `renderWorkspace` now
   htmx-fetches on entry. **First pass reused `blockPreviewBytes`' 8KB
   render cap** (the same one the drill-down page uses) — this was
   wrong; the user explicitly asked for untruncated text in workspace
   mode specifically, caught it ("workspace mode still truncates at
   8192 bytes?"). Fixed: workspace's fragment renders `.Block.Body`
   raw, no cap — the one place on the site `blockPreviewBytes` does
   not apply. `TestDiscoveryBlockBodyIsNotTruncated` seeds an ~18.4KB
   block and asserts the tail survives with no truncation notice —
   this is the regression test that would have caught the original
   mistake.
3. **Hide ignored.** User: "i'd like a way to just not show ignored
   rows. otherwise they are not really ignored." Default behavior
   (dim-in-place, per the mockup) stayed as-is; added `hide_ignored=1`
   as an opt-in toolbar checkbox (`discoveryView.HideIgnored`) that
   drops ignored rows from the rendered list after state marks are
   attached — applied post-cache, not folded into the SQL query, since
   "ignored" is a UI-only mark the aggregation query has no concept of.
   `Shown` count adjusts to match what's actually on the page.

**Also answered, no code change:** "is it expected that ignored still
show up [by default]?" — yes, confirmed against the mockup
(`design/mockups/discovery-B-ledger.html`): ignored rows dim
(opacity 0.55/0.22) but were never meant to vanish; that's what
`hide_ignored` (item 3) is for.

**Committed as `c836c9b`** — one commit, all of §34 + this session's
three follow-ups together (they're one continuous feature, never
shipped separately). Staged explicitly by path, NOT `git add -A`: the
untracked design/mockup/dev-scaffolding files listed in §32-34
(`cmd/bigpreview/`, `cmd/previewserver/`, `cmd/realpreview/`,
`design/build_data.py`, `design/discovery_export.json`,
`design/inline_data.py`, `design/mockups/*` exploration artifacts,
`design/overview-mockups/`) are still untracked and still deliberately
excluded — same disposition as before, unrelated to what shipped here.
Also left out: `support/pasted_content_2026-09-25_14-23-43-630_7b956f.txt`,
a stray paste artifact, not part of this feature.

Pre-commit hook note: `golangci-lint` needs `go` on PATH, which only
exists inside `devenv shell` — running `git commit` directly on the
host shell fails the hook (`exec: "go": executable file not found`).
Always commit via `devenv shell --no-tui -- bash -c "git commit ..."`.

**Ticket:** posted a progress comment on #50
(https://github.com/cnf/arbiter/issues/50#issuecomment-5840851844)
summarizing what shipped, including the three follow-ups beyond the
original scope. **Left open**, per the batch convention — #50/#52
(shipped)/#53 (shipped)/#54/#55 close together after the user's own
full-UI pass. #54 (Overview) is still the only unported page left in
the batch.

**Known noise (unchanged, do not re-diagnose):**
```
devenv shell --no-tui -- go build ./...   # clean
devenv shell --no-tui -- go vet ./...     # clean
devenv shell --no-tui -- go test ./...    # same 3 pre-existing failures:
    internal/config: TestShippedArbiterYAMLLoads, TestShippedTitlePatternsAreTheIntendedRegexes
    internal/ui: TestSeriesEndpointShape
```
Re-verified post-commit, not just pre-commit — build/vet/test run again
against the committed tree, same result.

**Next steps, in order:**
1. Do not close #50/#52/#53/#54/#55 — still batched, pending the user's
   own full-UI test pass.
2. #54 (Overview) is the only remaining unported page in the batch.
3. Branch `newui` still has no upstream configured — ask before pushing.

---

## 36. Session 2026-09-26 (later) — #54 (Overview) implemented, toolbar fixed from first real use, docs refreshed (branch `newui`, HEAD `aedb979`)

Continues §35. **#54 is now shipped and the batch is code-complete** — every
page in it is built. Branch `newui` went `5c3b8c7` → `325b03b` → `dbacc38` →
`ecc6299` → `26c358c` → `aedb979`.

**What landed, in five commits:**

- **`325b03b` / `dbacc38` — phase 1: the store layer.** `store.Window` gained
  `Until` (exclusive; zero = open-ended, so every existing caller is unchanged),
  and `internal/store/flow.go` added `RoutingEdge`/`RoutingFlow`/`ConfigEpochs`
  with per-edge rate metrics. The first version collapsed config-epoch bursts by
  **end-to-start** gap and was wrong on live data: consecutive epochs *overlap*
  (an in-flight request is recorded under the old epoch after the new one began),
  so the gap went negative at every reload and the settled config got swallowed.
  Now start-to-start (< 3 min = `epochBurstGap`), and the seeded test carries the
  straggler case — reverting the comparison reproduces the exact live failure.
- **`ecc6299` — phase 2: summaries and compare.** `internal/store/summary.go`:
  `Measures` (the one definition of cache-hit and cost-per-1M, embedded by both
  `RoutingEdge` and `WindowSummary` so the KPI strip and the drawer cannot
  drift), `SummarizeWindow`, `Delta`, `Direction`, `CompareWindows`. Direction is
  a **tri-state** (`Neutral`/`LessIsBetter`/`MoreIsBetter`), not a bool — a bool
  printed "requests −4.6% worse", and request volume has no good direction.
- **`26c358c` — phases 3+4+5: the page, and the rip-out.** `internal/ui/sankey.go`
  + `sankeypath.go` (geometry in Go, unit-testable), a rewritten `overview.go`,
  `templates/pages/overview.html` + `partials/overviewNode.html`. Deleted by
  `git rm`: `internal/ui/series.go`, `static/chart.js`, `uplot.min.js`,
  `internal/store/series.go`, `pivot.go`, and their tests. Route cap 24 → 12
  (tail folds into `other`) after a live render showed 18 model nodes with labels
  9px apart; too-short bands now draw a bar only.
- **`aedb979` — the toolbar fix**, from the user's first real click-through.

**The toolbar fix is the part worth reading before touching that page.** The user
reported six problems, and five shared one root cause: **mode was a button pair
separate from the anchor select**, so the two controls could contradict each
other. Picking a change in single mode did nothing; clicking Compare submitted
without an anchor and rendered a 400. Mode is now *derived* from the anchor —
there is no `?mode=` parameter at all — which removed the defect class instead of
patching symptoms. A real `datetime-local` picker was added as `?anchor_at=`, and
it wins over the select when both are set.

Two bugs underneath it passed every existing test, both worth remembering:

- **`activeSince` returned `overviewDefaultWindow.String()`** — `"24h0m0s"` —
  which matches no `<option>` value, so the default page marked *nothing*
  selected and the browser displayed its first entry ("last 1h") while rendering
  24h of data. That is why the window control felt inert. Now a
  `defaultSinceChoice` constant with a test asserting the two literals agree.
- **Anchor selection only failed on live data.** The option value is RFC3339
  (no sub-second part) while selection compared `time.Equal` against the
  reparsed value, so real timestamps' nanoseconds broke every match — while
  seeded test data (zero nanoseconds) stayed green. **This is the same failure
  shape as the phase-1 burst bug: unrealistically tidy seed data hid a live-only
  defect.** The test now seeds `123456789ns`.

**Two real defects found during the doc pass, both now tracked — and the second
is worse than this session first reported it.** Both are consequences of the
greenfield rip-out that nobody re-checked:

1. **#56 — every `flatLineHref` link 404s.** `internal/ui/requests.go` still
   builds `/admin/ui/requests?flat=1…` for the Sessions page's "open flat list"
   affordance (`data-open-href`, consumed by `laneDetail.js`, plus a second
   client-built href at `live.js:322`), but that page was deleted in the
   rebuild. Confirmed live: link present, target 404.
2. **#57 — the live tail returns 200 with an EMPTY `html` on every row, and
   nothing mounts it.** This session initially called it "dead-but-working" on
   the strength of the endpoint returning 200. That was too generous and wrong:
   `renderTailRow` renders through the `req-line` partial, which was deleted in
   `572443e`, and the error is swallowed by design — so the endpoint reports
   success while producing no markup at all. Verified live: ids, keys and cursor
   correct, `html` is `""` for every row. **The lesson: a 200 is not evidence a
   handler produced anything**, and a deliberately-swallowed render error is
   exactly where that assumption fails. The same pass also sized the user's real
   question in that ticket — what live tailing the Sessions *lanes* page would
   cost, and the two shapes it could take.

**Docs refreshed in this session** (README endpoint table + Admin web UI +
live-tail + grouping + Overview sections; REQUIREMENTS 7b-1/7b-3a/7b-3b;
DESIGN.md Overview and landing page; this file's §6 batch table). The
administrative shape of that pass: **the README had five passages describing the
deleted requests page as if it were live**, and REQUIREMENTS still marked the
**pivot** Overview "BUILT, COMMITTED" and 7b-3b (uPlot) as merely pending — the
pivot was deleted, so 7b-3b is *dropped*, not deferred. Anything in an older doc
that describes `/admin/ui/requests` as a page is stale.

**Still open / next steps:**
1. **Do not close #50/#52/#53/#55** — batched, and #54 now joins them. The user's
   own full-UI pass is the gate, and every page is built, so that pass is the
   immediate next step.
2. **#56 and #57** — the two defects found in the doc pass. #56 (dead flat-list
   links) is a `bug/priority:medium`. **#57 carries the user's actual question:**
   the live tail both returns empty HTML and has no mount point, and the ticket
   scopes what live tailing the Sessions *lanes* page would cost (fragment-refresh
   ≈ a day; true row-splicing ≈ several days, dominated by placement rules).
3. Housekeeping the user has been offered and not yet greenlit:
   `build/livecheck.db` (1.2GB, gitignored, safe to delete) and a stale
   `.claude/worktrees/*` checkout.
4. Branch `newui` still has no upstream configured — ask before pushing.
