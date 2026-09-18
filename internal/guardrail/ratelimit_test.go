package guardrail

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/pkg/types"
)

// Rate-limit counters, seeded from the store.
//
// The behaviour these exist to prove: a cap is not reset by the pipeline being
// rebuilt (a config reload) or the process restarting. Before seeding, a per-day
// cap of 1000 became 1000 again after every config save, which made the limit
// meaningless in practice.

// fakeCounts is a stand-in for the store's count query. It is deliberately
// settable per window so a test can model "the store already recorded N".
type fakeCounts struct {
	lastMinute int
	lastDay    int
	err        error
	calls      int
}

// CountSince distinguishes the windows by how far back the caller asked. The
// boundary is mid-way between the two so neither window's edge can be mistaken
// for the other's — an earlier version used 1m+1s, which made "24h ago" and
// "1m ago" both fall through to the day branch and silently returned the wrong
// count.
func (f *fakeCounts) CountSince(ctx context.Context, since time.Time) (int, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	ago := time.Since(since)
	if ago < 30*time.Minute {
		return f.lastMinute, nil
	}
	return f.lastDay, nil
}

func anyReq() *types.NormalizedRequest {
	return &types.NormalizedRequest{Messages: []types.Message{{Role: "user", Content: []types.ContentBlock{{Type: "text", Text: "x"}}}}}
}

// TestRateLimitSeedsFromStore is the core fix: a fresh guardrail over a store
// that already recorded requests starts from that count, not from zero.
func TestRateLimitSeedsFromStore(t *testing.T) {
	counts := &fakeCounts{lastMinute: 4, lastDay: 9}
	g := NewRateLimitGuardrail("rl", 5, 0, counts) // 5/min, 4 already used

	// The fifth request is allowed...
	if _, err := g.ApplyPre(context.Background(), anyReq()); err != nil {
		t.Fatalf("5th request rejected: %v", err)
	}
	// ...and the sixth is not, because the store's 4 count.
	_, err := g.ApplyPre(context.Background(), anyReq())
	if err == nil {
		t.Fatal("expected the per-minute cap to trip at 5, counting the seeded 4")
	}
	if !strings.Contains(err.Error(), "per-minute") {
		t.Errorf("error = %v, want a per-minute refusal", err)
	}
}

// TestRateLimitWithoutSeedingWouldNotTrip is the counterfactual, proving the
// seeding is what does the work: with a store reporting 0 the same cap allows
// many more requests.
func TestRateLimitWithoutSeedingWouldNotTrip(t *testing.T) {
	g := NewRateLimitGuardrail("rl", 5, 0, &fakeCounts{})

	for i := 0; i < 4; i++ {
		if _, err := g.ApplyPre(context.Background(), anyReq()); err != nil {
			t.Fatalf("request %d rejected unexpectedly: %v", i+1, err)
		}
	}
}

// TestRateLimitSurvivesRebuild is the reload case stated directly: a guardrail
// built fresh (as a reload does) still honours requests the store already
// recorded.
func TestRateLimitSurvivesRebuild(t *testing.T) {
	// The store has already recorded exactly the cap's worth, so the next request
	// is the one that must be refused. (999 would still allow one more, which is
	// correct — the cap trips AT the limit, not before it.)
	counts := &fakeCounts{lastDay: 1000}

	// A reload builds a brand-new guardrail. Its in-memory count is zero, so
	// only the seeded baseline can make the cap trip.
	after := NewRateLimitGuardrail("rl", 0, 1000, counts)
	if _, err := after.ApplyPre(context.Background(), anyReq()); err == nil {
		t.Fatal("a rebuilt guardrail allowed a request that should have hit the per-day cap")
	}
}

// TestRateLimitTripsAtTheLimitNotBefore pins the boundary the test above depends
// on: a cap of N allows exactly N requests, and refuses the N+1st. Getting this
// off by one would either leak a request or refuse one early, and neither shows
// up as a failure anywhere else.
func TestRateLimitTripsAtTheLimitNotBefore(t *testing.T) {
	// N already recorded, cap N: the next one is refused.
	g := NewRateLimitGuardrail("rl", 0, 10, &fakeCounts{lastDay: 10})
	if _, err := g.ApplyPre(context.Background(), anyReq()); err == nil {
		t.Error("cap reached in the store, but a request was allowed")
	}
	// N-1 recorded, cap N: the next one is allowed.
	g = NewRateLimitGuardrail("rl", 0, 10, &fakeCounts{lastDay: 9})
	if _, err := g.ApplyPre(context.Background(), anyReq()); err != nil {
		t.Errorf("one slot remaining, but the request was refused: %v", err)
	}
}

