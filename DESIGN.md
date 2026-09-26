---
version: alpha
name: Arbiter
description: A dense, data-first admin dashboard for a single-user LLM proxy — built to be scanned and compared, not admired.
colors:
  primary: "#FF6700"
  secondary: "#0098FF"
  secondary-ink: "#46B4FF"
  secondary-ink-light: "#0072CC"
  ink: "#E6E8EC"
  ink-light: "#17181B"
  bg: "#14161C"
  bg-light: "#F5F4F1"
  panel: "#1B1E26"
  panel-light: "#FFFFFF"
  panel-2: "#21242E"
  panel-2-light: "#FAFAF8"
  line: "#2A2E3A"
  line-light: "#E5E3DD"
  line-strong: "#3B4051"
  line-strong-light: "#D2CFC6"
  muted: "#9AA1AC"
  muted-light: "#6E6A61"
  faint: "#6B7280"
  faint-light: "#A5A196"
  on-primary: "#14161A"
  on-secondary: "#14161A"
  ok: "#5FC98D"
  ok-light: "#16794A"
  warn: "#E0A44A"
  warn-light: "#A35A00"
  err: "#FF8A80"
  err-light: "#B3261E"
  note: "#A6A6F0"
  note-light: "#3F3F8F"
  kind-classifier: "#A996FF"
  kind-classifier-light: "#7C5CFC"
  kind-title: "#5FD4D4"
  kind-title-light: "#0EA5A5"
  kind-subagent: "#E58BCB"
  kind-subagent-light: "#C2419C"
  tool: "#E0B34A"
  tool-light: "#B8860B"
typography:
  h1:
    fontFamily: ui-sans-serif, system-ui, "Segoe UI", Roboto, sans-serif
    fontSize: 1.25rem
    fontWeight: 600
    lineHeight: 1.25
    letterSpacing: "-0.01em"
  h2:
    fontFamily: ui-sans-serif, system-ui, "Segoe UI", Roboto, sans-serif
    fontSize: 0.95rem
    fontWeight: 600
    lineHeight: 1.3
  label-caps:
    fontFamily: ui-sans-serif, system-ui, "Segoe UI", Roboto, sans-serif
    fontSize: 0.65625rem
    fontWeight: 700
    letterSpacing: "0.06em"
  body-md:
    fontFamily: ui-sans-serif, system-ui, "Segoe UI", Roboto, sans-serif
    fontSize: 0.875rem
    lineHeight: 1.45
  body-sm:
    fontFamily: ui-sans-serif, system-ui, "Segoe UI", Roboto, sans-serif
    fontSize: 0.75rem
    lineHeight: 1.4
  data-mono:
    fontFamily: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace
    fontSize: 0.78125rem
    lineHeight: 1.4
rounded:
  sm: 4px
  md: 6px
  lg: 8px
  pill: 9999px
spacing:
  xs: 4px
  sm: 8px
  md: 12px
  lg: 20px
  xl: 32px
components:
  button-primary:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.on-primary}"
    rounded: "{rounded.md}"
    padding: 8px
  button-primary-hover:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.on-primary}"
  button-secondary:
    backgroundColor: "{colors.panel}"
    textColor: "{colors.secondary-ink}"
    rounded: "{rounded.md}"
    padding: 8px
  button-secondary-hover:
    backgroundColor: "{colors.panel}"
    textColor: "{colors.secondary-ink}"
  card:
    backgroundColor: "{colors.panel}"
    textColor: "{colors.ink}"
    rounded: "{rounded.md}"
    padding: 12px
  table-header:
    backgroundColor: "{colors.panel}"
    textColor: "{colors.muted}"
    typography: "{typography.label-caps}"
  badge-ok:
    backgroundColor: "{colors.panel}"
    textColor: "{colors.ok}"
    rounded: "{rounded.sm}"
  badge-warn:
    backgroundColor: "{colors.panel}"
    textColor: "{colors.warn}"
    rounded: "{rounded.sm}"
  badge-err:
    backgroundColor: "{colors.panel}"
    textColor: "{colors.err}"
    rounded: "{rounded.sm}"
  badge-note:
    backgroundColor: "{colors.panel}"
    textColor: "{colors.note}"
    rounded: "{rounded.sm}"
  tag:
    backgroundColor: "{colors.panel}"
    textColor: "{colors.muted}"
    rounded: "{rounded.sm}"
  input:
    backgroundColor: "{colors.bg}"
    textColor: "{colors.ink}"
    rounded: "{rounded.md}"
    padding: 4px
  brand-mark:
    backgroundColor: "{colors.primary}"
    rounded: "{rounded.lg}"
    size: 30px
