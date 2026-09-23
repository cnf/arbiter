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
ls design/mockups/                    # file 5 is current; files 1-2 are idea-bank reference; file 3 is superseded but kept for history
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
- `design/mockups/arbiter-redesign-5-lanes-realdata.html` is the reference
  file (supersedes files 3 and the deleted file 4 — see below). Multi-page
  app (confirmed, not single-stream).
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
- Merged-page structure: session = full-width swimlane, two stacked lines —
  a **header line** (session-id chip + request/satellite counts + "started
  …" left-aligned; first-message preview + running cost right-justified on
  the same line) and a **full-width timeline line** below it holding every
  node for that session (client requests + the classifier/title/subagent
  calls they triggered, satellites connected to their parent via a visible
  stem). High-volume sessions collapse repeated same-route requests into a
  single pill-shaped **stack node** (e.g. "337×") rather than spawning more
  lanes or nodes — verified against a real 342-request session. Right side:
  persistent detail panel (not a modal/dock) — default view is aggregate
  stats for what's in view, swaps to full rationale/facts on node click
  (including stack-node clicks, which show aggregate stack detail), never
  navigates the lane list away.
- Row/node visual design (§ below) has moved from "wide open" to "agreed at
  the composition level, open on live-data tuning" — see next bullet block.

**Decided this round, real-data-grounded (2026-09-23, file 5):**
- Session-lane rows and the right detail panel were rebuilt and verified
  against real rows pulled from `/data/arbiter/arbiter.db` (read-only mount)
  instead of synthetic placeholder text — a clean 4-request session
  (`ccd913`), a real broken 8-request/4-error run (`e3113e`), and a real
  342-request session (`a257df`, this very redesign conversation as
  recorded by Arbiter itself).
- A regression happened and was caught mid-round: an earlier pass (file 4,
  now deleted) collapsed the one-lane-per-session model into one `<div
  class="lane">` per *request*, so a 4-request session rendered as 4 stacked
  lanes, and dropped the persistent right panel while focused on row CSS.
  User caught both immediately ("you removed the right bar... i dont
  understand the new lanes"). Fixed by rebuilding on file 3's actual
  lane-row skeleton rather than patching file 4 further — **always verify a
  round's structure against the last-approved file's actual DOM shape when
  rebuilding with new data, not just visually.**
- User then asked for header-line justification: id/counts left, preview
  text + cost right, on one line, full-width timeline below. Built and
  vision-verified (zoomed crop confirming no truncation/crowding).
- User's explicit sign-off: *"yeah, much better... i think this is a nice
  overview"* and *"i think that is good for now on the sessions. it'll
  probably need tweaking when i use it live, but that is a problem for
  then."* — composition + row/node treatment are settled for now; expect
  revision once used against live traffic, and that's expected, not a gap
  to chase preemptively.
- File 4 (`arbiter-redesign-4-lane-row-realdata.html`) was deleted — it
  represented the regressed structure and would only confuse a future
  reader if left alongside file 5.

**Explicitly NOT decided — deferred until live use:**
- Exact node sizing/spacing/proportions may need adjustment once this page
  is used against real live traffic day to day. This is an accepted,
  expected follow-up, not an open design question to resolve now.
- Overview and Discovery pages: not designed in this thread at all. User has
  confirmed Discovery is needed (already known to be its own page) and
  guesses Overview is needed but hasn't used it yet — no mockup exists for
  either.
- `DESIGN.md` **was reconciled** 2026-09-23 on the `newui` branch (see §6),
  then updated again the same day to match file 5's finalized lane-row
  design (header-line justification, stack-node pattern, restored right
  panel). It is current, not stale.

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

**Root cause, found and fixed in the file-5 round**: the actual culprit was
`fonts.conf` pointing `<dir>` at all of `/nix/store` — `fc-cache -f` (which
Chromium triggers on first launch) walks that whole tree and built a 433MB+
cache from it every time, refilling `/tmp` even after cleanup. Fix: point
`fonts.conf`'s `<dir>` at just the specific font package path (e.g.
`/nix/store/<hash>-dejavu-fonts-2.37/share/fonts`), not `/nix/store` itself.
With a scoped `fonts.conf`, the cache stays under ~5MB. Use this from the
start instead of the clean-up-after-the-fact workaround above:

```xml
<?xml version="1.0"?>
<!DOCTYPE fontconfig SYSTEM "fonts.dtd">
<fontconfig>
  <dir>/nix/store/<hash>-dejavu-fonts-2.37/share/fonts</dir>
  <cachedir>/tmp/chromehome/.cache/fontconfig</cachedir>
</fontconfig>
```

(Find the exact hash with `find /nix/store -maxdepth 1 -iname "*dejavu-fonts-*"`.)

**Chromium headless can hang, not just crash, under tmpfs pressure**: when
`/tmp` was near-full (86%) this round, `chromium --headless --screenshot`
hung indefinitely instead of erroring — the foreground `terminal` call hit
its timeout while Chromium was still alive in the background. Symptom: the
tool reports a timeout, but the screenshot file may or may not have
actually been written (check `ls -la` on the output path before assuming
total failure — it landed successfully once despite the reported timeout).
If it truly hung, `pkill -9 -f "chromium.*<distinguishing-arg>"` before
retrying; retrying without killing the stuck process wastes another full
timeout window.
