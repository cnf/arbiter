package pipeline

import (
	"context"
	"testing"
	"time"
)

func TestAffinityStorePinAndGet(t *testing.T) {
	s := newAffinityStore(nil)
	s.pin(context.Background(), "k", "ph", "auto", "claude", "claude-3-haiku-20250307", time.Minute)

	provider, model, ok := s.get(context.Background(), "k", "ph", "auto")
	if !ok || provider != "claude" || model != "claude-3-haiku-20250307" {
		t.Fatalf("get = (%q, %q, %v), want the pinned values", provider, model, ok)
	}
}

func TestAffinityStoreGetMissesUnknownKey(t *testing.T) {
	s := newAffinityStore(nil)
	if _, _, ok := s.get(context.Background(), "nope", "ph", "auto"); ok {
		t.Fatal("expected a miss for an unpinned key")
	}
}

func TestAffinityStoreMissesWhenRequestedModelChanges(t *testing.T) {
	s := newAffinityStore(nil)
	s.pin(context.Background(), "k", "ph", "auto", "claude", "m", time.Minute)

	if _, _, ok := s.get(context.Background(), "k", "ph", "gpt-4o"); ok {
		t.Fatal("a different requested model must discard the pin")
	}
	// The original requested model still hits.
	if _, _, ok := s.get(context.Background(), "k", "ph", "auto"); !ok {
		t.Fatal("the original requested model should still hit the pin")
	}
}

func TestAffinityStoreExpiresAfterTTL(t *testing.T) {
	s := newAffinityStore(nil)
	s.pin(context.Background(), "k", "ph", "auto", "claude", "m", 10*time.Millisecond)

	time.Sleep(20 * time.Millisecond)

	if _, _, ok := s.get(context.Background(), "k", "ph", "auto"); ok {
		t.Fatal("expected the pin to have expired")
	}
}

func TestAffinityStoreGetRefreshesIdleTTL(t *testing.T) {
	s := newAffinityStore(nil)
	s.pin(context.Background(), "k", "ph", "auto", "claude", "m", 40*time.Millisecond)

	// Keep the session alive across what would otherwise be its expiry.
	for i := 0; i < 3; i++ {
		time.Sleep(20 * time.Millisecond)
		if _, _, ok := s.get(context.Background(), "k", "ph", "auto"); !ok {
			t.Fatalf("hit %d: pin expired despite being reused within its TTL", i)
		}
	}

	// Stop touching it; now it should expire.
	time.Sleep(60 * time.Millisecond)
	if _, _, ok := s.get(context.Background(), "k", "ph", "auto"); ok {
		t.Fatal("expected the pin to expire once it stopped being reused")
	}
}

func TestAffinityStoreRepinReplacesTarget(t *testing.T) {
	s := newAffinityStore(nil)
	s.pin(context.Background(), "k", "ph", "auto", "claude", "m1", time.Minute)
	s.pin(context.Background(), "k", "ph", "auto", "gpt4", "m2", time.Minute)

	provider, model, ok := s.get(context.Background(), "k", "ph", "auto")
	if !ok || provider != "gpt4" || model != "m2" {
		t.Fatalf("get = (%q, %q, %v), want the replacement pin (gpt4, m2)", provider, model, ok)
	}
}

// TestAffinityStoreDifferentPromptHashAreIndependentSlots is the unit-level
// regression test for #69: two prompt families sharing one session key (the
// main thread and a title call both carry the same X-Session-Id) must not
// evict each other.
func TestAffinityStoreDifferentPromptHashAreIndependentSlots(t *testing.T) {
	s := newAffinityStore(nil)
	s.pin(context.Background(), "k", "ph-main", "arbiter", "claude", "claude-sonnet-5", time.Minute)
	s.pin(context.Background(), "k", "ph-title", "arbiter", "openrouter", "free", time.Minute)

	if provider, model, ok := s.get(context.Background(), "k", "ph-main", "arbiter"); !ok || provider != "claude" || model != "claude-sonnet-5" {
		t.Fatalf("main get = (%q, %q, %v), want (claude, claude-sonnet-5, true) — the title pin evicted it", provider, model, ok)
	}
	if provider, model, ok := s.get(context.Background(), "k", "ph-title", "arbiter"); !ok || provider != "openrouter" || model != "free" {
		t.Fatalf("title get = (%q, %q, %v), want (openrouter, free, true)", provider, model, ok)
	}
}
