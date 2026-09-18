package guardrail

import (
	"context"
	"sync"
	"time"

	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// RateLimitGuardrail enforces per-minute/per-day request-count caps using a
// counter that is seeded from the store and kept in memory between writes.
//
// The seeding is the point. A purely in-memory counter is thrown away whenever
// the pipeline is rebuilt, which happens on every config save — so a per-day cap
// of 1000 became 1000 again after each reload, and the limit was meaningless in
// practice. Seeding from the store also makes the cap survive a restart, which
// is arguably more correct than the original behaviour even without reloads.
//
// The in-memory count is a delta on top of the seeded baseline rather than the
// whole truth: the store is queried once (lazily, on first use), and requests
// recorded since are added locally. This keeps the hot path allocation-free and
// free of a query per request, at the cost of the count being approximate if
// another process writes to the same store — which cannot happen, since Arbiter
// is one binary per deployment.
//
// A nil counter source means nothing is persisted: counting starts at zero, which
// is the behaviour before seeding existed and the correct degradation when no
// store is configured.
type RateLimitGuardrail struct {
	name      string
	perMinute int
	perDay    int

	// counts answers "how many requests in the last <duration>", from the store.
	// Nil means count only what this process has seen.
	counts CountSource

	mu sync.Mutex

	// seeded records whether the baseline has been read from the store yet.
	// Lazy so startup does not depend on the store being queryable, and so a
	// guardrail that is never reached costs no query.
	seeded bool
	// minuteBaseline/dayBaseline are the store's counts as of seed time.
	minuteBaseline int
	dayBaseline    int
	// minuteStart/dayStart bound the local delta's window. They are set at seed
	// time so the local count and the baseline describe the same span.
	minuteStart time.Time
	dayStart    time.Time
	// minuteCount/dayCount are requests seen locally since the baseline.
	minuteCount int
	dayCount    int
}

// CountSource reports how many requests the store recorded in the last window.
// A narrow interface so this package never depends on the store's wider surface,
// and so tests can substitute a fake — the shape CostLatencyLookup and Pinner
// both establish.
//
// Implementations should count requests Arbiter actually made, including its own
// internal calls (classifier, title-gen): those are real upstream requests and a
// rate limit that ignored them would not mirror the upstream's own view.
type CountSource interface {
	// CountSince returns the number of stored requests at or after since.
	CountSince(ctx context.Context, since time.Time) (int, error)
}

// NewRateLimitGuardrail creates a rate limit guardrail. counts may be nil, in
// which case the caps apply only to requests this process has seen.
func NewRateLimitGuardrail(name string, perMinute, perDay int, counts CountSource) *RateLimitGuardrail {
	return &RateLimitGuardrail{
		name:      name,
		perMinute: perMinute,
		perDay:    perDay,
		counts:    counts,
	}
}

func (rlg *RateLimitGuardrail) Name() string {
	return rlg.name
}

// ApplyPre increments the request counters and rejects the request once either
// window's cap is exceeded. A cap of 0 means "unlimited" for that window, so
// config doesn't need a magic large number to opt out.
func (rlg *RateLimitGuardrail) ApplyPre(ctx context.Context, req *types.NormalizedRequest) (*types.NormalizedRequest, error) {
	rlg.mu.Lock()
	defer rlg.mu.Unlock()

	now := time.Now()
	rlg.seed(ctx, now)

	// Roll the local windows. A roll resets the local delta AND the baseline is
	// no longer relevant to the new window, so it is re-read on the next seed.
	if now.Sub(rlg.minuteStart) >= time.Minute {
		rlg.minuteStart = now
		rlg.minuteCount = 0
		rlg.minuteBaseline = 0
	}
	if now.Sub(rlg.dayStart) >= 24*time.Hour {
		rlg.dayStart = now
		rlg.dayCount = 0
		rlg.dayBaseline = 0
	}

	minuteTotal := rlg.minuteBaseline + rlg.minuteCount
	dayTotal := rlg.dayBaseline + rlg.dayCount

	if rlg.perMinute > 0 && minuteTotal >= rlg.perMinute {
		msg := formatLimit("per-minute", minuteTotal, rlg.perMinute)
		return nil, arbitererrors.NewGuardrailError(msg, 429, nil)
	}
	if rlg.perDay > 0 && dayTotal >= rlg.perDay {
		msg := formatLimit("per-day", dayTotal, rlg.perDay)
		return nil, arbitererrors.NewGuardrailError(msg, 429, nil)
	}

	rlg.minuteCount++
	rlg.dayCount++
	return req, nil
}

// seed reads the store's counts once, setting the baselines and the window
// starts together so the two halves of the count describe the same span.
//
// A store error is not fatal: the guardrail falls back to counting only what it
// has seen, which is the old behaviour. Refusing every request because a count
// query failed would turn a store hiccup into an outage, and under-counting is
// the safer failure for a rate limit.
func (rlg *RateLimitGuardrail) seed(ctx context.Context, now time.Time) {
	if rlg.seeded || rlg.counts == nil {
		return
	}
	rlg.seeded = true
	rlg.minuteStart = now
	rlg.dayStart = now

	if rlg.perMinute > 0 {
		if n, err := rlg.counts.CountSince(ctx, now.Add(-time.Minute)); err == nil {
			rlg.minuteBaseline = n
		}
	}
	if rlg.perDay > 0 {
		if n, err := rlg.counts.CountSince(ctx, now.Add(-24*time.Hour)); err == nil {
			rlg.dayBaseline = n
		}
	}
}

func (rlg *RateLimitGuardrail) ApplyPost(ctx context.Context, resp *types.NormalizedResponse, route types.Route) (*types.NormalizedResponse, error) {
	// No-op for post — counting happens on the way in, not the way out.
	return resp, nil
}

func (rlg *RateLimitGuardrail) ShouldRun(req *types.NormalizedRequest) bool {
	return true
}

// formatLimit renders a rate-limit refusal. It names the window and the counts so
// the operator can tell a genuine cap from a mis-set one without guessing.
func formatLimit(window string, count, limit int) string {
	return window + " rate limit exceeded (" + itoa(count) + "/" + itoa(limit) + ")"
}

// itoa avoids pulling strconv in for one call in a hot-ish path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
