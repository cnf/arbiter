# Redesign exploration — read this first for UI work

A new session should be able to resume the **UI redesign thread** from this file
alone, without replaying the conversation. This is a **map, not a source of
truth**: the mockup files are the source of truth for what was agreed; this
file just points at them and records the reasoning, open questions, and
process lessons that aren't visible in the HTML itself.

This file is about an **exploratory redesign thread that has not landed in
code yet**. It is separate from `../DESIGN.md`, which is the formal,
lint-checked token spec for the *currently shipped* UI (issues #46–48, dark
old-generation "ledger" flat-white admin panel). Do not edit `DESIGN.md` from
this thread until the new direction is fully settled — right now only the
**page composition and chrome** are settled; the session-lane row/node visual
design is explicitly still open (see §3).

Written 2026-09-23, at `develop` = `2f3a740` (ahead of origin by 52, two
untracked files: `cmd/previewserver/` and `feedback.md` — both pre-existing,
unrelated to this thread). Run `git -C .. log --oneline -3` to confirm this
is still current.

## 1. Orient

Run from the repo root (`Arbiter/`), not from `design/`:

```bash
git log --oneline -8                  # confirm develop hasn't moved past 2f3a740
ls design/mockups/                    # the three live mockups this thread produced
```

## 2. What happened, in order (so the reasoning isn't lost)

1. **Started from the shipped UI** (`DESIGN.md` + concepts A–I, the flat
   white/ledger-style admin panel already implemented via #46–48). User's
   complaint: it "still looks like the old UI" — dense-admin-table posture
   never actually changed, only row-grid mechanics did.
2. **First attempt (rejected): "feels" A/B/C** — kept the exact same page
   shape (header/nav/table-of-rows) and only varied typography/surface/color
   mode. User called this out correctly: *"it just looks like a new skin
   over the same site... as soon as you see what is there, it is impossible
   to do something different"*. Lesson: seeing the existing UI before
   designing anchors composition even when told to "redesign" — reskin is
   the default failure mode. **These three files were deleted**, not kept.
3. **Second attempt: genuine blank-slate composition, two directions** —
   explicitly no inherited header/nav/footer, no assumption that "one row =
   one request" is even right:
   - **Scope** — IDE-debugger shape: icon rail (mode filters) + one
     continuous multiplexed event stream (client + system traffic
     interleaved) + persistent right inspector. Kept in
     `design/mockups/arbiter-redesign-1-scope.html` as a reference/idea bank
     — **not** the chosen direction (see §3), but the "everything as one
     filtered stream" idea may still be worth mining for Overview or
     Discovery later.
   - **Threads** — lineage-first shape: a **session is the swimlane**, its
     requests are large ringed nodes, classifier/title/subagent calls it
     triggered are small connected satellite nodes above it. Kept in
     `design/mockups/arbiter-redesign-2-threads.html`. This is the file
     whose *idea* (not its exact page shell) became §3.
4. **User's reaction, the actual decision point**: neither whole-page
   composition was right, but both had real ideas — critically, *the
   requests-page/sessions-page split never felt right, and merging them
   (Threads' insight) suddenly gives a spacious view*. Also confirmed: a
   single page cannot hold all the app's information (kills Scope's
   everything-in-one-stream idea for the top-level IA) — Overview, Sessions,
   and Discovery are confirmed as **separate pages**; session transcript is
   reached *from* the Sessions page, not a peer nav item.
5. **Third build: the merged Sessions page**, decompressed into its own
   page now that it's not fighting a rail+inspector for room. This is the
   file that stuck — kept in
   `design/mockups/arbiter-redesign-3-sessions-merged.html`, and is now
   **the reference file for "what Arbiter's UI should generally feel and
   compose like."** User, verbatim: *"i think as a general design, i like
   this."*
6. **Two follow-up rounds of polish on file 3** (both landed, both
   verified — see §4):
   - **Theme**: dark-first, but not neutral gray — desaturated blue-slate
     background (`--bg:#14161C`, `--panel:#1B1E26`) so `--secondary` blue
     has a native surface and `--primary` orange pops via warm/cool
     contrast (user explicitly invited picking a background that
     complements the brand pair, not just "make it dark"). Light theme
     still fully present via `html[data-theme="light"]`, toggled by a
     real button, default is dark.
   - **Header**: was flagged as "just a bar with words in it — neither
     information dense, nor visually appealing." Fixed twice:
     1. Turned each nav item into a live stat (`OVERVIEW → $18/24h`,
        `SESSIONS → 3 active`, `DISCOVERY → 2 gaps`) instead of a bare
        label — user's reaction: *"i had not thought of putting more
        information in the top bar... i think that actually works."*
     2. Logo made prominent (30px gradient mark, larger wordmark) and
        turned into a real `<a href>` that always routes home — it was
        previously a plain styled `<div>`.

## 3. Current state — what's decided vs. still open

**Decided (general design direction):**
- `design/mockups/arbiter-redesign-3-sessions-merged.html` is the reference
  file. Multi-page app (confirmed, not single-stream).
- Pages: **Overview**, **Sessions** (merges old Requests+Sessions pages —
  this is the one built), **Discovery**, all top-level; **session
  transcript** reached from a session row on the Sessions page, not its own
  nav item.
- Header: slim persistent top bar, logo always links home, each nav item
  carries a live glanceable stat, not just a label.
- Theme: dark-first, desaturated blue-slate (not neutral gray, not harsh
  black), full light-mode parity via a token swap + toggle button.
- Brand colors unchanged from `DESIGN.md`: primary `#FF6700` orange
  (commit/action only), secondary `#0098FF` blue (nav/reference only) — this
  constraint was fixed by the user before any exploration started and never
  revisited.
- Merged-page structure: session = swimlane (fixed label column: session id
  chip, first-message preview, request/satellite counts, running cost),
  requests + their triggered classifier/title/subagent calls are nodes along
  a horizontal timeline within the lane, satellite nodes connect to their
  parent via a visible stem. Right side: persistent detail panel (not a
  modal/dock) — default view is aggregate stats for what's in view, swaps to
  full rationale/facts on node click, never navigates the lane list away.

**Explicitly NOT decided — its own future design round:**
- The **visual design of the session-lane row itself** (node sizing/shape,
  spacing, exact swimlane visual treatment, how it holds up with real dense
  session data). User was explicit: *"i'm not going to comment on the design
  of the session lanes, because that is a separate design round."* Don't
  treat file 3's current row visuals as agreed — only the page-level
  composition (swimlane-per-session, merged page, persistent detail panel)
  is agreed.
- Overview and Discovery pages: not designed in this thread at all. User has
  confirmed Discovery is needed (already known to be its own page) and
  guesses Overview is needed but hasn't used it yet — no mockup exists for
  either.
- `DESIGN.md` **was reconciled** 2026-09-23 on the `newui` branch (see §6) —
  it now matches this thread's decided direction (§3's "Decided" list) and
  explicitly calls out the still-open items (session-lane row visuals,
  Overview) as open rather than settled. It is current, not stale — the
  old "needs a deliberate reconciliation pass" note below is resolved.

