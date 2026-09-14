package pipeline

import (
	"sync"
	"time"
)

// affinityPin records which provider/model last served a session, so
// subsequent turns in the same conversation land on the same upstream
// (preserving prompt-cache reuse) instead of re-routing every request.
//
// RequestedModel is the client's `model` value at pin time. A pin only
// applies while the client keeps asking for that same model: a client that
// explicitly switches models means it, and the pin is discarded in favor of
// fresh routing.
type affinityPin struct {
	Provider       string
	Model          string
	RequestedModel string
	ExpiresAt      time.Time
}

// affinityStore is an in-memory, mutex-guarded map of session key -> pin,
// mirroring the shape of Pipeline.cooldowns. get slidingly refreshes a hit's
// expiry using the TTL it was originally pinned with (idle-timeout
// semantics): a conversation that keeps going stays pinned, an abandoned one
// expires. At most one pin is held per session key — a pin recorded under a
// new requested model replaces the old one rather than accumulating.
type affinityStore struct {
	mu   sync.Mutex
	pins map[string]affinityPin
	ttl  map[string]time.Duration // key -> the TTL it was last pinned with
}

func newAffinityStore() *affinityStore {
	return &affinityStore{
		pins: make(map[string]affinityPin),
		ttl:  make(map[string]time.Duration),
	}
}

// get looks up the pin for key, valid only if the client is still requesting
// requestedModel. It takes no ttl parameter: the provider isn't known until
// the pin is found, so expiry must have been precomputed at pin time. The
// TTL refreshes on a hit.
func (s *affinityStore) get(key, requestedModel string) (provider, model string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	pin, found := s.pins[key]
	if !found || pin.RequestedModel != requestedModel || time.Now().After(pin.ExpiresAt) {
		return "", "", false
	}

	if ttl, ok := s.ttl[key]; ok {
		pin.ExpiresAt = time.Now().Add(ttl)
		s.pins[key] = pin
	}
	return pin.Provider, pin.Model, true
}

// pin records that provider/model served key while the client was requesting
// requestedModel, with expiry ttl from now.
func (s *affinityStore) pin(key, requestedModel, provider, model string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pins[key] = affinityPin{Provider: provider, Model: model, RequestedModel: requestedModel, ExpiresAt: time.Now().Add(ttl)}
	s.ttl[key] = ttl
}
