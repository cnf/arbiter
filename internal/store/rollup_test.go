package store

import (
	"context"
	"testing"
	"time"
)

// TestRollupContentHashStatsMatchesRepeatedContent is the load-bearing
// correctness test: the incremental rollup must agree with the from-scratch
// aggregation it replaces on the read path, or Discovery would show two
// different numbers depending on which code path happened to answer.
func TestRollupContentHashStatsMatchesRepeatedContent(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	const shared = "a block resent across sessions, long enough to hash meaningfully"
	w.Record(Event{
		TraceID: "t1", SessionKey: "s1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, shared)}},
	})
	w.Record(Event{
		TraceID: "t2", SessionKey: "s2", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, shared)}},
	})
	// Same session as the first request, same block: must not double-count
	// the session, but must count the request.
	w.Record(Event{
		TraceID: "t3", SessionKey: "s1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, shared)}},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	folded, watermark, err := r.RollupContentHashStats(ctx)
	if err != nil {
		t.Fatalf("RollupContentHashStats: %v", err)
	}
	if folded != 3 {
		t.Errorf("folded = %d, want 3", folded)
	}
	if watermark <= 0 {
		t.Errorf("watermark = %d, want positive", watermark)
	}

	repeated, err := r.RepeatedContent(ctx, WindowFrom(time.Hour), 2, 0, 50)
	if err != nil {
		t.Fatalf("RepeatedContent: %v", err)
	}
	if len(repeated) != 1 {
		t.Fatalf("RepeatedContent = %+v, want one block", repeated)
	}

	stats, err := r.ContentHashStats(ctx, 2, 0, 50, 0)
	if err != nil {
		t.Fatalf("ContentHashStats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("ContentHashStats = %+v, want one block", stats)
	}

	if stats[0].Requests != repeated[0].Requests {
		t.Errorf("rollup requests = %d, RepeatedContent requests = %d, want equal",
			stats[0].Requests, repeated[0].Requests)
	}
	if stats[0].Requests != 3 {
		t.Errorf("requests = %d, want 3", stats[0].Requests)
	}
	if stats[0].Sessions != repeated[0].Sessions {
		t.Errorf("rollup sessions = %d, RepeatedContent sessions = %d, want equal",
			stats[0].Sessions, repeated[0].Sessions)
	}
	if stats[0].Sessions != 2 {
		t.Errorf("sessions = %d, want 2 (s1, s2 — resend within s1 must not double-count)", stats[0].Sessions)
	}
}

// TestRollupContentHashStatsIsIncremental proves a second call only folds in
// requests written since the first call's watermark, and that a session seen
// in an earlier batch is correctly remembered rather than re-counted when it
// reappears in a later batch — the scenario an hourly sweep hits constantly
// (the same live conversation spanning two sweep intervals).
func TestRollupContentHashStatsIsIncremental(t *testing.T) {
	w, path := newTestWriter(t)
	ctx := context.Background()

	const shared = "boilerplate the client injects on every turn of this conversation"
	w.Record(Event{
		TraceID: "t1", SessionKey: "s1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, shared)}},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close (first batch): %v", err)
	}

	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	folded, watermark1, err := r.RollupContentHashStats(ctx)
	if err != nil {
		t.Fatalf("first RollupContentHashStats: %v", err)
	}
	if folded != 1 {
		t.Fatalf("first rollup folded = %d, want 1", folded)
	}

	// Second call with nothing new: must be a no-op, not re-scan everything.
	folded, watermark2, err := r.RollupContentHashStats(ctx)
	if err != nil {
		t.Fatalf("second RollupContentHashStats (no-op): %v", err)
	}
	if folded != 0 {
		t.Errorf("no-op rollup folded = %d, want 0", folded)
	}
	if watermark2 != watermark1 {
		t.Errorf("no-op rollup watermark = %d, want unchanged %d", watermark2, watermark1)
	}

	// New request, same session, same block — a later sweep batch. A fresh
	// writer over the same file, mirroring how the real sweep (a separate
	// reader connection) observes rows the long-lived writer process wrote
	// after the previous sweep ran.
	w2, err := NewSQLiteWriter(path, &recordingLogger{})
	if err != nil {
		t.Fatalf("reopen writer: %v", err)
	}
	w2.Record(Event{
		TraceID: "t2", SessionKey: "s1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, shared)}},
	})
	if err := w2.Close(); err != nil {
		t.Fatalf("Close (second batch): %v", err)
	}

	folded, watermark3, err := r.RollupContentHashStats(ctx)
	if err != nil {
		t.Fatalf("third RollupContentHashStats: %v", err)
	}
	if folded != 1 {
		t.Errorf("third rollup folded = %d, want 1", folded)
	}
	if watermark3 <= watermark2 {
		t.Errorf("watermark did not advance: %d -> %d", watermark2, watermark3)
	}

	stats, err := r.ContentHashStats(ctx, 2, 0, 50, 0)
	if err != nil {
		t.Fatalf("ContentHashStats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("ContentHashStats = %+v, want one block", stats)
	}
	if stats[0].Requests != 2 {
		t.Errorf("requests = %d, want 2 (folded across two sweep batches)", stats[0].Requests)
	}
	if stats[0].Sessions != 1 {
		t.Errorf("sessions = %d, want 1 (same session across both batches must not double-count)", stats[0].Sessions)
	}
}

// TestRollupContentHashStatsSkipsSessionlessRequests mirrors
// RepeatedContent's own COUNT(DISTINCT session_key) behaviour: a request
// with no session_key must count toward requests but never toward sessions,
// matching SQL's COUNT(DISTINCT) ignoring NULLs.
func TestRollupContentHashStatsSkipsSessionlessRequests(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	const shared = "a block sent with no session-affinity header at all, long enough to hash"
	w.Record(Event{
		TraceID: "t1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, shared)}},
	})
	w.Record(Event{
		TraceID: "t2", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, shared)}},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, _, err := r.RollupContentHashStats(ctx); err != nil {
		t.Fatalf("RollupContentHashStats: %v", err)
	}

	stats, err := r.ContentHashStats(ctx, 2, 0, 50, 0)
	if err != nil {
		t.Fatalf("ContentHashStats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("ContentHashStats = %+v, want one block", stats)
	}
	if stats[0].Requests != 2 {
		t.Errorf("requests = %d, want 2", stats[0].Requests)
	}
	if stats[0].Sessions != 0 {
		t.Errorf("sessions = %d, want 0 (sessionless requests never count)", stats[0].Sessions)
	}
}

// TestUnseenDiscoveryCount covers the three-state gate UnseenDiscoveryCount
// applies per hash: no mark counts, ignored never counts, and a seen mark
// only stops counting until the block's last_ts moves past the moment it was
// marked seen. Also checks minSessions actually filters (a block below the
// threshold never counts, regardless of state).
func TestUnseenDiscoveryCount(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	// widespread: 2 sessions, never marked -> counts as unseen.
	const widespread = "an unmarked block sent across two different sessions"
	w.Record(Event{
		TraceID: "t1", SessionKey: "s1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("system", 0, 0, widespread)}},
	})
	w.Record(Event{
		TraceID: "t2", SessionKey: "s2", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("system", 0, 0, widespread)}},
	})

	// ignoredBlock: 2 sessions, marked ignored -> never counts.
	const ignoredBlock = "a block the operator has already marked as ignored boilerplate"
	w.Record(Event{
		TraceID: "t3", SessionKey: "s1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("system", 0, 0, ignoredBlock)}},
	})
	w.Record(Event{
		TraceID: "t4", SessionKey: "s2", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("system", 0, 0, ignoredBlock)}},
	})

	// seenBlock: 2 sessions, marked seen and not reappeared since -> does not count.
	const seenBlock = "a block the operator has already looked at and marked seen"
	w.Record(Event{
		TraceID: "t5", SessionKey: "s1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("system", 0, 0, seenBlock)}},
	})
	w.Record(Event{
		TraceID: "t6", SessionKey: "s2", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("system", 0, 0, seenBlock)}},
	})

	// tooNarrow: only 1 session -> excluded by minSessions regardless of state.
	const tooNarrow = "a block confined to a single session, below the threshold"
	w.Record(Event{
		TraceID: "t7", SessionKey: "s1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("system", 0, 0, tooNarrow)}},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, _, err := r.RollupContentHashStats(ctx); err != nil {
		t.Fatalf("RollupContentHashStats: %v", err)
	}

	repeated, err := r.ContentHashStats(ctx, 2, 0, 50, 0)
	if err != nil {
		t.Fatalf("ContentHashStats: %v", err)
	}
	hashFor := map[string]string{}
	for _, rc := range repeated {
		hashFor[rc.Preview] = rc.Hash
	}

	if err := r.SetDiscoveryState(ctx, hashFor[ignoredBlock], DiscoveryIgnored, ""); err != nil {
		t.Fatalf("SetDiscoveryState (ignored): %v", err)
	}
	var seenLastSeen string
	for _, rc := range repeated {
		if rc.Preview == seenBlock {
			seenLastSeen = rc.LastSeen
		}
	}
	if err := r.SetDiscoveryState(ctx, hashFor[seenBlock], DiscoverySeen, seenLastSeen); err != nil {
		t.Fatalf("SetDiscoveryState (seen): %v", err)
	}

	n, err := r.UnseenDiscoveryCount(ctx, 2)
	if err != nil {
		t.Fatalf("UnseenDiscoveryCount: %v", err)
	}
	if n != 1 {
		t.Fatalf("UnseenDiscoveryCount = %d, want 1 (only the unmarked widespread block)", n)
	}

	// Now the seen block reappears (a fresh request advances its last_ts
	// past the moment it was marked seen) -> it counts again.
	rawHash, err := decodeHash(hashFor[seenBlock])
	if err != nil {
		t.Fatalf("decodeHash: %v", err)
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE content_hash_stats SET last_ts = ? WHERE hash = ?`,
		time.Now().UTC().Add(time.Hour).Format(time.RFC3339), rawHash); err != nil {
		t.Fatalf("bump last_ts: %v", err)
	}
	n, err = r.UnseenDiscoveryCount(ctx, 2)
	if err != nil {
		t.Fatalf("UnseenDiscoveryCount after reappearance: %v", err)
	}
	if n != 2 {
		t.Errorf("UnseenDiscoveryCount after reappearance = %d, want 2 (widespread + reappeared seenBlock)", n)
	}

	// minSessions=3 excludes everything (nothing has 3 sessions).
	n, err = r.UnseenDiscoveryCount(ctx, 3)
	if err != nil {
		t.Fatalf("UnseenDiscoveryCount at min_sessions=3: %v", err)
	}
	if n != 0 {
		t.Errorf("UnseenDiscoveryCount at min_sessions=3 = %d, want 0", n)
	}
}
