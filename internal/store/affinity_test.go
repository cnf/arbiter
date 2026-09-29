package store

import (
	"context"
	"testing"
	"time"
)

// Persisted affinity pins, against real SQLite. The pipeline's own tests use a
// fake Pinner, so these cover the half those cannot: the SQL, the upsert, and
// the expiry rule as the store enforces it.

// TestSaveAndLoadPin proves the round trip: a saved pin loads back with the
// same fields, and its expiry survives the UTC round trip through sqlite.
func TestSaveAndLoadPin(t *testing.T) {
	r, db := newTestReader(t)
	ctx := context.Background()
	w := &SQLiteWriter{db: db}

	want := AffinityPin{
		SessionKey:     "sess-1",
		PromptHash:     "ph-main",
		RequestedModel: "auto",
		Provider:       "claude",
		Model:          "claude-sonnet-5",
		ExpiresAt:      time.Now().Add(time.Hour).UTC(),
	}
	if err := w.SavePin(ctx, want); err != nil {
		t.Fatalf("SavePin: %v", err)
	}

	got, ok, err := r.LoadPin(ctx, "sess-1", "ph-main")
	if err != nil || !ok {
		t.Fatalf("LoadPin = (%v, %v), want a hit", ok, err)
	}
	if got.RequestedModel != want.RequestedModel || got.Provider != want.Provider || got.Model != want.Model {
		t.Errorf("LoadPin = %+v, want %+v", got, want)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, want.ExpiresAt)
	}
}

// TestSavePinUpserts proves re-pinning a (session, prompt family) replaces its
// row rather than accumulating: at most one pin per (session_key, prompt_hash)
// is the documented rule, and a second row would make the load
// non-deterministic.
func TestSavePinUpserts(t *testing.T) {
	r, db := newTestReader(t)
	ctx := context.Background()
	w := &SQLiteWriter{db: db}

	first := AffinityPin{SessionKey: "s", PromptHash: "ph-main", RequestedModel: "auto", Provider: "claude", Model: "m1", ExpiresAt: time.Now().Add(time.Hour)}
	second := AffinityPin{SessionKey: "s", PromptHash: "ph-main", RequestedModel: "auto", Provider: "openai", Model: "m2", ExpiresAt: time.Now().Add(2 * time.Hour)}

	if err := w.SavePin(ctx, first); err != nil {
		t.Fatalf("SavePin first: %v", err)
	}
	if err := w.SavePin(ctx, second); err != nil {
		t.Fatalf("SavePin second: %v", err)
	}

	got, ok, err := r.LoadPin(ctx, "s", "ph-main")
	if err != nil || !ok {
		t.Fatalf("LoadPin = (%v, %v), want a hit", ok, err)
	}
	if got.Provider != "openai" || got.Model != "m2" {
		t.Errorf("LoadPin = %+v, want the second pin (an upsert, not an append)", got)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM affinity_pins`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("affinity_pins holds %d rows, want 1", n)
	}
}

// TestSavePinDoesNotCollideAcrossPromptHash is the regression test for #69: a
// title-gen call and the main thread can share one session_key (Hermes sends
// the same X-Session-Id on both) but must never share a row, because they
// have different system prompts and therefore different prompt_hash values.
func TestSavePinDoesNotCollideAcrossPromptHash(t *testing.T) {
	r, db := newTestReader(t)
	ctx := context.Background()
	w := &SQLiteWriter{db: db}

	main := AffinityPin{SessionKey: "s", PromptHash: "ph-main", RequestedModel: "arbiter", Provider: "claude", Model: "claude-sonnet-5", ExpiresAt: time.Now().Add(time.Hour)}
	title := AffinityPin{SessionKey: "s", PromptHash: "ph-title", RequestedModel: "arbiter", Provider: "openrouter", Model: "free", ExpiresAt: time.Now().Add(time.Hour)}

	if err := w.SavePin(ctx, main); err != nil {
		t.Fatalf("SavePin main: %v", err)
	}
	if err := w.SavePin(ctx, title); err != nil {
		t.Fatalf("SavePin title: %v", err)
	}

	gotMain, ok, err := r.LoadPin(ctx, "s", "ph-main")
	if err != nil || !ok || gotMain.Provider != "claude" {
		t.Errorf("LoadPin(s, ph-main) = (%+v, %v, %v), want the main pin intact — the title pin evicted it", gotMain, ok, err)
	}
	gotTitle, ok, err := r.LoadPin(ctx, "s", "ph-title")
	if err != nil || !ok || gotTitle.Provider != "openrouter" {
		t.Errorf("LoadPin(s, ph-title) = (%+v, %v, %v), want the title pin", gotTitle, ok, err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM affinity_pins`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("affinity_pins holds %d rows, want 2 (one per prompt family)", n)
	}
}

