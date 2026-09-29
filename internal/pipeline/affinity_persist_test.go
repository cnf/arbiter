package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Persisted affinity pins. The behaviour these exist to prove: a pin survives the
// pipeline being rebuilt (a config reload) and the process restarting. Before
// persistence, both silently re-routed every live conversation and broke its
// prompt cache.

// fakePinner is an in-memory stand-in for the store, and it deliberately outlives
// a pipeline: a test builds a pipeline, pins through it, throws it away, and
// builds a new one over the same fake. That is exactly the reload case.
//
// Keyed by (SessionKey, PromptHash) — the same composite key the real store
// uses — so these tests exercise the real collision boundary: two prompt
// families sharing one SessionKey must land in different slots.
type fakePinner struct {
	mu    sync.Mutex
	pins  map[[2]string]AffinityPinRecord
	saves int
}

func newFakePinner() *fakePinner {
	return &fakePinner{pins: make(map[[2]string]AffinityPinRecord)}
}

func (f *fakePinner) SavePin(ctx context.Context, p AffinityPinRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pins[[2]string{p.SessionKey, p.PromptHash}] = p
	f.saves++
	return nil
}

func (f *fakePinner) LoadPin(ctx context.Context, sessionKey, promptHash string) (AffinityPinRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pins[[2]string{sessionKey, promptHash}]
	return p, ok, nil
}

func (f *fakePinner) DeletePin(ctx context.Context, sessionKey, promptHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pins, [2]string{sessionKey, promptHash})
	return nil
}

// TestPinSurvivesPipelineRebuild is the whole point. A pin recorded by one
// pipeline must be honoured by a *different* one built over the same store —
// which is what happens on every config reload.
func TestPinSurvivesPipelineRebuild(t *testing.T) {
	ctx := context.Background()
	pinner := newFakePinner()

	// First "process"/pipeline records a pin.
	before := newAffinityStore(pinner)
	before.pin(ctx, "sess-1", "ph-main", "auto", "claude", "claude-sonnet-5", time.Hour)

	// A reload throws that pipeline away and builds a fresh one — its in-memory
	// cache starts empty, so the pin can only come from the store.
	after := newAffinityStore(pinner)
	provider, model, ok := after.get(ctx, "sess-1", "ph-main", "auto")
	if !ok {
		t.Fatal("pin lost across a pipeline rebuild — this is the reload bug")
	}
	if provider != "claude" || model != "claude-sonnet-5" {
		t.Errorf("get = (%q, %q), want the pinned values", provider, model)
	}
}

// TestPinIsCachedAfterAStoreHit proves the store is consulted once, not on every
// turn: the common case is a hit on the very next turn and should not be a
// database round-trip.
func TestPinIsCachedAfterAStoreHit(t *testing.T) {
	ctx := context.Background()
	pinner := newFakePinner()

	writer := newAffinityStore(pinner)
	writer.pin(ctx, "sess-1", "ph-main", "auto", "claude", "m", time.Hour)

	reader := newAffinityStore(pinner)
	if _, _, ok := reader.get(ctx, "sess-1", "ph-main", "auto"); !ok {
		t.Fatal("expected the pin from the store")
	}

	// Drop it from the store entirely. A second get must still hit, because the
	// first one cached it — proving no further store read is needed.
	if err := pinner.DeletePin(ctx, "sess-1", "ph-main"); err != nil {
		t.Fatalf("DeletePin: %v", err)
	}
	if _, _, ok := reader.get(ctx, "sess-1", "ph-main", "auto"); !ok {
		t.Error("second get missed; the store hit was not cached")
	}
}

// TestStoredPinDoesNotApplyToADifferentRequestedModel proves the requested-model
// rule holds for a pin loaded from the store, not just one from the cache. A
// client that explicitly switched models must not be re-pinned to the old target
// by a stale row.
func TestStoredPinDoesNotApplyToADifferentRequestedModel(t *testing.T) {
	ctx := context.Background()
	pinner := newFakePinner()

	writer := newAffinityStore(pinner)
	writer.pin(ctx, "sess-1", "ph-main", "auto", "claude", "m", time.Hour)

	reader := newAffinityStore(pinner)
	if _, _, ok := reader.get(ctx, "sess-1", "ph-main", "gpt-4o"); ok {
		t.Error("a stored pin applied to a different requested model")
	}
	// And it must not have been cached under the new model either, or the next
	// turn would wrongly hit.
	if _, _, ok := reader.get(ctx, "sess-1", "ph-main", "gpt-4o"); ok {
		t.Error("a mismatched stored pin was cached and then applied")
	}
}

