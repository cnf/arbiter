package store

import (
	"context"
	"fmt"
	"time"
)

// Measures is the counter set the Overview page measures anything by — one
// route, or a whole window — together with the derived rates.
//
// It exists as its own type so the formulas have exactly one definition. The
// KPI strip summarises a window and the drawer summarises a single route; if
// each computed cache-hit or cost-per-1M from raw columns itself, the strip and
// the drawer could disagree about what those words mean, which is precisely the
// bug a page whose purpose is spotting a bad cache rate cannot afford. Both
// embed this instead.
type Measures struct {
	Requests int64   `json:"requests"`
	CostUSD  float64 `json:"cost_usd"`

	// Tokens counts input+output+cache_read+cache_write — every token moved,
	// which is the denominator the cost-per-1M metric divides by. Cache tokens
	// are inside it deliberately: the provider reports them disjointly from
	// input_tokens, so excluding them would understate the volume by roughly an
	// order of magnitude on a cache-heavy route and turn cost-per-1M into a
	// number that tracks nothing real.
	Tokens int64 `json:"tokens"`

	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`

	Errors       int64 `json:"errors"`
	AvgLatencyMs int64 `json:"avg_latency_ms"`
}

// CostPer1MTokens is the rate metric.
//
// Cost per *request* is deliberately not offered anywhere: request sizes on
// this traffic fluctuate by orders of magnitude, so a per-request average
// tracks how big the calls happened to be rather than how expensive the route
// is. Per-token is the comparable one.
//
// Zero tokens yields zero rather than a division by zero — a route that
// errored on every call has real requests, real latency, and no tokens, and
// must still render.
func (m Measures) CostPer1MTokens() float64 {
	if m.Tokens == 0 {
		return 0
	}
	return m.CostUSD / (float64(m.Tokens) / 1_000_000)
}

// CacheHitRate is the share of *prompt* tokens that were served from the
// provider's cache, in [0,1].
//
// The denominator is cache_read + input_tokens, not the full token count: only
// prompt tokens are cacheable, so folding output tokens in would dilute the
// rate with volume that was never eligible and make a well-cached route look
// mediocre. Cache *writes* are excluded from the denominator too — a write is
// the cost of populating the cache, not a missed read.
//
// Zero cacheable tokens yields zero, and callers that must distinguish "0%
// cache hit" from "nothing cacheable happened" should test Cacheable first —
// the page does, and renders no gauge at all in the second case rather than an
// empty one implying a miss.
func (m Measures) CacheHitRate() float64 {
	cacheable := m.CacheReadTokens + m.InputTokens
	if cacheable == 0 {
		return 0
	}
	return float64(m.CacheReadTokens) / float64(cacheable)
}

// Cacheable reports whether any prompt tokens moved at all, so a caller can
// tell a real 0% cache hit from nothing having been cacheable.
func (m Measures) Cacheable() bool { return m.CacheReadTokens+m.InputTokens > 0 }

// ErrorRate is the share of requests that failed, in [0,1].
func (m Measures) ErrorRate() float64 {
	if m.Requests == 0 {
		return 0
	}
	return float64(m.Errors) / float64(m.Requests)
}

// add accumulates another counter set into this one. Latency is deliberately
// left alone: averaging two averages weights a 1-request group the same as a
// 1000-request one, so callers that fold rows together handle latency
// explicitly (RoutingFlow's remainder bucket sums it weighted by request count
// and divides once at the end).
func (m *Measures) add(o Measures) {
	m.Requests += o.Requests
	m.CostUSD += o.CostUSD
	m.Tokens += o.Tokens
	m.InputTokens += o.InputTokens
	m.OutputTokens += o.OutputTokens
	m.CacheReadTokens += o.CacheReadTokens
	m.CacheWriteTokens += o.CacheWriteTokens
	m.Errors += o.Errors
}

// RoutingEdge is one alias→model route over a window: what a client asked for
// on the left, what it actually reached on the right, and everything the
// Overview page measures that route by.
//
// The grain is (alias, provider, model) rather than (alias, model): the same
// model name can be served by more than one provider, and collapsing that
// would merge two routes whose cost and cache behaviour differ. Provider is
// also what colours the ribbon.
//
// Alias is "" for a request that named a concrete model instead of an alias.
// That is not an absence to be filtered out — it is a real and common route
// (a client bypassing the router entirely), and the page labels it explicitly
// rather than dropping it. The presentation layer owns that label; the store
// reports the empty string.
type RoutingEdge struct {
	Alias    string `json:"alias"`
	Provider string `json:"provider"`
	Model    string `json:"model"`

	Measures

	// FoldedRoutes is how many real routes this edge stands for. It is 0 on
	// every genuine route and >0 only on the single remainder edge RoutingFlow
	// appends past its cap, which is what IsRemainder tests.
	FoldedRoutes int `json:"folded_routes,omitempty"`
}

// IsRemainder reports whether this edge is the "everything past the cap" bucket
// rather than a real route.
//
// Callers must ask this instead of pattern-matching the empty Alias/Model
// fields: an empty Alias is also a legitimate route (a client that named a
// concrete model), so the fields alone cannot tell a bucket from a bypass.
func (e RoutingEdge) IsRemainder() bool { return e.FoldedRoutes > 0 }

// MaxRoutingEdges caps how many routes RoutingFlow returns individually.
//
// It is small by the standards of the other limits in this package because a
// flow diagram is read, not scrolled: past a few dozen ribbons the picture
// stops answering "where does traffic go" and starts obscuring it. The routes
// beyond the cap are not dropped — RoutingFlow folds them into one remainder
// edge so the totals still add up, which is the whole reason the cap is safe.
const MaxRoutingEdges = 24

// RoutingFlow returns a window's alias→model routes, busiest first, capped at
// limit individually with everything past the cap folded into a single
// remainder edge.
//
// Only client traffic is counted (kind = 'client'). Classifier and
// title-generation calls are Arbiter's own machinery rather than routing
// decisions a client made, and mixing them into a flow diagram of "where did
// my requests go" would answer a different question — the same reason every
// other aggregate in this package filters the same way.
//
// The remainder edge carries the empty string in Alias/Provider/Model: it is
// not a route, it is a bucket, and the presentation layer recognises it by
// asking IsRemainder rather than by matching a magic label the store invented.
// Its metrics are real sums, so a KPI strip computed from the returned edges
// still totals the whole window.
//
// Measured cost on the live store (1.1GB, ~16k request rows, 7-day window):
// ~30ms. That is why there is no cache here, unlike the Discovery ledger — this
// is an indexed GROUP BY over `requests`, not a scan of the content-ref join
// table, and wrapping it in a TTL would add a staleness surface to buy nothing.
func (r *Reader) RoutingFlow(ctx context.Context, w Window, limit int) ([]RoutingEdge, error) {
	if limit <= 0 || limit > MaxRoutingEdges {
		limit = MaxRoutingEdges
	}

	where, args := w.tsClause("ts")
	q := `
SELECT
    COALESCE(alias_used, ''),
    provider,
    model,
    COUNT(*),
    COALESCE(SUM(cost_usd), 0),
    CAST(COALESCE(SUM(input_tokens), 0)       AS INTEGER),
    CAST(COALESCE(SUM(output_tokens), 0)      AS INTEGER),
    CAST(COALESCE(SUM(cache_read_tokens), 0)  AS INTEGER),
    CAST(COALESCE(SUM(cache_write_tokens), 0) AS INTEGER),
    CAST(COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END), 0) AS INTEGER),
    CAST(COALESCE(AVG(latency_ms), 0)         AS INTEGER)