// TestRateLimitNilCountsStillWorks proves the degradation: with no store, the
// cap counts only what this process has seen — the behaviour before seeding.
func TestRateLimitNilCountsStillWorks(t *testing.T) {
	g := NewRateLimitGuardrail("rl", 2, 0, nil)

	if _, err := g.ApplyPre(context.Background(), anyReq()); err != nil {
		t.Fatalf("1st request: %v", err)
	}
	if _, err := g.ApplyPre(context.Background(), anyReq()); err != nil {
		t.Fatalf("2nd request: %v", err)
	}
	if _, err := g.ApplyPre(context.Background(), anyReq()); err == nil {
		t.Fatal("expected the cap to trip at 2 without a store")
	}
}

// TestRateLimitStoreErrorFallsBackToLocalCounting proves a failing count query
// degrades rather than failing every request. Turning a store hiccup into an
// outage would be worse than under-counting.
func TestRateLimitStoreErrorFallsBackToLocalCounting(t *testing.T) {
	g := NewRateLimitGuardrail("rl", 2, 0, &fakeCounts{err: errors.New("store unavailable")})

	if _, err := g.ApplyPre(context.Background(), anyReq()); err != nil {
		t.Fatalf("1st request: %v", err)
	}
	if _, err := g.ApplyPre(context.Background(), anyReq()); err != nil {
		t.Fatalf("2nd request: %v", err)
	}
	if _, err := g.ApplyPre(context.Background(), anyReq()); err == nil {
		t.Fatal("expected local counting to still enforce the cap")
	}
}

// TestRateLimitSeedsOnce proves the store is queried once per guardrail, not per
// request — the seeding is a baseline, and a query on the hot path would be a
// cost the cap does not justify.
func TestRateLimitSeedsOnce(t *testing.T) {
	counts := &fakeCounts{}
	g := NewRateLimitGuardrail("rl", 100, 100, counts)

	for i := 0; i < 5; i++ {
		if _, err := g.ApplyPre(context.Background(), anyReq()); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
	// One call per configured window (minute + day), then never again.
	if counts.calls != 2 {
		t.Errorf("store queried %d time(s), want 2 (one per window, once)", counts.calls)
	}
}

// TestRateLimitZeroCapIsUnlimited proves 0 still means "unlimited", so config
// needs no magic number to opt out, and a disabled window is not queried.
func TestRateLimitZeroCapIsUnlimited(t *testing.T) {
	counts := &fakeCounts{lastDay: 100000}
	g := NewRateLimitGuardrail("rl", 0, 0, counts)

	for i := 0; i < 10; i++ {
		if _, err := g.ApplyPre(context.Background(), anyReq()); err != nil {
			t.Fatalf("request %d rejected with both caps at 0: %v", i+1, err)
		}
	}
	if counts.calls != 0 {
		t.Errorf("store queried %d time(s) with no cap configured, want 0", counts.calls)
	}
}

// TestRateLimitRefusalNamesTheWindowAndCounts proves the error is diagnosable —
// the operator can tell a real cap from a mis-set one.
func TestRateLimitRefusalNamesTheWindowAndCounts(t *testing.T) {
	g := NewRateLimitGuardrail("rl", 1, 0, &fakeCounts{})
	if _, err := g.ApplyPre(context.Background(), anyReq()); err != nil {
		t.Fatalf("1st request: %v", err)
	}
	_, err := g.ApplyPre(context.Background(), anyReq())
	if err == nil {
		t.Fatal("expected a refusal")
	}
	msg := err.Error()
	if !strings.Contains(msg, "per-minute") || !strings.Contains(msg, "1/1") {
		t.Errorf("error %q does not name the window and counts", msg)
	}
}
