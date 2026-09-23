---
version: alpha
name: Arbiter
description: A dense, data-first admin dashboard for a single-user LLM proxy — built to be scanned and compared, not admired.
colors:
  primary: "#FF6700"
  secondary: "#0098FF"
  secondary-ink: "#0072CC"
  ink: "#14161A"
  ink-dark: "#E6E8EC"
  bg: "#FBFBFC"
  bg-dark: "#15171B"
  surface: "#FFFFFF"
  surface-dark: "#1B1E23"
  line: "#D9DCE1"
  line-dark: "#2B2F36"
  muted: "#6B7280"
  muted-dark: "#9AA1AC"
  on-primary: "{colors.ink}"
  on-secondary: "{colors.ink}"
  ok: "#16794A"
  ok-dark: "#5FC98D"
  warn: "#A35A00"
  warn-dark: "#E0A44A"
  err: "#B3261E"
  err-dark: "#FF8A80"
  note: "#3F3F8F"
  note-dark: "#A6A6F0"
  kind-classifier: "#7C5CFC"
  kind-classifier-dark: "#A996FF"
  kind-title: "#0EA5A5"
  kind-title-dark: "#5FD4D4"
  kind-subagent: "#C2419C"
  kind-subagent-dark: "#E58BCB"
  tool: "#B8860B"
  tool-dark: "#E0B34A"
typography:
  h1:
    fontFamily: ui-sans-serif, system-ui, "Segoe UI", Roboto, sans-serif
    fontSize: 1.15rem
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
    fontSize: 0.6875rem
    fontWeight: 600
    letterSpacing: "0.04em"
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
  sm: 3px
  md: 6px
  lg: 10px
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
    rounded: "{rounded.sm}"
    padding: 8px
  button-primary-hover:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.on-primary}"
  button-secondary:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.secondary-ink}"
    rounded: "{rounded.sm}"
    padding: 8px
  button-secondary-hover:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.secondary-ink}"
  card:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.ink}"
    rounded: "{rounded.md}"
    padding: 12px
  table-header:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.muted}"
    typography: "{typography.label-caps}"
  badge-ok:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.ok}"
    rounded: "{rounded.sm}"
  badge-warn:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.warn}"
    rounded: "{rounded.sm}"
  badge-err:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.err}"
    rounded: "{rounded.sm}"
  badge-note:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.note}"
    rounded: "{rounded.sm}"
  tag:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.muted}"
    rounded: "{rounded.pill}"
  input:
    backgroundColor: "{colors.bg}"
    textColor: "{colors.ink}"
    rounded: "{rounded.sm}"
    padding: 4px
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

## Colors

- **Primary (`{colors.primary}`, `#FF6700`):** The one high-emphasis action
  per view — the button that actually does something (start a tail, submit a
  filter). Never used for passive decoration, never used twice in the same
  view at the same weight.
- **Secondary (`{colors.secondary}`, `#0098FF`):** Navigation and reference —
  links, the active nav item, "go look at this" affordances. Lower emphasis
  than primary; it points, it doesn't commit. As flat text-on-white this hue
  reads at 3.03:1, under WCAG AA — where it carries body text or a button
  label, use `secondary-ink` (`#0072CC`, 4.91:1) instead. The bright value
  stays valid for borders, fills, and anything not carrying small text.
- **Ink / Bg / Surface / Line:** The actual page — text, page background,
  card background, and hairline borders. These carry the UI, not the brand.
  Each has a `-dark` pair; the format has no native scheme-variant token
  yet, so light is canonical and `*-dark` is the `prefers-color-scheme: dark`
  swap-in, both wired through `color-scheme: light dark` same as today.