---

## Overview

Arbiter's UI is an instrument panel, not a marketing surface: one operator,
reading request logs, classifier decisions, and token counts character by
character. Every choice serves scanning speed — density over whitespace,
borders over shadows, label over icon. The two brand accents (`primary`
orange, `secondary` blue) are used exactly where the operator needs to *act*
or *navigate*; everywhere else the page stays quiet so those two colors keep
their signal. Status color (ok/warn/err/note) is a wholly separate channel
from brand color — a row is never both "the accent" and "a warning" at once.

**Dark is the default and primary-designed theme**, not an afterthought —
the operator runs this at their desk for hours; a bright white admin panel
is the thing being fixed, not the baseline to fall back to. Light mode is a
fully-specified alternate, not a stub.

## Colors

- **Primary (`{colors.primary}`, `#FF6700`):** The one high-emphasis action
  per view — the button that actually does something (start a tail, submit a
  filter). Never used for passive decoration, never used twice in the same
  view at the same weight.
- **Secondary (`{colors.secondary}`, `#0098FF`):** Navigation and reference —
  links, the active nav item, "go look at this" affordances. Lower emphasis
  than primary; it points, it doesn't commit. As flat text-on-surface this
  hue fails WCAG AA in both themes — use `secondary-ink` (dark theme:
  `#46B4FF`, light theme: `secondary-ink-light` `#0072CC`) wherever it
  carries body text or a button label. The bright value stays valid for
  borders, fills, and anything not carrying small text.
- **Ink / Bg / Panel / Line:** The actual page — text, page background, card
  background, and hairline borders. Each has a `-light` pair. Unlike the
  previous generation of this spec, **dark values are canonical** (`ink`,
  `bg`, `panel`, `line`, `line-strong`, `muted`, `faint` all resolve dark by
  default) and `*-light` is the swap-in. The swap is **not**
  `prefers-color-scheme` — it's an explicit `data-theme` attribute on
  `<html>` toggled by an in-page control, because the operator's own
  preference (dark, confirmed) should win over OS default, and the toggle
  itself is discoverable UI, not a hidden system setting.