FROM requests
WHERE ` + where + ` AND kind = 'client'
GROUP BY COALESCE(alias_used, ''), provider, model
ORDER BY COUNT(*) DESC, COALESCE(SUM(cost_usd), 0) DESC`

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("routing flow: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []RoutingEdge{}
	var remainder RoutingEdge
	var folded int
	for rows.Next() {
		var e RoutingEdge
		if err := rows.Scan(&e.Alias, &e.Provider, &e.Model, &e.Requests, &e.CostUSD,
			&e.InputTokens, &e.OutputTokens, &e.CacheReadTokens, &e.CacheWriteTokens,
			&e.Errors, &e.AvgLatencyMs); err != nil {
			return nil, fmt.Errorf("scan routing edge: %w", err)
		}
		e.Tokens = e.InputTokens + e.OutputTokens + e.CacheReadTokens + e.CacheWriteTokens

		if len(out) < limit {
			out = append(out, e)
			continue
		}
		// Past the cap: fold into the remainder. Latency is summed weighted by
		// request count here and averaged once at the end — averaging an
		// average would weight a 1-request route the same as a 1000-request
		// one, which is why Measures.add leaves latency to the caller.
		folded++
		remainder.AvgLatencyMs += e.AvgLatencyMs * e.Requests
		remainder.add(e.Measures)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("routing flow rows: %w", err)
	}
	if folded > 0 {
		if remainder.Requests > 0 {
			remainder.AvgLatencyMs /= remainder.Requests
		}
		remainder.FoldedRoutes = folded
		out = append(out, remainder)
	}
	return out, nil
}

// EpochAnchor is one config generation, offered as a candidate "before/after"
// boundary for the Overview page's compare mode.
//
// Epoch is the config hash; Started/Ended are the first and last request
// timestamps observed under it, as stored text. An anchor is identified by
// Started — that is the instant the config took effect as far as traffic is
// concerned, which is what a before/after comparison pivots on.
type EpochAnchor struct {
	Epoch    string    `json:"epoch"`
	Started  time.Time `json:"started"`
	Ended    time.Time `json:"ended"`
	Requests int64     `json:"requests"`

	// Merged is how many raw config epochs were collapsed into this anchor
	// (1 when none were). See ConfigEpochs for why they are collapsed at all;
	// the page shows it so a settled anchor is visibly the end of an editing
	// burst rather than a single save.
	Merged int `json:"merged"`
}

// epochBurstGap is how close two config epochs must be to count as one editing
// session rather than two deliberate changes.
//
// Arbiter reloads its config on file change, so the user's editor saves land in
// the store as a burst of epochs seconds apart — the live store shows runs like
// 19:06:33 (2 requests), 19:06:40 (4 requests), then 19:08:07 (754 requests).
// Those are one change, and the meaningful anchor is where it settled.
//
// This is why the filter is a gap and not a request-count floor: a floor throws
// away the whole burst (every member is small) while also keeping unrelated
// epochs that were stable but briefly used. Collapsing by proximity keeps one
// candidate per actual edit, and keeps the *last* one, which is the config that
// then served real traffic.
const epochBurstGap = 3 * time.Minute

// ConfigEpochs lists a window's config generations as compare-mode anchors,
// newest first, with editing-burst runs collapsed into one anchor each.
//
// It exists so the page can offer real change boundaries instead of asking the
// operator to guess a timestamp: "did this config change help?" is the question
// Overview's compare mode is for, and the store already knows when every change
// took effect. Free-form anchors stay possible — this is a convenience list,
// not the only way in.
//
// Rows whose config_epoch is NULL or empty (written before the column existed)
// are skipped rather than merged into one pseudo-epoch: they span an arbitrary
// stretch of history and would offer a boundary that never corresponded to a
// change.
func (r *Reader) ConfigEpochs(ctx context.Context, w Window, limit int) ([]EpochAnchor, error) {
	if limit <= 0 {
		limit = 40
	}

	where, args := w.tsClause("ts")
	q := `