- **Muted:** Secondary text — metadata, timestamps, column headers, anything
  read after the primary value in a cell, not instead of it.
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
`on-primary`/`on-secondary` resolve to `{colors.ink}`. At these lightnesses,
white-on-orange and white-on-blue both land under the WCAG AA 4.5:1 text
threshold (≈2.9:1 and ≈3.0:1); dark text clears it (~6:1) on both. This is
also just the correct move stylistically — bright saturated fills read
better with dark text (see YC's own orange).

## Typography

System font stacks only — this ships inside a self-hosted admin tool, so
nothing should ever block on a webfont fetch. Sans for every UI chrome
(nav, labels, buttons, prose); `data-mono` for anything the store produced
verbatim — hashes, session keys, model names, token counts, JSON. Hierarchy
is carried by size and weight, not typeface switching: `h1`/`h2` step down
from the page title into section headers, `label-caps` marks a column
header or a status chip, `body-md`/`body-sm` are prose and cell text.

## Layout

4px baseline scale. `xs` for the gap between a value and its unit inside one
control, `sm` for gaps inside a control cluster (a filter row, a button
group), `md` for the padding inside a card/table cell cluster, `lg` between
stacked components on a page, `xl` between page sections. Max content width
stays wide (1600px) — this is a table-reading tool, columns want the room.

## Shapes

Flat by design — no elevation/shadow system; a hairline `line` border is
the only depth cue a bordered surface gets. `rounded.sm` (3px) is the
default for anything interactive or data-dense (buttons, inputs, table
container, badges) so corners don't compete with the content. `rounded.md`
(6px) is reserved for the few real "cards" (panels, fact grids).
`rounded.pill` is for tags only.

## Components

- `button-primary` is the one committing action visible at a time — filled
  orange, ink text, no shadow.
- `button-secondary` is a ghost button: `surface` background, blue text,
  used for everything reversible or secondary (reset, cancel, a lower-
  priority nav action).
- `card` is the default bordered surface for a fact grid, panel, or turn —
  `surface` background, `line` border, `rounded.md`.
- `table-header` renders as `label-caps` in `muted` — headers recede so the
  data is what's read.
- `badge-*` are status chips: text-colored, not fill-colored, with a
  matching-tint border — enough signal to scan a column, not enough weight
  to outshine the row's actual value.
- `tag` is the neutral pill for kind/axis labels that aren't a status.
- `input` matches the page background (`bg`, not `surface`) so a focused
  filter field visually recedes into the page until it's actively used.

## Do's and Don'ts

- **Do** keep `primary` to one action per view. If two things feel
  equally important, neither gets the orange — that's a hierarchy bug, not
  a color problem.
- **Do** use `secondary` for anything that navigates or references, never
  for anything that mutates state.
- **Do** use token references (`{colors.primary}`) in components, never
  re-typed hex.
- **Don't** put white text on `primary` or `secondary` fills — both fail
  AA contrast at these lightnesses; pair with `ink` instead.
- **Don't** reach for a shadow. Depth is a border or it isn't there.
- **Don't** let a status color (ok/warn/err/note) double as a brand accent,
  or a scan of the status column stops being trustworthy.

## Page Patterns

Token values and component styles above answer "what a thing looks like."
This section answers "how a page is built" — layout decisions specific
to Arbiter's data, arrived at through iterative mockup review. A build
session should treat these as structural requirements, not suggestions.

### Request rows (requests page)

- Grid: `3px status-bar | 84px time | 1fr chain | auto session | auto cost`.
  Cost/latency is **always the last column**, dead-aligned across every
  row regardless of nesting — achieved via CSS `order`, not by reordering
  markup, so future columns can be added without touching row templates.
- **Routing chain**, not a single model name: collapse to the fewest
  segments that carry information —
  `requested → routed → actual`, dropping a segment wherever consecutive
  values are identical. Drop the word "alias"; a literal-model request
  renders as one segment (`literal: anthropic claude-opus-4-6`), never a
  redundant `X → X`. A real divergence renders all three:
  `debug: openrouter preset/bugspray → google/gemma-4-26b-a4b-it:free`.
- **`request_kind`** (classifier/title/subagent) renders as one chip style,
  distinguished only by the `kind-*` color — never a different tag shape
  per kind. Axis pills (domain/effort/cost_class) sit on their own line
  below the model-name/status line, not beside the chain and not beside
  cost.
- **Threading:** a classifier/title/subagent request spawned by a client
  request renders as a **child row directly above its parent**, newest
  first (chronology reads bottom-to-top within a thread). Only the time
  column gets a `↳` indent marker — never the whole row, and never the
  cost/latency column, so cost stays scannable in a straight line down
  the page regardless of nesting depth.
- **Stream grouping:** repeated/streamed requests stay stacked with a
  count badge (below the "x ago" text, not beside it) as long as nothing
  else happens in between. A subagent request breaks the stack — finish
  the current stack, render the subagent row, start a new stack after it.
- **Status bar:** every row, parent or child, keeps the colored left-edge
  status bar. No row is ever exempt.
- Session id renders as a bordered chip with a dot-marker (not a plain
  muted link) — it's a "conversation handle," not incidental metadata.

### Session transcript

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

### Sessions index

- Lower redesign priority than requests/transcript — it was already
  "mostly good." Apply the requests-page visual language (session-id
  chip, quiet numeric columns, mono for anything store-verbatim) rather
  than inventing new conventions.
- **Search-first, not sort-first.** A single text search box (matches
  session key / provider / model / alias) is the primary way to find a
  session; per-column sort on headers is a secondary affordance, not the
  main interaction — this page is used occasionally and for lookup, not
  continuous monitoring.
- **First-user-message preview column**: a single-line, truncated preview
  of the session's first user message, purely as a quick visual aid for
  recognizing a session at a glance — not a feature to invest further
  design weight in. Click opens the same full-screen popover as the
  transcript page. This column is `1fr` (takes whatever space remains);
  every other column (session/span/turns/tokens/cost/err) must use a
  **fixed pixel width, not `auto`** — `auto`-sized trailing columns
  shift position row-to-row because their width depends on that row's
  own content length, breaking vertical column alignment. This was a
  real bug (screenshot-caught), not a hypothetical: `auto` columns are
  disallowed in any row-repeating grid on this page.

### Discovery

- Needs backend/query changes alongside the visual pass, not visual-only:
  **dedupe by session, not raw request** (a repeated system prompt across
  200 requests in one session should count once), add **role/block-type
  filter chips** (system/user/assistant × text/tool_use/tool_result), and
  **cluster by User-Agent** (already a captured header — group blocks by
  originating client, e.g. Claude Code vs. o‍pencode vs. Claude Desktop,
  as collapsible sections).
- Block preview text reuses the same full-screen popover as the
  transcript and sessions pages — one popover mechanism, three call
  sites, not three implementations.

### Cross-page conventions

- A concept that works on one page (routing-chain chip, kind color,
  popover, mono-for-verbatim-data) must be reused verbatim elsewhere
  rather than re-invented — the four uses of the popover above are one
  component, not four.
- Every structural decision here was checked against **real Arbiter
  session data** pulled from its own SQLite store, not synthetic
  placeholder text — long messages, multi-block tool calls, and a real
  guardrail-redacted preamble all came from actual dogfooded sessions.
  A build session should keep doing this: fixture data that's "nice"
  but shorter/cleaner than real traffic will hide the exact problems
  this redesign exists to fix (35k-char messages, dense tool-call
  sequences, streamed request bursts).
</content>
