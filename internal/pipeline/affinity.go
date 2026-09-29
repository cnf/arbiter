package pipeline

import (
	"context"
	"sync"
	"time"
)

// affinityPin records which provider/model last served one prompt family
// within a session, so subsequent turns in the same family reuse the same
// upstream (preserving prompt-cache reuse) instead of re-routing every
// request.
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

// affinityKey composes the in-process cache/map key for one prompt family
// within a session. sessionKey is req.SessionKey (the client's session header
// when sent, else a hash of the conversation) — this is also the grouping key
// the UI reads and must never be mangled. promptHash (PromptHash, computed by
// the caller from the client's pre-guardrail system prompt) separates the
// families that share one sessionKey: a chat session's main thread, its
// title-generation calls, and its subagent runs each have a different system
// prompt, so each gets its own slot and none can overwrite another's pin.
//
// This is an in-memory map key only — never written to a column or compared
// against requests.session_key, which stays the pure sessionKey everywhere.
// The persisted store keeps the two as separate columns (session_key,
// prompt_hash) for exactly that reason; see store.AffinityPin.
func affinityKey(sessionKey, promptHash string) string {
	return sessionKey + "\x1f" + promptHash
}

// affinityStore is a mutex-guarded cache of (session, prompt family) -> pin in
// front of an optional persistent store. get slidingly refreshes a hit's
// expiry using the TTL it was originally pinned with (idle-timeout
// semantics): a family that keeps going stays pinned, an abandoned one
// expires. At most one pin is held per (session, prompt family) — a pin
// recorded under a new requested model replaces the old one rather than
// accumulating.
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
	ttl    map[string]time.Duration // affinityKey -> the TTL it was last pinned with
	pinner Pinner
}

func newAffinityStore(p Pinner) *affinityStore {
	return &affinityStore{
		pins:   make(map[string]affinityPin),
		ttl:    make(map[string]time.Duration),
		pinner: p,
	}
}

// get looks up the pin for (sessionKey, promptHash), valid only if the client
// is still requesting requestedModel. It takes no ttl parameter: the provider
// isn't known until the pin is found, so expiry must have been precomputed at
// pin time. The TTL refreshes on a hit.
//
// A cache miss consults the store, so a pin recorded by a previous pipeline
// (before a reload) or a previous process (before a restart) is still honoured.
func (s *affinityStore) get(ctx context.Context, sessionKey, promptHash, requestedModel string) (provider, model string, ok bool) {
	key := affinityKey(sessionKey, promptHash)
	s.mu.Lock()
	pin, found := s.pins[key]
	ttl, hasTTL := s.ttl[key]
	s.mu.Unlock()

	if !found || pin.RequestedModel != requestedModel || time.Now().After(pin.ExpiresAt) {
		// Either nothing cached, or a cached pin that no longer applies. Fall
		// back to the store before giving up.
		loaded, hit, err := s.load(ctx, sessionKey, promptHash, requestedModel)
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
		s.save(ctx, sessionKey, promptHash, pin)
	}
	return pin.Provider, pin.Model, true
}

