package pipeline

import (
	"context"
	"testing"
	"time"
)

func TestAffinityStorePinAndGet(t *testing.T) {
	s := newAffinityStore(nil)
	s.pin(context.Background(), "k", "auto", "claude", "claude-3-haiku-20250307", time.Minute)

	provider, model, ok := s.get(context.Background(), "k", "auto")
	if !ok || provider != "claude" || model != "claude-3-haiku-20250307" {
		t.Fatalf("get = (%q, %q, %v), want the pinned values", provider, model, ok)
	}
}

func TestAffinityStoreGetMissesUnknownKey(t *testing.T) {
	s := newAffinityStore(nil)
	if _, _, ok := s.get(context.Background(), "nope", "auto"); ok {
		t.Fatal("expected a miss for an unpinned key")
	}
}

func TestAffinityStoreMissesWhenRequestedModelChanges(t *testing.T) {
	s := newAffinityStore(nil)
	s.pin(context.Background(), "k", "auto", "claude", "m", time.Minute)

	if _, _, ok := s.get(context.Background(), "k", "gpt-4o"); ok {
		t.Fatal("a different requested model must discard the pin")
	}
	// The original requested model still hits.
	if _, _, ok := s.get(context.Background(), "k", "auto"); !ok {
		t.Fatal("the original requested model should still hit the pin")
	}
}

func TestAffinityStoreExpiresAfterTTL(t *testing.T) {
	s := newAffinityStore(nil)
	s.pin(context.Background(), "k", "auto", "claude", "m", 10*time.Millisecond)

	time.Sleep(20 * time.Millisecond)

	if _, _, ok := s.get(context.Background(), "k", "auto"); ok {
		t.Fatal("expected the pin to have expired")
	}
}

func TestAffinityStoreGetRefreshesIdleTTL(t *testing.T) {
	s := newAffinityStore(nil)
	s.pin(context.Background(), "k", "auto", "claude", "m", 40*time.Millisecond)

	// Keep the session alive across what would otherwise be its expiry.
	for i := 0; i < 3; i++ {
		time.Sleep(20 * time.Millisecond)
		if _, _, ok := s.get(context.Background(), "k", "auto"); !ok {
			t.Fatalf("hit %d: pin expired despite being reused within its TTL", i)
		}
	}

	// Stop touching it; now it should expire.
	time.Sleep(60 * time.Millisecond)
	if _, _, ok := s.get(context.Background(), "k", "auto"); ok {
		t.Fatal("expected the pin to expire once it stopped being reused")
	}
}

func TestAffinityStoreRepinReplacesTarget(t *testing.T) {
	s := newAffinityStore(nil)
	s.pin(context.Background(), "k", "auto", "claude", "m1", time.Minute)
	s.pin(context.Background(), "k", "auto", "gpt4", "m2", time.Minute)

	provider, model, ok := s.get(context.Background(), "k", "auto")
	if !ok || provider != "gpt4" || model != "m2" {
		t.Fatalf("get = (%q, %q, %v), want the replacement pin (gpt4, m2)", provider, model, ok)
	}
}