// TestLoadPinRejectsExpired proves the deadline is enforced at read time, not
// left to the sweeper — a stale row can sit in the table for up to a sweep
// interval, and honouring it would pin a conversation past its idle timeout.
func TestLoadPinRejectsExpired(t *testing.T) {
	r, db := newTestReader(t)
	ctx := context.Background()
	w := &SQLiteWriter{db: db}

	if err := w.SavePin(ctx, AffinityPin{
		SessionKey: "s", PromptHash: "ph-main", RequestedModel: "auto", Provider: "claude", Model: "m",
		ExpiresAt: time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatalf("SavePin: %v", err)
	}

	if _, ok, err := r.LoadPin(ctx, "s", "ph-main"); ok || err != nil {
		t.Errorf("LoadPin = (%v, %v), want a miss for an expired pin", ok, err)
	}
}

func TestLoadPinMissesUnknownSession(t *testing.T) {
	r, _ := newTestReader(t)
	if _, ok, err := r.LoadPin(context.Background(), "nope", "ph-main"); ok || err != nil {
		t.Errorf("LoadPin = (%v, %v), want a clean miss", ok, err)
	}
}

func TestDeletePin(t *testing.T) {
	r, db := newTestReader(t)
	ctx := context.Background()
	w := &SQLiteWriter{db: db}

	if err := w.SavePin(ctx, AffinityPin{
		SessionKey: "s", PromptHash: "ph-main", RequestedModel: "auto", Provider: "claude", Model: "m",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("SavePin: %v", err)
	}
	if err := w.DeletePin(ctx, "s", "ph-main"); err != nil {
		t.Fatalf("DeletePin: %v", err)
	}
	if _, ok, _ := r.LoadPin(ctx, "s", "ph-main"); ok {
		t.Error("pin survived DeletePin")
	}
	// Deleting an absent pin is not an error: the caller is expressing intent,
	// not asserting existence.
	if err := w.DeletePin(ctx, "absent", "ph-main"); err != nil {
		t.Errorf("DeletePin on an absent session errored: %v", err)
	}
}

// TestSweepPinsRemovesOnlyExpired proves the sweeper reclaims dead rows and
// leaves live ones alone — the latter matters more, since deleting a live pin
// would silently re-route a conversation.
func TestSweepPinsRemovesOnlyExpired(t *testing.T) {
	r, db := newTestReader(t)
	ctx := context.Background()
	w := &SQLiteWriter{db: db}

	if err := w.SavePin(ctx, AffinityPin{SessionKey: "dead", PromptHash: "ph-main", RequestedModel: "auto", Provider: "p", Model: "m", ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatalf("SavePin dead: %v", err)
	}
	if err := w.SavePin(ctx, AffinityPin{SessionKey: "live", PromptHash: "ph-main", RequestedModel: "auto", Provider: "p", Model: "m", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("SavePin live: %v", err)
	}

	n, err := r.SweepPins(ctx)
	if err != nil {
		t.Fatalf("SweepPins: %v", err)
	}
	if n != 1 {
		t.Errorf("SweepPins removed %d, want 1", n)
	}
	if _, ok, _ := r.LoadPin(ctx, "live", "ph-main"); !ok {
		t.Error("the sweep removed a live pin")
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM affinity_pins`).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 1 {
		t.Errorf("affinity_pins holds %d rows, want 1", remaining)
	}
}

// TestActiveSessionCount proves the nav bar's "N active" Sessions stat counts
// sessions still inside their cache TTL (a live affinity_pins row) and
// excludes ones whose pin has already expired — "active" means "still
// pinned for prompt-cache reuse", not "had any request ever". A session
// pinned under two prompt families (main thread + a title call) still counts
// once, not twice.
func TestActiveSessionCount(t *testing.T) {
	r, db := newTestReader(t)
	ctx := context.Background()
	w := &SQLiteWriter{db: db}

	if n, err := r.ActiveSessionCount(ctx); err != nil || n != 0 {
		t.Fatalf("ActiveSessionCount on an empty table = (%d, %v), want (0, nil)", n, err)
	}

	if err := w.SavePin(ctx, AffinityPin{SessionKey: "live-1", PromptHash: "ph-main", RequestedModel: "auto", Provider: "p", Model: "m", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("SavePin live-1: %v", err)
	}
	// Second prompt family for the SAME session — must not inflate the count.
	if err := w.SavePin(ctx, AffinityPin{SessionKey: "live-1", PromptHash: "ph-title", RequestedModel: "auto", Provider: "p", Model: "m", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("SavePin live-1/title: %v", err)
	}
	if err := w.SavePin(ctx, AffinityPin{SessionKey: "live-2", PromptHash: "ph-main", RequestedModel: "auto", Provider: "p", Model: "m", ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatalf("SavePin live-2: %v", err)
	}
	if err := w.SavePin(ctx, AffinityPin{SessionKey: "dead", PromptHash: "ph-main", RequestedModel: "auto", Provider: "p", Model: "m", ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatalf("SavePin dead: %v", err)
	}

	// Four rows in the table across three sessions, but only two sessions are
	// still within their TTL — the expired one must not count, and live-1's
	// two prompt families must count once, not twice.
	n, err := r.ActiveSessionCount(ctx)
	if err != nil {
		t.Fatalf("ActiveSessionCount: %v", err)
	}
	if n != 2 {
		t.Errorf("ActiveSessionCount = %d, want 2 (live-1 counts once despite two pins, dead must not count)", n)
	}
}

// TestSessionPinExpiry proves the Sessions lane list's data source (the "hot"
// dot and the "live only" filter) reports each session's LATEST pin expiry —
// not just a bool, and not one arbitrary row when a session holds several —
// and respects the since floor: a pin that expired before since is excluded,
// one on or after since (live, or expired but within a grace window the
// caller chose by picking since) is included with its real ExpiresAt. An
// empty table reports an empty, non-nil map rather than an error.
func TestSessionPinExpiry(t *testing.T) {
	r, db := newTestReader(t)
	ctx := context.Background()
	w := &SQLiteWriter{db: db}

	expiry, err := r.SessionPinExpiry(ctx, time.Now())
	if err != nil {
		t.Fatalf("SessionPinExpiry on an empty table: %v", err)
	}
	if expiry == nil || len(expiry) != 0 {
		t.Fatalf("SessionPinExpiry on an empty table = %v, want empty non-nil map", expiry)
	}

	liveExpiresAt := time.Now().Add(time.Hour)
	laterExpiresAt := time.Now().Add(2 * time.Hour)
	deadExpiresAt := time.Now().Add(-time.Minute)
	if err := w.SavePin(ctx, AffinityPin{SessionKey: "live-1", PromptHash: "ph-main", RequestedModel: "auto", Provider: "p", Model: "m", ExpiresAt: liveExpiresAt}); err != nil {
		t.Fatalf("SavePin live-1: %v", err)
	}
	// Second prompt family for live-1 with a LATER expiry — the session must
	// report this one, not whichever row is scanned first.
	if err := w.SavePin(ctx, AffinityPin{SessionKey: "live-1", PromptHash: "ph-title", RequestedModel: "auto", Provider: "p", Model: "m", ExpiresAt: laterExpiresAt}); err != nil {
		t.Fatalf("SavePin live-1/title: %v", err)
	}
	if err := w.SavePin(ctx, AffinityPin{SessionKey: "dead", PromptHash: "ph-main", RequestedModel: "auto", Provider: "p", Model: "m", ExpiresAt: deadExpiresAt}); err != nil {
		t.Fatalf("SavePin dead: %v", err)
	}

	// since = now: only the still-live session qualifies, and its LATEST
	// expiry (not merely a bool, not the first-scanned row) comes back.
	expiry, err = r.SessionPinExpiry(ctx, time.Now())
	if err != nil {
		t.Fatalf("SessionPinExpiry: %v", err)
	}
	if len(expiry) != 1 {
		t.Fatalf("SessionPinExpiry(now) = %v, want exactly {live-1: ...} (the expired pin must not appear)", expiry)
	}
	if got, ok := expiry["live-1"]; !ok || got.Sub(laterExpiresAt).Abs() > time.Second {
		t.Errorf("live-1 expiry = %v, want ~%v (the later of its two prompt families)", got, laterExpiresAt)
	}

	// since = an hour before now: the expired pin is now within the grace
	// window and must appear too, with its own (past) expiry.
	expiry, err = r.SessionPinExpiry(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("SessionPinExpiry(grace window): %v", err)
	}
	if len(expiry) != 2 {
		t.Fatalf("SessionPinExpiry(now-1h) = %v, want both live-1 and dead (both expired after the grace floor)", expiry)
	}
	if _, ok := expiry["dead"]; !ok {
		t.Errorf("SessionPinExpiry(now-1h) missing dead's own (past) expiry: %v", expiry)
	}
}
