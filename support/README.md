# support/

Things that are useful to have but are not part of the shipped binary. Nothing
here is compiled into `arbiter`; nothing here is imported by anything that is.
`git` tracks it, and so does the laptop backup, which is the point — this is
where a thing goes when it is worth keeping but not worth shipping.

## preview/

Three throwaway HTTP servers that serve the admin UI for eyeballing it without
running the real binary against a real database. They were previously
`cmd/bigpreview`, `cmd/previewserver` and `cmd/realpreview`.

Each is its own package, so `go build ./...` still compiles them (as it did
when they lived in `cmd/`) and `go vet` still checks them.

- `bigpreview` — seeds a 40-turn synthetic conversation into a scratch DB in
  a temp dir, then serves it on :8098. Use it for list/density work: it exists
  to make the sessions list long enough to be awkward.
- `previewserver` — seeds a 2-turn synthetic conversation with a
  pre-guardrailed variant (so the "as sent"/"as guardrailed" toggle has
  something to toggle), on :8099. Use it for the transcript/detail pane.
- `realpreview` — serves the UI **read-only** against `build/livecheck.db`
  on :8097. For visual checks against real traffic. It never writes, and it
  does not use the real binary's port.

### Running one

    go run ./support/preview/bigpreview
    go run ./support/preview/previewserver
    go run ./support/preview/realpreview

Same invocation shape as before the move — the directory just changed.
`realpreview` reads `build/livecheck.db` relative to the **current directory**,
so run it from the repo root.

## notes/

Working documents that are worth keeping but are not user-facing
documentation. `docs/` is for how to *use* Arbiter; this is for how it was
reasoned about.

- `refactor-findings-2026-09-27.md` — the discovery pass that drove the
  refactor: the pain-point list, what was fixed and what was deliberately left,
  and corrections for findings that turned out to be wrong.
- `trace-request-lifecycle.md` — a hop-by-hop trace of one HTTP request through
  the pipeline, as a tree.

Both lived in `build/` until now, which was wrong: `build/` holds a 1.2GB
scratch database, font caches and cross-compiled binaries, and is ignored by
git, so anything placed there is one `git clean` away from gone and never
reaches the backup.

## The rest

- `fakellm.yaml` — config for the `mockllm` devenv process (a stand-in
  OpenAI-shaped upstream). Referenced by `devenv.nix`; do not move it without
  updating that path.
- `downloads/`, `design/` — gitignored fetched artifacts (upstream price lists,
  mockups). They live here on disk, not in git.