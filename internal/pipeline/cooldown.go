package pipeline

import (
	"sync"
	"time"
)

// cooldownStore holds provider -> earliest time it may be tried again, after a
// 429.
//
// It is a separate type from Pipeline — and injected, like the affinity store
// and the event writer — because it must survive a config reload. A reload
// rebuilds the whole pipeline, so a cooldown map living inside it was thrown away
// on every config save: editing an unrelated setting while a provider was backing
// off immediately re-opened the flood, producing another 429 and another wait.
// The user's call: "i dont want it cleared ... resetting should be a deliberate
// decision."
//
// It is deliberately NOT persisted to the store. A cooldown is short-lived
// backoff state measured in seconds (defaultCooldown is 5s, or the upstream's own
// Retry-After), and a process that just restarted has no memory of the 429 that
// caused it — honouring a stale cooldown across a restart would be inventing
// knowledge Arbiter does not have. Surviving a reload is the requirement;
// surviving a restart is not.
type CooldownStore struct {
	mu        sync.Mutex
	providers map[string]time.Time
}

func NewCooldownStore() *CooldownStore {
	return &CooldownStore{providers: make(map[string]time.Time)}
}

// onCooldown reports whether provider is in its cooldown window, and until when.
// An entry whose time has passed is not a cooldown — it is not deleted here,
// because markCooldown's extend-never-shorten rule reads the old value, and an
// expired entry is harmless (the comparison is what decides).
func (s *CooldownStore) onCooldown(provider string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.providers[provider]
	if !ok || time.Now().After(until) {
		return time.Time{}, false
	}
	return until, true
}

// markCooldown extends a provider's cooldown to `until`, never shortening an
// existing one — a second 429 with a longer Retry-After extends the wait, a
// shorter one does not cut the current cooldown short.
func (s *CooldownStore) markCooldown(provider string, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if until.After(s.providers[provider]) {
		s.providers[provider] = until
	}
}

// ClearForAdmin is Clear exposed as a no-argument function, so main can bind it
// directly as the admin action without a wrapper closure.
func (s *CooldownStore) ClearForAdmin() int { return s.clear() }

// clear removes every cooldown, returning how many were active. This is the
// deliberate reset the user asked for: a config edit must not clear cooldowns as
// a side effect, so clearing wants its own affordance rather than happening
// incidentally.
//
// Only active cooldowns are counted and removed; expired entries are dropped too,
// since they are dead weight.
func (s *CooldownStore) clear() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	active := 0
	for provider, until := range s.providers {
		if now.Before(until) {
			active++
		}
		delete(s.providers, provider)
	}
	return active
}
