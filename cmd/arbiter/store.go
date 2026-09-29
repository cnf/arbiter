package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/cnf/arbiter/internal/config"
	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/store"
)

// listen opens the server's listener: a unix socket when socketPath is set
// (overriding the TCP bind/port), else TCP on bind:port. The default bind is
// loopback — Arbiter is meant to sit behind Caddy/tailscale, and a listener
// reachable from anywhere would let a peer forge the admin gate's
// forward-auth header (and reach the chat endpoints) directly.
func listen(socketPath, bind, port string) (net.Listener, error) {
	if socketPath != "" {
		// Remove a stale socket from a previous run; bind fails if the path
		// exists even when nothing is listening on it.
		if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove stale socket %s: %w", socketPath, err)
		}
		ln, err := net.Listen("unix", socketPath)
		if err != nil {
			return nil, fmt.Errorf("listen unix %s: %w", socketPath, err)
		}
		return ln, nil
	}
	addr := net.JoinHostPort(bind, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen tcp %s: %w", addr, err)
	}
	return ln, nil
}

// storeHandle is a Writer that also owns resources main must release at
// shutdown. Both concrete writers satisfy it, so main treats the enabled and
// disabled cases identically.
type storeHandle interface {
	store.Writer
	Close() error
}

// openStore returns the event-store writer named by config: a SQLiteWriter
// when storage.path is set, else a NoopWriter so nothing is persisted and the
// pipeline needs no nil checks.
func openStore(cfg *config.Config, logger logging.Logger) (storeHandle, error) {
	if cfg.Storage.Path == "" {
		slog.Info("event store disabled (storage.path unset)")
		return store.NoopWriter{}, nil
	}
	w, err := store.NewSQLiteWriter(cfg.Storage.Path, logger)
	if err != nil {
		return nil, err
	}
	slog.Info("event store enabled", "path", cfg.Storage.Path)
	if cfg.Storage.CaptureContent {
		slog.Info("content capture enabled",
			"content_ttl", cfg.Storage.ContentTTL,
			"note", "prompt and response bodies are stored; empty content_ttl means never expire")
	}
	return w, nil
}

// sweepInterval is how often expired captured content is reclaimed. Hourly is
// far more often than a TTL measured in days needs, and cheap: the sweep is two
// indexed deletes.
const sweepInterval = time.Hour

// startSweeper runs the background reclamation sweep in the background until ctx
// is done: expired content (only when a content TTL is configured), the
// content-hash rollup Discovery reads from, and expired affinity pins, which
// have their own deadlines and so are swept regardless.
// It runs once immediately so a long-idle store is reclaimed at startup rather
// than after the first interval.
//
// Only a non-zero content TTL sweeps content; with TTL unset ("never expire") a
// content sweep could only ever delete nothing. Pins changed the shape of this:
// they expire unconditionally, so the goroutine now always starts when a store
// is configured, and it is the content half that is conditional.
// Pins are swept unconditionally because their expiry is not opt-in — every pin
// carries an absolute deadline, so a row that outlives it is dead weight whether
// or not content retention is configured. Correctness does not depend on this
// running (LoadPin rejects an expired pin on its own), but without it the table
// grows forever. The content-hash rollup is unconditional for the same reason
// as pins: it does not gate on contentTTL, because it isn't about retention.
//
// One goroutine, not several: all of these are cheap, indexed operations on
// the same database, and a separate ticker per concern would only add
// contention for no benefit.
func startSweeper(ctx context.Context, path, ttlSpec string, logger logging.Logger) {
	if path == "" {
		return
	}

	// A separate reader connection from the writer's: the sweep must not
	// contend with the drain goroutine, and it is exactly the read-while-writing
	// case the shared DSN's WAL + busy_timeout exist for.
	reader, err := store.OpenReader(path)
	if err != nil {
		slog.Error("cannot start reclamation sweeper", "error", err)
		return
	}

	// A content TTL is optional; an invalid one is reported and then ignored
	// (content simply never expires) rather than preventing the pin sweep.
	var contentTTL time.Duration
	if ttlSpec != "" {
		d, err := time.ParseDuration(ttlSpec)
		switch {
		case err != nil:
			slog.Error("content_ttl is not a valid duration; content will not expire",
				"content_ttl", ttlSpec, "error", err)
		case d <= 0:
			// Zero/negative means "never expire", which is the documented way to
			// keep content forever.
		default:
			contentTTL = d
		}
	}

	go func() {
		defer func() { _ = reader.Close() }()
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			if contentTTL > 0 {
				bodies, refs, err := reader.SweepContent(ctx, contentTTL)
				if err != nil {
					if ctx.Err() == nil {
						logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "content_sweep"})
					}
				} else if refs > 0 || bodies > 0 {
					slog.Info("expired captured content", "refs", refs, "bodies", bodies)
				}
			}

			// Folds newly-arrived content_refs into content_hash_stats (see
			// schema.sql), so the Discovery page reads a running total
			// instead of aggregating content_refs from scratch on every
			// load. Runs unconditionally, like the pin sweep below: the
			// rollup is a read-side speed-up, not part of content
			// retention, so it doesn't depend on contentTTL being set.
			if folded, _, err := reader.RollupContentHashStats(ctx); err != nil {
				if ctx.Err() == nil {
					logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "content_hash_rollup"})
				}
			} else if folded > 0 {
				slog.Info("rolled up content hash stats", "requests_folded", folded)
			}

			pins, err := reader.SweepPins(ctx)
			if err != nil {
				if ctx.Err() == nil {
					logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "pin_sweep"})
				}
			} else if pins > 0 {
				slog.Info("expired session pins", "pins", pins)
			}

			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
