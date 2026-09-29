package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Session-affinity pins, persisted so they outlive a config reload and a
// restart.
//
// Why this is in the store at all: a reload rebuilds the whole pipeline, and
// pins living inside it were lost every time the config file was saved — so
// editing an unrelated setting silently re-routed every live conversation and
// broke its prompt cache. Session pinning is what keeps a conversation coherent
// and cheap, so it has to survive both a reload and a restart.

// AffinityPin is one prompt family's pin within a session.
type AffinityPin struct {
	SessionKey     string
	PromptHash     string
	RequestedModel string
	Provider       string
	Model          string
	ExpiresAt      time.Time
}

// Pinner persists session-affinity pins. It is the narrow interface the pipeline
// depends on, so the pipeline never learns the store's wider surface and tests
// can substitute a fake — the same shape CostLatencyLookup already establishes.
//
// A nil Pinner is valid and means "not persisted": the pipeline then keeps pins
// in memory only, which is exactly today's behaviour and the correct degradation
// when no store is configured.
type Pinner interface {
	SavePin(ctx context.Context, p AffinityPin) error
	LoadPin(ctx context.Context, sessionKey, promptHash string) (AffinityPin, bool, error)
	DeletePin(ctx context.Context, sessionKey, promptHash string) error
}

// SQLiteWriter implements Pinner. Asserted at compile time because the failure
// mode is silent: if this stops being true, the pipeline's type assertion in
// main.go fails and pins quietly become in-memory only — no error, just lost
// sessions after a reload.
var _ Pinner = (*SQLiteWriter)(nil)

// Pins go through the writer's own connection rather than a second handle,
// because they are written on the request path alongside the request row and two
// handles would contend on sqlite's single writer. They are written
// synchronously (not via the event queue) because a pin must be durable before
// the next turn of the same conversation reads it, and the queue is explicitly
// allowed to drop under load — dropping a pin is not acceptable, and the write
// is a single-row upsert.
func (w *SQLiteWriter) SavePin(ctx context.Context, p AffinityPin) error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return fmt.Errorf("event store is closed")
	}

	const q = `
INSERT INTO affinity_pins (session_key, prompt_hash, requested_model, provider, model, expires_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(session_key, prompt_hash) DO UPDATE SET
    requested_model = excluded.requested_model,
    provider        = excluded.provider,
    model           = excluded.model,
    expires_at      = excluded.expires_at`
	if _, err := w.db.ExecContext(ctx, q, p.SessionKey, p.PromptHash, p.RequestedModel, p.Provider, p.Model, p.ExpiresAt.UTC()); err != nil {
		return fmt.Errorf("save affinity pin for session %q: %w", p.SessionKey, err)
	}
	return nil
}

// DeletePin removes one prompt family's pin. Used when a client explicitly
// switches models within that family, which means the pin no longer applies —
// the other families sharing this session_key are untouched.
func (w *SQLiteWriter) DeletePin(ctx context.Context, sessionKey, promptHash string) error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return fmt.Errorf("event store is closed")
	}
	if _, err := w.db.ExecContext(ctx, `DELETE FROM affinity_pins WHERE session_key = ? AND prompt_hash = ?`, sessionKey, promptHash); err != nil {
		return fmt.Errorf("delete affinity pin for session %q: %w", sessionKey, err)
	}
	return nil
}

// LoadPin reads one prompt family's pin through the writer's own connection.
//
// It lives here as well as on Reader because Pinner requires all three methods on
// one type, and the pipeline is handed the writer. Without it, no single type
// satisfied Pinner and the wiring silently degraded to in-memory pins — a failure
// with no error anywhere, which is why the end-to-end check for this feature
// asserts a row actually lands in the table.
func (w *SQLiteWriter) LoadPin(ctx context.Context, sessionKey, promptHash string) (AffinityPin, bool, error) {
	return loadPin(ctx, w.db, sessionKey, promptHash)
}

// LoadPin reads one prompt family's pin. ok=false means no usable pin: absent, or
// present but expired. The reader's copy exists so read-side callers (and tests)
// need no writer handle; both run the same SQL through loadPin.
func (r *Reader) LoadPin(ctx context.Context, sessionKey, promptHash string) (AffinityPin, bool, error) {
	return loadPin(ctx, r.db, sessionKey, promptHash)
}

// loadPin is the shared implementation. The expiry check happens here rather than
// being left to the caller, and deliberately not left to the sweeper: a stale row
// can sit in the table for up to a sweep interval, and honouring it would pin a
// conversation to a provider long after the idle timeout was supposed to release
// it.
func loadPin(ctx context.Context, db *sql.DB, sessionKey, promptHash string) (AffinityPin, bool, error) {
	const q = `
SELECT session_key, prompt_hash, requested_model, provider, model, expires_at
FROM affinity_pins
WHERE session_key = ? AND prompt_hash = ?`
	var p AffinityPin
	err := db.QueryRowContext(ctx, q, sessionKey, promptHash).Scan(
		&p.SessionKey, &p.PromptHash, &p.RequestedModel, &p.Provider, &p.Model, &p.ExpiresAt)
	if err == sql.ErrNoRows {
		return AffinityPin{}, false, nil
	}
	if err != nil {
		return AffinityPin{}, false, fmt.Errorf("load affinity pin for session %q: %w", sessionKey, err)
	}
	if time.Now().After(p.ExpiresAt) {
		return AffinityPin{}, false, nil
	}
	return p, true, nil
}

// SweepPins deletes pins whose deadline has passed, returning how many went.
// Called from the same hourly goroutine that sweeps expired content, so pin
// expiry needs no second background loop.
//
// Rows are only reclaimed here; correctness does not depend on this running,
// because LoadPin rejects an expired pin on its own.
func (r *Reader) SweepPins(ctx context.Context) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM affinity_pins WHERE expires_at < ?`, time.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("sweep affinity pins: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		// Not fatal: the delete succeeded, we just cannot report the count.
		return 0, nil
	}
	return n, nil
}