## 4. How mockups were verified this session (repeat this, don't re-derive it)

The sandbox terminal has no browser/node by default. What worked:

```bash
# ad-hoc deps per container-instructions.md — don't install on the host
devenv --option packages:pkgs "nodejs chromium dejavu_fonts fontconfig" shell --no-tui -- bash -c '
  node -e "…syntax-check the <script> block via new Function(src)…"
  export HOME=/tmp/chromehome FONTCONFIG_FILE=/tmp/fonts.conf
  chromium --headless=new --disable-gpu --no-sandbox --virtual-time-budget=2000 \
    --screenshot=/tmp/out.png --window-size=1680,1050 --hide-scrollbars \
    file:///workspace/Arbiter/design/mockups/<file>.html
'
```
Then `VisionAnalyze` the screenshot. For click-driven interaction (theme
toggle, node-click detail panel), static screenshots aren't enough — drive
real clicks via the Chrome DevTools Protocol over the `--remote-debugging-port`
websocket (`Runtime.evaluate` to click, `Page.captureScreenshot` after) rather
than faking state by hand-editing HTML attributes before rendering; a hand-
edited attribute bypasses the JS that keeps UI in sync and produced one false
"bug" this session (a toggle-button label mismatch that wasn't real).

**Fonts**: this ephemeral nix shell has no fontconfig by default — Chromium
hard-crashes (`SkFontMgr...Not implemented`) without a minimal
`FONTCONFIG_FILE` pointed at a scratch cachedir + `/nix/store` as a font
`<dir>`. `dejavu_fonts` is enough; don't chase pixel-perfect font matching,
it's a rendering-doesn't-crash check, not a typography review.

**`devenv --option packages:pkgs "..."` cold-builds a new shell derivation
per distinct package set** — first call after a package-set change can take
minutes; treat a `timeout` hit as "still building," not "hung," and retry
with patience once, background+notify if it's genuinely long.

**A real host-side browser tool also exists** (`desktop_preview` /
`drive_preview` / `annotate_preview`) but runs in a **separate
host/filesystem namespace** from this sandbox's terminal — a file path valid
here (`/workspace/...`) resolves to `chrome-error://chromewebdata` there. It
works fine for real URLs (a running localhost dev server on the *host's*
side, or any public URL) but cannot reach files written from inside this
sandbox. Don't spend time trying to bridge this; screenshot-and-VisionAnalyze
via the headless-Chromium route above is the working path for local mockup
files.

## 5. Landmines

- **Don't reskin.** Seeing the current shipped UI before starting a
  "redesign" reliably produces a reskin, even when explicitly told not to.
  If asked to redesign again (e.g. Overview or Discovery), consider drafting
  blind first, or at minimum naming the composition/surface archetype out
  loud before touching any token.
- **Mockups written outside `design/mockups/` violate the "stay inside the
  project directory" rule.** Earlier this session several iterations were
  written to `/workspace/` (sibling to the repo, not inside it) before this
  was caught and corrected — the three surviving files were moved into
  `design/mockups/` after the fact. If a fresh session finds more loose
  `arbiter-*.html` files under `/workspace/` root, they predate this fix and
  are historical (the original concept A–I + `arbiter-design-preview.html`
  round, superseded by everything in §2) — leave them, don't clean up
  without asking, but don't add new files there either.
- **`DESIGN.md` is current, not a frozen "shipped" reference** — see §6. It
  was rewritten on `newui` to track this thread's decisions and is meant to
  be kept in sync going forward: when a new page-pattern decision lands
  (e.g. the session-lane visual round, Overview), update `DESIGN.md`
  directly rather than letting decisions accumulate only in this file. This
  file (`REDESIGN.md`) is for narrative/process/reasoning that doesn't fit
  DESIGN.md's token-and-prose format; DESIGN.md is the checked-in source of
  truth for what the UI actually looks like.

## 6. DESIGN.md reconciliation (2026-09-23, `newui` branch)

`DESIGN.md` was rewritten in place to match §3's decided direction, after
the user asked directly: *"does DESIGN.md MATCH what we are doing? or
contradict it?"* — a systematic diff against
`design/mockups/arbiter-redesign-3-sessions-merged.html` found real
contradictions, not just drift:

- **Theme mechanism inverted**: old spec said light is canonical,
  `*-dark` is a `prefers-color-scheme` swap-in. New spec: dark is
  canonical, `*-light` is an explicit `data-theme` attribute swap
  triggered by an in-page toggle button — not an OS hook.
- **Light-theme neutrals actually differed** (not just dark) — old
  `bg:#FBFBFC` (cool near-white) vs. mockup's `#F5F4F1` (warm off-white);
  old spec's warm-dark-vs-cool-light split didn't hold, both themes in the
  mockup lean warm on purpose.
- **Border radius scale abandoned** — old `rounded.sm` 3px vs. mockup's ad
  hoc 4–8px values with no consistent default.
- **Sessions-index page-pattern rule inverted** — old spec: first-message
  preview column is `1fr`, everything else fixed-px. New composition:
  preview sits in a *fixed* 260px label column, timeline is `1fr`.
- Old spec's entire "Request rows (requests page)" section described a
  page that no longer exists (merged into Sessions).

Fixed, not just documented: `DESIGN.md` was rewritten (color tokens,
mechanism prose, rounded scale, and the Page Patterns section) rather than
patched, since the mechanism-level contradictions meant patching individual
values would have left inconsistent prose. **Explicitly preserved as open**
in the new spec (not accidentally locked in by the rewrite): the
session-lane row's exact visual treatment, and the missing session-id
dot-marker convention from the old spec that the current mockup dropped
without an explicit decision either way.

Linted clean on this pass: `devenv --option packages:pkgs "nodejs" shell
--no-tui -- npx -y @google/design.md lint DESIGN.md` → **0 errors, 25
warnings (all `orphaned-tokens` — status/kind/tool colors referenced in
prose/page-patterns, not the `components:` whitelist, same pattern the
previous generation's spec already had), 1 info.** Not committed as part of
writing this file — that's the user's call, same as always.

**A real /tmp disk-space landmine hit during this pass, worth recording**:
this sandbox's `/tmp` is a 512MB tmpfs. An earlier verification step
(§4's Chromium fontconfig-cache workaround, `HOME=/tmp/chromehome`) filled
it completely and silently broke unrelated `devenv shell` invocations later
in the same session (`No space left on device` on a totally unrelated `npx`
lint command, plus a `home directory "/homeless-shelter" exists` nix error
from a stale `$HOME` env var leaking across terminal calls in the same
session). If a `devenv --option packages:pkgs` invocation fails with either
of those errors, check `df -h /tmp` and `rm -rf /tmp/chromehome` (or
whatever scratch dir accumulated) before assuming the command itself is
broken — and unset any `HOME`/`FONTCONFIG_FILE` override from an earlier
verification step before running an unrelated devenv command in the same
terminal session.
