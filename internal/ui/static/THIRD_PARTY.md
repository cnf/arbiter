# Third-party assets

Vendored into `internal/ui/static/` and embedded in the binary. Nothing is
fetched at runtime and no CDN is involved, so the UI works with no network
access of its own and a serving host needs no package manager.

| Asset | Version | Licence | Upstream |
| --- | --- | --- | --- |
| `htmx.min.js` | 2.0.10 | 0BSD | https://github.com/bigskysoftware/htmx |
| `LICENSE.htmx` | — | 0BSD (licence text) | https://unpkg.com/htmx.org@2.0.10/LICENSE |

htmx is used for in-page interactions only (filter swaps, lazy fragment loads,
the reload toast); navigation is ordinary links. Note the licence is **0BSD**,
not BSD-2-Clause: the zero-clause variant drops the attribution requirement
entirely, which is why there is no separate attribution obligation here — the
licence file is included because the source distribution ships it, not because
it is required.

Files were fetched once, at implementation time, from the versions named above.
Everything else under `internal/ui/static/` is ours, not vendored — `app.css`
and the small per-page scripts (`discovery.js`, `laneTimeline.js`, …).

uPlot was vendored here until #54: the old Overview was a pivot table with a
uPlot time-series chart, and the newui rebuild replaced that page with a
server-rendered SVG sankey. The chart, its data endpoint and the library were
deleted together — the current Overview computes its geometry in Go, so the UI
now carries no charting dependency at all.
