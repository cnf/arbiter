# PICKUP.md — read this first

A new session should be able to start work from this file **plus the board**, without
reading the README, REQUIREMENTS.md, `feedback.md`, or git history end to end.

This file is a **map, not a source of truth**. Anything here that can drift (branch
state, issue state, row counts) is given as a *command to run*, not as a fact to
trust. The board is authoritative for work items; the code is authoritative for
behaviour.

Written 2026-09-20, at `develop` = `0327600`; §5 and §9 updated 2026-09-21 at
`develop` = `6ef15bf`. Run `git log --oneline -1` for the truth.

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
the same three title patterns as the repo's copy — verified by loading the deployment's
config through the real parser and compiling each pattern against the real prompts.

**What is still unobserved: a non-NULL `request_kind`.** No row has one, and that is
expected rather than a defect — **zero title-gen requests have arrived since the
patterns were added** (newest is id 3865, 2026-09-20 20:00; ordinary traffic continues
past 2026-09-21 06:49). The label appears on the first title request after a client
starts a new session. Do not read the NULLs as a matching failure: check for a title
request *after* the config first, the way this file's §4 says to check the data before
the code.

**Consequence for #3/#30/#31:** their acceptance is still live confirmation, which needs
a fresh title request rather than a code change.

---

## 6. Open work, in priority order

Run `gh issue list` for the live list. This is the shape of it:

**Blocking anything else being verifiable**
- **#32** — deployment config: the `request-kind` classifier's three title patterns.
  **Done** — `/data/arbiter/arbiter.yaml` carries them and matches all three real
  prompts (verified through the real parser). Nothing left here; what gates #3/#30/#31
  is a fresh title request, not a config edit.

**High — the visibility goal (the project's whole point)**
- **#4** — META umbrella, "it is hard to see what is going on". Three stacked causes,
  one fixed, two open (#8 rows sort by finish time; #9 no client column).
- **#5** — a request that fails routing or is rejected by a guardrail gets **no row at
  all**. `recordRejected` writes content refs under `owner_kind="rejected"` and
  nothing else. This is the biggest single hole in the visibility goal.
- **#7** — the title-gen match pattern covered only Hermes, and opencode's prompt never
  matched. **The evidence it was blocked on is now in §9**: opencode's real prompt
  (`You are a title generator. You output ONLY a thread title.`), Hermes', and Claude
  Code CLI's, all three verified against the 79-prompt corpus. The repo's `arbiter.yaml`
  now carries all three patterns. What remains for #7 is the *label* half — the row's
  `request_kind` — which needs a fresh title request to observe, not a code change.
  The config half is done and deployed.
- **#28** — Anthropic prompt caching never engages (`cache_control` never set;
  483/483 Claude requests uncached).
- **#6** — Jev decision models: `score` (Phase E), `min_confidence` (Phase D),
  subagent detection (Phase F) unbuilt.

**Done in code, open on the board only pending live traffic — do not re-fix**
- **#31** — classifier input (the empty-first-turn bug). Fixed in `06c32e2`, with the
  diagnosis and revert probes recorded in the issue. The only thing left is confirming
  it against real traffic, which needs a fresh title request first (#32's config half is
  done and deployed).

**Medium** — #27, #26, #25, #24, #23, #22, #21, #20, #19, #18, #17, #16, #14, #13,
#12, #11, #10, #9, #8.

(#25, #19, #16, #10 are closed — see §9. #7 is the current front: it was blocked on
reading opencode's real prompt, and §9 now has it.)

**#12 is worth reading before touching the classifier path** — it holds two genuinely
open cost questions (does the pin still short-circuit; should the literal path write a
classifier row) plus the `affinity.pinned` vs `affinity.get` decision, which is a real
trap: `get` only hits when the client is still requesting the model the pin was
recorded under, so a client that switched models would look like a new session and
re-classify on every turn.

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