// pinned reports whether ANY pin is recorded for (sessionKey, promptHash),
// regardless of which model the client is currently requesting. It answers
// "does this session/family exist yet", which is the question
// classification-on-the-literal-path turns on — see Pipeline.classifyLiteral.
//
// Distinct from get, which answers "can this pin serve THIS request" and
// therefore returns a miss whenever the client switched models. Using get
// there would make a client that switched models look like a brand-new session
// on every single turn, and re-classify it forever.
//
// No TTL refresh: this is a read for a decision, not a use of the pin. A hit
// refreshes because the conversation is demonstrably continuing; merely asking
// whether it exists must not extend its life.
func (s *affinityStore) pinned(ctx context.Context, sessionKey, promptHash string) (affinityPin, bool) {
	if sessionKey == "" {
		return affinityPin{}, false
	}
	key := affinityKey(sessionKey, promptHash)

	s.mu.Lock()
	pin, found := s.pins[key]
	s.mu.Unlock()
	if found && !time.Now().After(pin.ExpiresAt) {
		return pin, true
	}

	// A miss consults the store: a pin recorded by a previous pipeline (before
	// a reload) or a previous process (before a restart) still proves the
	// session/family exists.
	if s.pinner == nil {
		return affinityPin{}, false
	}
	rec, ok, err := s.pinner.LoadPin(ctx, sessionKey, promptHash)
	if err != nil || !ok {
		return affinityPin{}, false
	}
	if time.Now().After(rec.ExpiresAt) {
		// Expired: drop the row so it cannot be loaded again. Deleting is safe
		// here — the pin is already dead.
		s.forget(ctx, sessionKey, promptHash)
		return affinityPin{}, false
	}
	pin = affinityPin{
		Provider:       rec.Provider,
		Model:          rec.Model,
		RequestedModel: rec.RequestedModel,
		ExpiresAt:      rec.ExpiresAt,
	}
	s.mu.Lock()
	s.pins[key] = pin
	s.mu.Unlock()
	return pin, true
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
func (s *affinityStore) load(ctx context.Context, sessionKey, promptHash, requestedModel string) (affinityPin, bool, error) {
	if s.pinner == nil {
		return affinityPin{}, false, nil
	}
	rec, ok, err := s.pinner.LoadPin(ctx, sessionKey, promptHash)
	if err != nil {
		return affinityPin{}, false, err
	}
	if !ok || rec.RequestedModel != requestedModel {
		return affinityPin{}, false, nil
	}
	if time.Now().After(rec.ExpiresAt) {
		// Expired: do not cache it, and drop the row so it cannot be loaded
		// again. Deleting is safe here — the pin is already dead.
		s.forget(ctx, sessionKey, promptHash)
		return affinityPin{}, false, nil
	}
	pin := affinityPin{
		Provider:       rec.Provider,
		Model:          rec.Model,
		RequestedModel: rec.RequestedModel,
		ExpiresAt:      rec.ExpiresAt,
	}
	s.mu.Lock()
	s.pins[affinityKey(sessionKey, promptHash)] = pin
	s.mu.Unlock()
	return pin, true, nil
}

// pin records that provider/model served (sessionKey, promptHash) while the
// client was requesting requestedModel, with expiry ttl from now, and
// persists it.
func (s *affinityStore) pin(ctx context.Context, sessionKey, promptHash, requestedModel, provider, model string, ttl time.Duration) {
	p := affinityPin{Provider: provider, Model: model, RequestedModel: requestedModel, ExpiresAt: time.Now().Add(ttl)}
	key := affinityKey(sessionKey, promptHash)
	s.mu.Lock()
	s.pins[key] = p
	s.ttl[key] = ttl
	s.mu.Unlock()
	s.save(ctx, sessionKey, promptHash, p)
}

// save writes a pin through, ignoring a persistence error after logging is not
// available here — the cache already holds it, so the conversation stays pinned
// for this process and only loses durability. Returning an error to the request
// path for that would fail a request that otherwise succeeded.
func (s *affinityStore) save(ctx context.Context, sessionKey, promptHash string, p affinityPin) {
	if s.pinner == nil {
		return
	}
	_ = s.pinner.SavePin(ctx, AffinityPinRecord{
		SessionKey:     sessionKey,
		PromptHash:     promptHash,
		RequestedModel: p.RequestedModel,
		Provider:       p.Provider,
		Model:          p.Model,
		ExpiresAt:      p.ExpiresAt,
	})
}

// forget drops one prompt family's pin, both cached and persisted. Used when
// the client explicitly switches models within that family, which means the
// pin no longer applies — other families sharing sessionKey are untouched.
func (s *affinityStore) forget(ctx context.Context, sessionKey, promptHash string) {
	key := affinityKey(sessionKey, promptHash)
	s.mu.Lock()
	delete(s.pins, key)
	delete(s.ttl, key)
	s.mu.Unlock()
	if s.pinner != nil {
		_ = s.pinner.DeletePin(ctx, sessionKey, promptHash)
	}
}
