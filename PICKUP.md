# PICKUP — Arbiter session handoff

This is a **map, not a source of truth**. Anything that can drift (git state,
issue state, row counts) is a *command to run*, not a fact to trust. The
GitHub issue tracker is authoritative for work items; the code is
authoritative for behavior; `support/notes/refactor-findings-2026-09-27.md`
is the running discovery/decision log for the current refactor pass.

Written 2026-09-28, at `develop` = `d76f32e`; updated same day at `69b087f` —
see §5 (verified state), §6 (#44 closed, #26 split), §7 (new landmines).

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

## 5. Verified state as of `69b087f`

Landed most recently (all on `develop`):

| commit | what |
|---|---|
| `69b087f` | #44's mechanism half, squashed from four commits at the user's request: matcher key renamed `kind:` → `request_kind:` (clean break, old spelling fails at load with an error naming both); `request_kind` settable on a **force alias only** (the three alias shapes are exclusive — pinned/group + request_kind is a load error, per the user's "EITHER pinned, OR a group, OR metadata" rule); stamped on turn 1 via `applyForceAlias` (overriding a classified kind) and re-stamped on turn 2+ by the affinity pin. Docs + example config shipped in the same commit. |

Earlier in this same continuous session (before the above): `d76f32e`,
`fe7aa33`, `5476af7`, `9fa3078` (Sessions page live-poll fixes + findings
file), and further back `46114c5` (#38 closed), `db64885` (test gaps),
`725d100` (status tracking), plus the larger structural cleanup pass
(commits `b94e05a` through `5ab3d02`) referenced in §6 of the findings file.
The 11-item cleanup-pass list is exhausted.

**Push state — run it, don't trust this file:** `git status -sb` / `git log
origin/develop..develop`. At last write `69b087f` was **ahead 1, unpushed**;
pushing is the user's step.

```bash
devenv shell --no-tui -- bash -c 'go build ./... && go vet ./... && gofmt -l . && golangci-lint run ./...'
devenv shell --no-tui -- bash -c 'LITELLM_URL=http://localhost:4000/v1 go test -count=1 ./...'
```
Last known-good result: full gates + full suite green at `69b087f` (verified
immediately before the squash and re-verified after it). One transient
`internal/config` FAIL right after the squash did not reproduce on re-run —
if you see the same, re-run before investigating.

## 6. Open work — READ THIS BEFORE PICKING ANYTHING UP

**UI work is FROZEN.** User's words: *"no more ui fix things until the
functionality below it is stable... i want to track down the functionality
properly, so when we get back to the UI, we have what we need."* Do not
resume #8, #59, #63, or any other UI ticket until told otherwise, even though
several are open and tempting.

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
- **#59** (transcript grouping rework), **#63** (tool-def display) — open,
  UI-frozen.
- **#62** (`RateLimit` captured but drives nothing) — filed, undecided,
  low priority.
- **#66, #67, #24, #23, #22, #21, #20, #18, #17, #14, #4** — feature/deferred
  or `priority:low`/meta items, untouched. Run `gh issue list` for the live
  state, don't trust this list's staleness.

## 7. Landmines

- **Don't infer current behavior from `build/livecheck.db`.** It's dev
  history — things tried, reverted, changed — not a snapshot of what current
  code does. A prior session got the #44 mechanism wrong once by reasoning
  from two different eras of data in that DB as if they were simultaneous
  facts. Verify against code + direct test execution instead.
- **`gh` needs devenv**, and **`gh issue create --body` with backticks/
  quotes in the body mangles it** (the shell eats them — #64/#65 were created
  with garbage bodies this way). Use `--body-file` with an absolute path.
  There is **no `UI` label** in the repo (or there wasn't; it was created
  2026-09-28 for #67 — check `gh label list` rather than assuming).
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

This session: `20260928_150016_d4b925` (the #44 `request_kind` work — design
debate, three user corrections, the squash to one commit). The previous
session (Sessions-page work, findings file): `20260927_120906_ea11f1`.
Recover exact quotes/commands via `session_search(query='...', session_id=...)`
for anything this file doesn't carry in enough detail.

**Last updated**: 2026-09-28, after the #44 squash and the #26 split.
