package pipeline

import (
	"context"
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

// affinityStore is a mutex-guarded cache of session key -> pin in front of an
// optional persistent store. get slidingly refreshes a hit's expiry using the
// TTL it was originally pinned with (idle-timeout semantics): a conversation
// that keeps going stays pinned, an abandoned one expires. At most one pin is
// held per session key — a pin recorded under a new requested model replaces the
// old one rather than accumulating.
//
// The cache exists because the common case is a hit on the very next turn, and
// that should not be a database round-trip. It is only a cache: a miss falls
// through to the store, which is what makes a pin survive a config reload (the
// pipeline is rebuilt, so this cache is empty) and a restart.
//
// A nil pinner means nothing is persisted — pins then live only in this map,
// which is exactly the behaviour before persistence existed and the correct
// degradation when no store is configured.
type affinityStore struct {
	mu     sync.Mutex
	pins   map[string]affinityPin
	ttl    map[string]time.Duration // key -> the TTL it was last pinned with
	pinner Pinner
}

func newAffinityStore(p Pinner) *affinityStore {
	return &affinityStore{
		pins:   make(map[string]affinityPin),
		ttl:    make(map[string]time.Duration),
		pinner: p,
	}
}

// get looks up the pin for key, valid only if the client is still requesting
// requestedModel. It takes no ttl parameter: the provider isn't known until
// the pin is found, so expiry must have been precomputed at pin time. The
// TTL refreshes on a hit.
//
// A cache miss consults the store, so a pin recorded by a previous pipeline
// (before a reload) or a previous process (before a restart) is still honoured.
func (s *affinityStore) get(ctx context.Context, key, requestedModel string) (provider, model string, ok bool) {
	s.mu.Lock()
	pin, found := s.pins[key]
	ttl, hasTTL := s.ttl[key]
	s.mu.Unlock()

	if !found || pin.RequestedModel != requestedModel || time.Now().After(pin.ExpiresAt) {
		// Either nothing cached, or a cached pin that no longer applies. Fall
		// back to the store before giving up.
		loaded, hit, err := s.load(ctx, key, requestedModel)
		if err != nil || !hit {
			return "", "", false
		}
		pin = loaded
		// A pin recovered from the store has no TTL in this process's map, so
		// the refresh below is skipped for it. That is deliberate: the stored
		// deadline is authoritative until this process pins again, and inventing
		// a TTL here would be guessing what the previous pipeline was configured
		// with.
		hasTTL = false
	}

	if hasTTL {
		pin.ExpiresAt = time.Now().Add(ttl)
		s.mu.Lock()
		s.pins[key] = pin
		s.mu.Unlock()
		s.save(ctx, key, pin)
	}
	return pin.Provider, pin.Model, true
}

// load reads a pin from the store, applying the same rules the cache does.
//
// The expiry check is repeated here rather than trusted to the store, and this is
// not redundant: the store's own LoadPin already rejects an expired row, but this
// type must not depend on that — a different Pinner implementation (or a fake in
// a test) could hand back a stale row, and caching one would pin a conversation
// long after the idle timeout was supposed to release it.
//
// The requested-model rule is enforced for the same reason: a stored pin for a
// different requested model must neither apply nor be cached, or a client that
// switched models would be re-pinned to the old target.
func (s *affinityStore) load(ctx context.Context, key, requestedModel string) (affinityPin, bool, error) {
	if s.pinner == nil {
		return affinityPin{}, false, nil
	}
	rec, ok, err := s.pinner.LoadPin(ctx, key)
	if err != nil {
		return affinityPin{}, false, err
	}
	if !ok || rec.RequestedModel != requestedModel {
		return affinityPin{}, false, nil
	}
	if time.Now().After(rec.ExpiresAt) {
		// Expired: do not cache it, and drop the row so it cannot be loaded
		// again. Deleting is safe here — the pin is already dead.
		s.forget(ctx, key)
		return affinityPin{}, false, nil
	}
	pin := affinityPin{
		Provider:       rec.Provider,
		Model:          rec.Model,
		RequestedModel: rec.RequestedModel,
		ExpiresAt:      rec.ExpiresAt,
	}
	s.mu.Lock()
	s.pins[key] = pin
	s.mu.Unlock()
	return pin, true, nil
}

// pin records that provider/model served key while the client was requesting
// requestedModel, with expiry ttl from now, and persists it.
func (s *affinityStore) pin(ctx context.Context, key, requestedModel, provider, model string, ttl time.Duration) {
	p := affinityPin{Provider: provider, Model: model, RequestedModel: requestedModel, ExpiresAt: time.Now().Add(ttl)}
	s.mu.Lock()
	s.pins[key] = p
	s.ttl[key] = ttl
	s.mu.Unlock()
	s.save(ctx, key, p)
}

// save writes a pin through, ignoring a persistence error after logging is not
// available here — the cache already holds it, so the conversation stays pinned
// for this process and only loses durability. Returning an error to the request
// path for that would fail a request that otherwise succeeded.
func (s *affinityStore) save(ctx context.Context, key string, p affinityPin) {
	if s.pinner == nil {
		return
	}
	_ = s.pinner.SavePin(ctx, AffinityPinRecord{
		SessionKey:     key,
		RequestedModel: p.RequestedModel,
		Provider:       p.Provider,
		Model:          p.Model,
		ExpiresAt:      p.ExpiresAt,
	})
}

// forget drops a session's pin, both cached and persisted. Used when the client
// explicitly switches models, which means the pin no longer applies.
func (s *affinityStore) forget(ctx context.Context, key string) {
	s.mu.Lock()
	delete(s.pins, key)
	delete(s.ttl, key)
	s.mu.Unlock()
	if s.pinner != nil {
		_ = s.pinner.DeletePin(ctx, key)
	}
}