- **Panel-2:** A second, slightly-lifted surface tone for internal
  dividers/nested panels where `panel`-on-`panel` would have no contrast
  (e.g. a stat block sitting inside a detail column that's already `panel`).
- **Bg vs. Panel is not neutral gray.** Both themes lean warm rather than
  clinical: dark is a desaturated **blue-slate** (`bg:#14161C`,
  `panel:#1B1E26`) chosen because it gives `secondary` blue a native surface
  to sit on and makes `primary` orange read harder via warm/cool contrast;
  light is a warm off-white (`bg:#F5F4F1`), not cool near-white. Do not
  default back to `#111`/`#fff`/pure gray — the warmth is deliberate,
  chosen specifically to flatter the brand pair.
- **Muted / Faint:** Two tiers of secondary text. `muted` is column headers,
  metadata, timestamps — read after the primary value in a cell, not instead
  of it. `faint` is one tier quieter still — placeholder text, disabled
  states, decorative tick marks (e.g. the vertical stem connecting a
  satellite node to its lane).
- **Status (ok/warn/err/note):** Request-outcome semantics only. Reused
  nowhere else, so a glance down a status column is never ambiguous with a
  glance down an accent.
- **Kind (classifier/title/subagent):** A third independent channel, for
  `request_kind` only. Purple/teal/pink are chosen to sit visually apart
  from both brand (orange/blue) and status (green/amber/red) — the eye can
  hunt for "which kind of request is this" without colliding with "is this
  an error" or "is this the primary action."
- **Tool (`{colors.tool}`, amber-brown):** Marks tool-call/tool-result
  content in a transcript. Distinct from `warn` — a tool call is neutral
  information, not a problem state — and distinct from `kind`, since a
  request's kind and its tool activity are different facts read together.

Both `primary` and `secondary` pair with **ink text, not white** —
`on-primary`/`on-secondary` resolve to a fixed dark ink (`#14161A`)
regardless of theme, because both accent fills stay bright enough in both
themes that white text fails WCAG AA on them. Dark text clears it on both.
This is also just the correct move stylistically — bright saturated fills
read better with dark text (see YC's own orange).

## Typography

System font stacks only — this ships inside a self-hosted admin tool, so
nothing should ever block on a webfont fetch. Sans for every UI chrome
(nav, labels, buttons, prose); `data-mono` for anything the store produced
verbatim — hashes, session keys, model names, token counts, JSON. Hierarchy
is carried by size and weight, not typeface switching: `h1`/`h2` step down
from the page title into section headers, `label-caps` marks a column
header or a status chip, `body-md`/`body-sm` are prose and cell text.
`h1` grew slightly (1.15rem → 1.25rem) versus the previous generation of
this spec — the new page chrome has more room to let a page title carry
real weight.

## Layout

4px baseline scale. `xs` for the gap between a value and its unit inside one
control, `sm` for gaps inside a control cluster (a filter row, a button
group), `md` for the padding inside a card/table cell cluster, `lg` between
stacked components on a page, `xl` between page sections. Max content width
is no longer a fixed page-wide cap — the merged Sessions page uses a fluid
two-column layout (lane list + persistent detail rail) instead of a single
centered table, so width is driven by the detail rail's fixed 380px plus
whatever the lane list needs, not a `max-width` on `<main>`.

## Shapes

Flat by design — no elevation/shadow system; a hairline `line` border is
the only depth cue a bordered surface gets, in both themes. The rounded
scale shifted up slightly from the previous generation
(`sm` 3px→4px, `md` 6px unchanged, `lg` 10px→8px) to match what the current
chrome actually uses: `rounded.sm` for small chips/pills/tags,
`rounded.md` for buttons, inputs, and the toolbar's bordered controls,
`rounded.lg` for the one deliberately-bigger shape on the page — the brand
mark in the header. `rounded.pill` remains reserved for tags only.

## Components

- `button-primary` is the one committing action visible at a time — filled
  orange, ink text, no shadow.
- `button-secondary` is a ghost button: `panel` background, blue text,
  used for everything reversible or secondary (reset, cancel, a lower-
  priority nav action).
- `card` is the default bordered surface for a fact grid, panel, or turn —
  `panel` background, `line` border, `rounded.md`.
- `table-header` renders as `label-caps` in `muted` — headers recede so the
  data is what's read.
- `badge-*` are status chips: text-colored, not fill-colored, with a
  matching-tint border — enough signal to scan a column, not enough weight
  to outshine the row's actual value.
- `tag` is the neutral pill for kind/axis labels that aren't a status.
- `input` matches the page background (`bg`, not `panel`) so a focused
  filter field visually recedes into the page until it's actively used.
- `brand-mark` is new: a 30px `rounded.lg` square, `primary`→`secondary`
  gradient fill, containing a simple line-art glyph. It anchors the header
  and is always the leftmost element in a real `<a href>` back to the home
  page — the logo is a navigation control, not decoration.

## Do's and Don'ts

- **Do** keep `primary` to one action per view. If two things feel
  equally important, neither gets the orange — that's a hierarchy bug, not
  a color problem.
- **Do** use `secondary` for anything that navigates or references, never
  for anything that mutates state.
- **Do** use token references (`{colors.primary}`) in components, never
  re-typed hex.
- **Do** treat dark as the theme every new component is designed against
  first; check the `-light` pairing second, not the other way around.
- **Don't** put white text on `primary` or `secondary` fills — both fail
  AA contrast; pair with the fixed `on-primary`/`on-secondary` ink instead.
- **Don't** reach for a shadow. Depth is a border or it isn't there.
- **Don't** let a status color (ok/warn/err/note) double as a brand accent,
  or a scan of the status column stops being trustworthy.
- **Don't** default to neutral gray for `bg`/`panel`. Both themes carry a
  deliberate warm undertone chosen to complement the orange/blue brand
  pair — a plain `#111`/`#fff` regression loses that.

## Page Patterns

Token values and component styles above answer "what a thing looks like."
This section answers "how a page is built" — layout decisions specific
to Arbiter's data. A build session should treat these as structural
requirements, not suggestions, **except where a subsection is explicitly
marked open** — those are known-undecided and should not be treated as
final just because they're written down.

### App chrome (all pages)

Arrived at through blank-slate exploration after the shipped UI's original
header ("a bar with words in it") was flagged as low-information. Applies
to every page, not just Sessions.

- **Header is a persistent 58px top bar**, not a route-aware component that
  changes shape per page.
- **Brand mark always links home.** A real `<a href>`, not a styled `<div>`
  — clicking the logo/wordmark from any page returns to the primary
  landing page, which is **Overview** (`/admin/ui/` 302s there; see the
  Overview note below).
- **Each nav item carries a live stat, not just a label** — two-line
  stack: an uppercase `label-caps` line, then a `data-mono` numeric/value
  line directly beneath it (`OVERVIEW → $18/24h`, `SESSIONS → 3 active`,
  `DISCOVERY → 2 gaps`). This was the single highest-value fix this round:
  the header stopped being pure navigation chrome and became a glanceable
  instrument in its own right. A nav item whose stat represents a problem
  (e.g. Discovery's gap count) uses `err` for the stat value, not `warn` or
  plain `ink`.
- **Theme toggle lives in the header's right cluster**, a real button
  (icon + text label reading the current theme name), not a silent
  `prefers-color-scheme` hook — state changes via `data-theme` on `<html>`,
  default `dark`.
- **Live status indicator** (`● live · N req today`) sits in the same right
  cluster, confirming this is a running system being watched, not a static
  report.

### Sessions (merged requests + sessions page)

**Supersedes the previous generation's separate "Request rows (requests
page)" and "Sessions index" sections below — those described two pages that
no longer exist as separate surfaces.** The split never read right in
practice; merging them turned out to also solve a layout problem (a single
merged page has room to breathe that two cramped table pages didn't).

- **A session is the primary unit — a full-width swimlane, not a table
  row.** Each lane is two stacked lines, not a label-column-plus-timeline
  grid: a **header line** (session-id chip + request/satellite counts +
  "started …" on the left; the first-message preview + running cost
  right-justified on the same line) followed by a **full-width timeline
  line** below it holding every node for that session. Justifying the
  preview/cost to the right, opposite the id/counts, was a deliberate call
  — it reads as one coherent line instead of a left-heavy label block.
- **Requests are large ringed nodes on the timeline**, positioned along a
  horizontal lane line spanning the row's full width. Client-request nodes
  are colored by outcome (`ok`/`warn`/`err` border).
- **Classifier/title/subagent calls a request triggered are small satellite
  nodes**, positioned above their parent node and connected to the lane by
  a visible vertical stem (`line-strong`, partial opacity) — this is the
  page's core visual metaphor: cause-and-effect between a client request
  and the machinery it triggered is a spatial fact you see on the lane,
  not a nesting rule you read in text. Satellite color follows the `kind-*`
  channel (classifier/title/subagent), same hues as before.
- **High-volume sessions collapse repeated same-route requests into a
  single stack node** — a pill shape (not a circle, to read as distinct
  from a single request), labeled with a count (e.g. "337×"). Verified
  against a real 342-request session: expanding every turn inline would
  make the lane unreadable, so the stack node is the answer to "how does a
  lane hold hundreds of nodes," not more lanes and not a scroll-heavy
  single lane. Clicking it shows stack-level aggregate detail (count, total
  cost) in the right panel, distinct from a single node's detail.
- **A persistent detail panel occupies a fixed 380px right column** —
  never a modal, never a dock that covers the page. Default state shows
  aggregate stats for whatever's currently in view (session/request/error
  counts, total cost). Clicking any node — client, satellite, or a
  collapsed stack — swaps the panel to that node's full detail (route,
  status, rationale, token/cost/latency facts, a link into the full
  transcript). Clicking elsewhere never navigates the lane list away; only
  an explicit "view session →" action in the detail panel does that.
- **Toolbar above the lane list**: free-text search (session id, provider,
  model, first message), filter chips (all / errors only / client only),
  a time-window select, and a result count — same search-first posture as
  the previous generation's Sessions index philosophy, now serving the
  merged page instead of a separate one.

**Open / not yet finalized:** row/node visual design has been agreed at the
composition level (full-width header line, full-width timeline below,
stack node for high-volume sessions) and stress-tested against real data
pulled from `/data/arbiter/arbiter.db` (a clean 4-request session, a real
8-request/4-error broken run, and a real 342-request session) — not
synthetic placeholder rows. The remaining open item is real-usage tuning:
node sizing/spacing/proportions may need adjustment once this is live and
used day to day, per explicit agreement that this is expected and fine to
defer. The session-id chip's dot-marker convention from the previous
generation ("a bordered chip with a dot-marker, not a plain muted link")
remains dropped and not explicitly re-decided either way.

### Session transcript

Unchanged from the previous generation of this spec — this page's design
was not revisited this round and nothing here contradicts it.

- **No chat bubbles.** Message bodies here run from a few words to 35k+
  characters (skill dumps, trace logs) — bubble containers assume
  short-form chat and break down at that length. Each turn is a
  full-width block with a colored **left border** (not a filled bubble)
  marking direction: user = secondary blue, assistant = primary orange.
- **Metadata lives outside the text flow**, not as a header row between
  turns — pull cost/latency/status/tokens/model into a rail/sidebar read
  *alongside* the conversation, not *through* it. The original complaint
  was metadata "sitting between everything else"; any layout that
  re-introduces an inline metadata row between turns repeats that bug.
- **No nested scrollboxes.** The page scrolls once. Long text (>~5 lines)
  fades out with a gradient mask and a "show more" toggle that expands
  it inline — never an inner `overflow:auto` box.
- **Full-screen popover** for content too long to read comfortably even
  expanded inline: a `position:fixed` overlay panel with a char count and
  a close control, opened without navigating away or losing scroll
  position. **Must lock background scroll** while open (toggle
  `overflow:hidden` on `<html>` on open, restore on close/Esc) — a popover
  that lets the page scroll behind it is a bug, not a variant.
- **Tool calls are content-first, not raw-JSON-first.** Render the
  meaningful part per known tool name (`terminal` → the command string,
  `grep`/search tools → pattern + path, file-edit tools → the path) with
  the JSON envelope (`id`/`name`/`input` wrapper) hidden behind a "raw
  JSON" toggle. Unrecognized tool names fall back to a generic JSON dump.
  Tool call/result blocks use the `tool` color, not `warn` — a tool call
  is neutral information, not a problem state.
- **Guardrail-modified fields** (e.g. a system preamble with
  Hermes-specific content stripped before the request left for the
  provider) default to **collapsed**, shown only as a `warn`-colored
  "modified by guardrail" chip. Clicking reveals an **inline line-level
  diff** — unified-diff style, editor-familiar: unchanged lines as muted
  context, removed lines with a red `−` gutter and strikethrough, added
  lines with a green `+` gutter, all in original line order. This is
  line-level, not word-level — a replace is just an adjacent removed line
  followed by an added line, which also covers pure insertion/injection
  and pure deletion without a separate mechanism for each.

### Discovery

Not visually redesigned this round — needs its own pass. The previous
generation's structural/backend requirements still stand and are carried
forward unchanged:

- Needs backend/query changes alongside the visual pass, not visual-only:
  **dedupe by session, not raw request** (a repeated system prompt across
  200 requests in one session should count once), add **role/block-type
  filter chips** (system/user/assistant × text/tool_use/tool_result), and
  **cluster by User-Agent** (already a captured header — group blocks by
  originating client, e.g. Claude Code vs. o‍pencode vs. Claude Desktop,
  as collapsible sections).
- Block preview text reuses the same full-screen popover as the
  transcript and sessions pages — one popover mechanism, shared call
  sites, not separate implementations.

### Overview

**Designed, built, and iterated once against real use.** The landing page is a
routing-flow diagram (aliases on the left, models reached on the right, ribbon
width = request share) above a KPI strip, with a config-change compare mode. The
mockup that settled it is `design/overview-mockups/f3-overview-styled.html`; the
authoritative description of behaviour is README's "Admin web UI" section, which
is maintained — this note is design history.

Two decisions here are worth keeping because they were not obvious:

- **The header's `$/24h` stat is real**, not the mockup's `$18/24h` placeholder.
- **Compare mode is entered by picking an anchor, not by a mode button.** The
  first implementation had a Single/Compare button pair beside the anchor select
  and the two could contradict each other — picking a change in single mode did
  nothing, and clicking Compare submitted without an anchor and rendered a 400.
  Mode is now *derived* from whether an anchor is set, which removed the whole
  class of defect rather than patching the symptoms.

### Cross-page conventions

- A concept that works on one page (kind color, popover, mono-for-
  verbatim-data, the app-chrome header) must be reused verbatim elsewhere
  rather than re-invented.
- Every structural decision here that comes from the previous generation's
  work was checked against **real Arbiter session data** pulled from its
  own SQLite store, not synthetic placeholder text. The current round's
  merged-Sessions-page exploration used representative sample data but has
  **not yet** been checked against real dense session data (20+ request
  sessions, heavy satellite fan-out) — that check is part of the open
  session-lane visual round, not yet done.