// TestExpiredStoredPinIsNotHonoured proves the deadline is enforced on load. A
// stale row can sit in the table for up to a sweep interval, and honouring it
// would pin a conversation long after the idle timeout was meant to release it.
func TestExpiredStoredPinIsNotHonoured(t *testing.T) {
	ctx := context.Background()
	pinner := newFakePinner()
	if err := pinner.SavePin(ctx, AffinityPinRecord{
		SessionKey:     "sess-1",
		PromptHash:     "ph-main",
		RequestedModel: "auto",
		Provider:       "claude",
		Model:          "m",
		ExpiresAt:      time.Now().Add(-time.Minute), // already expired
	}); err != nil {
		t.Fatalf("SavePin: %v", err)
	}

	s := newAffinityStore(pinner)
	if _, _, ok := s.get(ctx, "sess-1", "ph-main", "auto"); ok {
		t.Error("an expired stored pin was honoured")
	}
}

// TestNoPinnerStillWorksInMemory proves the degradation: with no store
// configured, pins live only in memory, which is the behaviour before
// persistence existed. A nil pinner must not panic.
func TestNoPinnerStillWorksInMemory(t *testing.T) {
	ctx := context.Background()
	s := newAffinityStore(nil)
	s.pin(ctx, "sess-1", "ph-main", "auto", "claude", "m", time.Hour)

	if _, _, ok := s.get(ctx, "sess-1", "ph-main", "auto"); !ok {
		t.Error("in-memory pinning stopped working without a store")
	}
	// A fresh store has nothing to load from, and must not panic doing it.
	if _, _, ok := newAffinityStore(nil).get(ctx, "sess-1", "ph-main", "auto"); ok {
		t.Error("a pin appeared without any store to hold it")
	}
}

// TestForgetClearsBothCacheAndStore proves a deliberate model switch clears the
// pin everywhere — leaving a stale row behind would resurrect it on the next
// reload.
func TestForgetClearsBothCacheAndStore(t *testing.T) {
	ctx := context.Background()
	pinner := newFakePinner()
	s := newAffinityStore(pinner)
	s.pin(ctx, "sess-1", "ph-main", "auto", "claude", "m", time.Hour)

	s.forget(ctx, "sess-1", "ph-main")

	if _, _, ok := s.get(ctx, "sess-1", "ph-main", "auto"); ok {
		t.Error("pin still hit after forget")
	}
	if _, ok, _ := pinner.LoadPin(ctx, "sess-1", "ph-main"); ok {
		t.Error("pin still in the store after forget — a reload would resurrect it")
	}
}

// TestPinIsPersistedOnEveryWrite proves a pin reaches the store, not just the
// cache. Without this the whole feature is a no-op that looks like it works.
func TestPinIsPersistedOnEveryWrite(t *testing.T) {
	ctx := context.Background()
	pinner := newFakePinner()
	s := newAffinityStore(pinner)

	s.pin(ctx, "sess-1", "ph-main", "auto", "claude", "m", time.Hour)

	if pinner.saves == 0 {
		t.Fatal("pin was never written to the store")
	}
	rec, ok, err := pinner.LoadPin(ctx, "sess-1", "ph-main")
	if err != nil || !ok {
		t.Fatalf("LoadPin = (%v, %v), want a stored pin", ok, err)
	}
	if rec.Provider != "claude" || rec.Model != "m" || rec.RequestedModel != "auto" {
		t.Errorf("stored pin = %+v, want the pinned values", rec)
	}
	if !rec.ExpiresAt.After(time.Now()) {
		t.Errorf("stored expiry %v is not in the future", rec.ExpiresAt)
	}
}

// TestDifferentPromptHashesDoNotCollide is the regression test for the actual
// bug (#69): a title-gen call and the main thread share one SessionKey (both
// carry the same X-Session-Id from Hermes) but have different system prompts,
// so they must land in different pin slots and never evict each other.
func TestDifferentPromptHashesDoNotCollide(t *testing.T) {
	ctx := context.Background()
	pinner := newFakePinner()
	s := newAffinityStore(pinner)

	s.pin(ctx, "sess-1", "ph-main", "arbiter", "claude", "claude-sonnet-5", time.Hour)
	s.pin(ctx, "sess-1", "ph-title", "arbiter", "openrouter", "free", time.Hour)

	provider, model, ok := s.get(ctx, "sess-1", "ph-main", "arbiter")
	if !ok || provider != "claude" || model != "claude-sonnet-5" {
		t.Errorf("main thread pin = (%q, %q, %v), want (claude, claude-sonnet-5, true) — the title pin evicted it", provider, model, ok)
	}

	provider, model, ok = s.get(ctx, "sess-1", "ph-title", "arbiter")
	if !ok || provider != "openrouter" || model != "free" {
		t.Errorf("title pin = (%q, %q, %v), want (openrouter, free, true)", provider, model, ok)
	}
}
