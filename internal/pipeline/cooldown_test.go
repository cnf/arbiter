package pipeline

import (
	"testing"
	"time"
)

// Cooldown state, injected so it survives a config reload.
//
// The user's decision: "i dont want it cleared, i have cause once if fine, but
// several times while editing the config is bad ... resetting should be a
// deliberate decision." So a reload must carry cooldowns across, and clearing is
// its own explicit action.

// TestCooldownStoreMarksAndReports is the base behaviour.
func TestCooldownStoreMarksAndReports(t *testing.T) {
	s := NewCooldownStore()
	if _, cooling := s.onCooldown("p"); cooling {
		t.Fatal("a provider reported as cooling before anything was marked")
	}

	until := time.Now().Add(time.Minute)
	s.markCooldown("p", until)

	got, cooling := s.onCooldown("p")
	if !cooling {
		t.Fatal("expected the provider to be cooling")
	}
	if !got.Equal(until) {
		t.Errorf("until = %v, want %v", got, until)
	}
}

// TestCooldownNeverShortens pins the extend-never-shorten rule: a second 429 with
// a shorter Retry-After must not cut an existing cooldown short.
func TestCooldownNeverShortens(t *testing.T) {
	s := NewCooldownStore()
	long := time.Now().Add(time.Hour)
	short := time.Now().Add(time.Minute)

	s.markCooldown("p", long)
	s.markCooldown("p", short)

	got, _ := s.onCooldown("p")
	if !got.Equal(long) {
		t.Errorf("until = %v, want the longer deadline %v — a shorter Retry-After must not shorten it", got, long)
	}
}

// TestCooldownExpiryIsNotACooldown proves an elapsed deadline stops applying
// without needing anything to delete it.
func TestCooldownExpiryIsNotACooldown(t *testing.T) {
	s := NewCooldownStore()
	s.markCooldown("p", time.Now().Add(-time.Second))
	if _, cooling := s.onCooldown("p"); cooling {
		t.Error("an expired cooldown still reported as cooling")
	}
}

// TestCooldownSurvivesPipelineRebuild is the reload case, stated directly. A
// cooldown marked by one pipeline must be visible to a *different* one built over
// the same store — which is what happens on every config save.
func TestCooldownSurvivesPipelineRebuild(t *testing.T) {
	shared := NewCooldownStore()

	// First pipeline records a cooldown.
	before := &Pipeline{cooldowns: shared}
	before.markCooldown("primary", time.Now().Add(time.Minute))

	// A reload throws it away and builds a new one over the same store.
	after := &Pipeline{cooldowns: shared}
	if _, cooling := after.onCooldown("primary"); !cooling {
		t.Fatal("cooldown lost across a pipeline rebuild — editing the config re-opens the flood")
	}
}

// TestClearCooldownsIsDeliberate proves the explicit reset works and reports what
// it cleared, so an operator gets feedback rather than silence.
func TestClearCooldownsIsDeliberate(t *testing.T) {
	s := NewCooldownStore()
	s.markCooldown("a", time.Now().Add(time.Minute))
	s.markCooldown("b", time.Now().Add(time.Minute))
	s.markCooldown("expired", time.Now().Add(-time.Minute))

	if n := s.clear(); n != 2 {
		t.Errorf("clear reported %d active cooldowns, want 2 (the expired one is not active)", n)
	}
	for _, p := range []string{"a", "b", "expired"} {
		if _, cooling := s.onCooldown(p); cooling {
			t.Errorf("provider %q still cooling after clear", p)
		}
	}
}

// TestNewPipelineGetsAWorkingCooldownStore proves a nil store does not panic —
// tests and any caller that does not care about reload survival still work.
func TestNewPipelineGetsAWorkingCooldownStore(t *testing.T) {
	p := NewPipeline(nil, nil, nil, nil, nil, &okUpstream{}, testProviders(), nil, nil, nil, fakeLogger{}, 0, nil, nil, nil, nil, nil)
	p.markCooldown("x", time.Now().Add(time.Minute))
	if _, cooling := p.onCooldown("x"); !cooling {
		t.Error("a pipeline built with no cooldown store did not record one")
	}
}
