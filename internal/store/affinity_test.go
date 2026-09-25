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
		RequestedModel: "auto",
		Provider:       "claude",
		Model:          "claude-sonnet-5",
		ExpiresAt:      time.Now().Add(time.Hour).UTC(),
	}
	if err := w.SavePin(ctx, want); err != nil {
		t.Fatalf("SavePin: %v", err)
	}

	got, ok, err := r.LoadPin(ctx, "sess-1")
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

// TestSavePinUpserts proves re-pinning a session replaces its row rather than
// accumulating: at most one pin per session key is the documented rule, and a
// second row would make the load non-deterministic.
func TestSavePinUpserts(t *testing.T) {
	r, db := newTestReader(t)
	ctx := context.Background()
	w := &SQLiteWriter{db: db}

	first := AffinityPin{SessionKey: "s", RequestedModel: "auto", Provider: "claude", Model: "m1", ExpiresAt: time.Now().Add(time.Hour)}
	second := AffinityPin{SessionKey: "s", RequestedModel: "auto", Provider: "openai", Model: "m2", ExpiresAt: time.Now().Add(2 * time.Hour)}

	if err := w.SavePin(ctx, first); err != nil {
		t.Fatalf("SavePin first: %v", err)
	}
	if err := w.SavePin(ctx, second); err != nil {
		t.Fatalf("SavePin second: %v", err)
	}

	got, ok, err := r.LoadPin(ctx, "s")
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

// TestLoadPinRejectsExpired proves the deadline is enforced at read time, not
// left to the sweeper — a stale row can sit in the table for up to a sweep
// interval, and honouring it would pin a conversation past its idle timeout.
func TestLoadPinRejectsExpired(t *testing.T) {
	r, db := newTestReader(t)
	ctx := context.Background()
	w := &SQLiteWriter{db: db}

	if err := w.SavePin(ctx, AffinityPin{
		SessionKey: "s", RequestedModel: "auto", Provider: "claude", Model: "m",
		ExpiresAt: time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatalf("SavePin: %v", err)
	}

	if _, ok, err := r.LoadPin(ctx, "s"); ok || err != nil {
		t.Errorf("LoadPin = (%v, %v), want a miss for an expired pin", ok, err)
	}
}

func TestLoadPinMissesUnknownSession(t *testing.T) {
	r, _ := newTestReader(t)
	if _, ok, err := r.LoadPin(context.Background(), "nope"); ok || err != nil {
		t.Errorf("LoadPin = (%v, %v), want a clean miss", ok, err)
	}
}

func TestDeletePin(t *testing.T) {
	r, db := newTestReader(t)
	ctx := context.Background()
	w := &SQLiteWriter{db: db}

	if err := w.SavePin(ctx, AffinityPin{
		SessionKey: "s", RequestedModel: "auto", Provider: "claude", Model: "m",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("SavePin: %v", err)
	}
	if err := w.DeletePin(ctx, "s"); err != nil {
		t.Fatalf("DeletePin: %v", err)
	}
	if _, ok, _ := r.LoadPin(ctx, "s"); ok {
		t.Error("pin survived DeletePin")
	}
	// Deleting an absent pin is not an error: the caller is expressing intent,
	// not asserting existence.
	if err := w.DeletePin(ctx, "absent"); err != nil {
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

	if err := w.SavePin(ctx, AffinityPin{SessionKey: "dead", RequestedModel: "auto", Provider: "p", Model: "m", ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatalf("SavePin dead: %v", err)
	}
	if err := w.SavePin(ctx, AffinityPin{SessionKey: "live", RequestedModel: "auto", Provider: "p", Model: "m", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("SavePin live: %v", err)
	}

	n, err := r.SweepPins(ctx)
	if err != nil {
		t.Fatalf("SweepPins: %v", err)
	}
	if n != 1 {
		t.Errorf("SweepPins removed %d, want 1", n)
	}
	if _, ok, _ := r.LoadPin(ctx, "live"); !ok {
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
// pinned for prompt-cache reuse", not "had any request ever".
func TestActiveSessionCount(t *testing.T) {
	r, db := newTestReader(t)
	ctx := context.Background()
	w := &SQLiteWriter{db: db}

	if n, err := r.ActiveSessionCount(ctx); err != nil || n != 0 {
		t.Fatalf("ActiveSessionCount on an empty table = (%d, %v), want (0, nil)", n, err)
	}

	if err := w.SavePin(ctx, AffinityPin{SessionKey: "live-1", RequestedModel: "auto", Provider: "p", Model: "m", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("SavePin live-1: %v", err)
	}
	if err := w.SavePin(ctx, AffinityPin{SessionKey: "live-2", RequestedModel: "auto", Provider: "p", Model: "m", ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatalf("SavePin live-2: %v", err)
	}
	if err := w.SavePin(ctx, AffinityPin{SessionKey: "dead", RequestedModel: "auto", Provider: "p", Model: "m", ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatalf("SavePin dead: %v", err)
	}

	// Three rows in the table, but only two are still within their TTL — the
	// expired one must not count as active even though SweepPins hasn't run.
	n, err := r.ActiveSessionCount(ctx)
	if err != nil {
		t.Fatalf("ActiveSessionCount: %v", err)
	}
	if n != 2 {
		t.Errorf("ActiveSessionCount = %d, want 2 (one live pin has already expired and must not count)", n)
	}
}