SELECT config_epoch, COUNT(*), MIN(ts), MAX(ts)
FROM requests
WHERE ` + where + ` AND kind = 'client'
  AND config_epoch IS NOT NULL AND config_epoch <> ''
GROUP BY config_epoch
ORDER BY MIN(ts) DESC`

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("config epochs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Collected newest-first, which is also the order the collapse walks: a
	// run's *last* member (the settled config) is the one encountered first,
	// so it becomes the anchor and its predecessors fold into it.
	var out []EpochAnchor
	for rows.Next() {
		var (
			a                  EpochAnchor
			startedRaw, endRaw string
		)
		if err := rows.Scan(&a.Epoch, &a.Requests, &startedRaw, &endRaw); err != nil {
			return nil, fmt.Errorf("scan config epoch: %w", err)
		}
		a.Started = parseStoredTs(startedRaw)
		a.Ended = parseStoredTs(endRaw)
		a.Merged = 1

		// Fold into the current anchor when this epoch *started* within the
		// burst gap of the anchor's own start — one editing session's saves,
		// not two deliberate changes.
		//
		// Start-to-start, and against the anchor rather than the previous
		// member, for a reason live data exposed: consecutive epochs overlap.
		// Requests already in flight when the config reloads are recorded under
		// the old epoch after the new one has begun serving (observed on the
		// live store: an epoch's last request landed three minutes after its
		// successor's first). An end-to-start gap is therefore *negative* across
		// every such boundary and reads as a burst, which made a long-lived
		// config absorb its predecessor — the settled end of a real burst got
		// swallowed by the next day's config, leaving the burst's two throwaway
		// saves behind as the anchor. Comparing starts is immune to the overlap,
		// and comparing against the anchor (not the previous member) keeps a
		// long run of saves from chaining into one ever-growing anchor.
		if n := len(out); n > 0 && !a.Started.IsZero() && !out[n-1].Started.IsZero() &&
			out[n-1].Started.Sub(a.Started) < epochBurstGap {
			out[n-1].Merged += a.Merged
			out[n-1].Requests += a.Requests
			// The anchor keeps the settled epoch's Started: that is the instant
			// the config that actually served traffic took effect, which is
			// what a before/after comparison pivots on.
			continue
		}
		out = append(out, a)
		if len(out) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("config epoch rows: %w", err)
	}
	return out, nil
}

// parseStoredTs turns the store's ts text back into a time.Time.
//
// The column holds Go's time.Time.String() layout, which is not RFC3339 and
// which time.Parse cannot guess — writing it was a Stringer call, so reading it
// needs the matching layout spelled out. A value that will not parse yields the
// zero time rather than an error: a timestamp is presentation here (an anchor
// label, a window edge), and one unparseable row should not fail the list.
func parseStoredTs(raw string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", raw)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
