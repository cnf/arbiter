package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/pkg/types"

	_ "modernc.org/sqlite"
)

// schemaFS embeds the schema so creating the table needs no filesystem path.
//
//go:embed schema.sql
var schemaFS embed.FS

const (
	// eventBuffer is the queue depth between Record and the drain goroutine.
	// A full queue means the writer is not keeping up with request volume.
	eventBuffer = 256

	// recordTimeout bounds how long Record blocks on a full queue before
	// giving up and logging. It is deliberately short: Record is called on
	// the request path, and a stalled store must not stall requests.
	recordTimeout = 50 * time.Millisecond
)

// Event is one completed request, as recorded in the store. It is a flat
// value type so the pipeline can build one without holding a DB handle.
//
// Empty strings in the nullable fields (SessionKey, ClientID, AliasUsed,
// Domain, Effort, CostClass, Error) are written as SQL NULL — an absent
// value and an empty one are the same thing here.
type Event struct {
	TraceID    string
	SessionKey string
	ClientID   string

	Ts               time.Time // zero means "now", filled in by Record
	Format           string
	Provider         string
	Model            string
	AliasUsed        string
	RoutingRationale string

	Domain     string
	Effort     string
	CostClass  string
	Confidence float64

	Usage      types.Usage
	LatencyMs  int64
	StatusCode int
	Error      string
	Stream     bool
	ToolCalls  []string
}

// Writer records completed requests. Record must be non-blocking in the
// common case: it is called on the request path.
type Writer interface {
	Record(ev Event)
}

// NoopWriter discards every event. It is the default when no store is
// configured, so tests and local dev without a DB file work unchanged.
type NoopWriter struct{}

// Record implements Writer.
func (NoopWriter) Record(Event) {}

// Close satisfies the handle main uses for both writer kinds; a no-op here.
func (NoopWriter) Close() error { return nil }

// SQLiteWriter persists events to a sqlite database through a single drain
// goroutine — the same one-goroutine-owns-the-resource pattern the streaming
// path uses. Record enqueues; the goroutine executes the inserts.
type SQLiteWriter struct {
	db     *sql.DB
	q      *Queries
	logger logging.Logger

	events chan Event
	done   chan struct{}

	mu     sync.RWMutex
	closed bool
}

// NewSQLiteWriter opens (creating if needed) the database at path, applies
// the schema, and starts the drain goroutine. The caller owns the writer's
// lifetime and must Close it to flush the queue.
func NewSQLiteWriter(path string, logger logging.Logger) (*SQLiteWriter, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open event store %q: %w", path, err)
	}

	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("read embedded schema: %w", err)
	}
	if _, err := db.ExecContext(context.Background(), string(schema)); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply event store schema: %w", err)
	}

	w := &SQLiteWriter{
		db:     db,
		q:      New(db),
		logger: logger,
		events: make(chan Event, eventBuffer),
		done:   make(chan struct{}),
	}
	go w.run()
	return w, nil
}

// Record enqueues an event. If the queue is full it waits up to
// recordTimeout, then logs a warning and drops the event rather than
// blocking the request. Dropping is the last resort: the events lost are
// exactly the burst periods worth investigating, so a drop here is a signal
// to raise eventBuffer or batch inserts, not a normal outcome.
func (w *SQLiteWriter) Record(ev Event) {
	if ev.Ts.IsZero() {
		ev.Ts = time.Now().UTC()
	}

	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return
	}

	select {
	case w.events <- ev:
	case <-time.After(recordTimeout):
		w.logger.LogError(context.Background(), "warn",
			fmt.Errorf("event store queue full after %s, dropping event", recordTimeout),
			map[string]interface{}{"component": "event_store", "trace_id": ev.TraceID})
	}
}

// Close stops accepting events, drains the queue, and closes the database.
// It is safe to call more than once.
func (w *SQLiteWriter) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	close(w.events)
	w.mu.Unlock()

	<-w.done
	return w.db.Close()
}

// run drains the queue until Close closes it, inserting each event. A failed
// insert is logged and skipped — one bad row must not stop the drain.
func (w *SQLiteWriter) run() {
	defer close(w.done)
	for ev := range w.events {
		if _, err := w.q.InsertRequest(context.Background(), insertParams(ev)); err != nil {
			w.logger.LogError(context.Background(), "error", err,
				map[string]interface{}{"component": "event_store", "trace_id": ev.TraceID})
		}
	}
}

// insertParams maps an Event onto sqlc's insert parameters, converting empty
// strings to NULL for the nullable columns.
func insertParams(ev Event) InsertRequestParams {
	return InsertRequestParams{
		TraceID:          ev.TraceID,
		SessionKey:       nullStr(ev.SessionKey),
		ClientID:         nullStr(ev.ClientID),
		Ts:               ev.Ts,
		Format:           ev.Format,
		Provider:         ev.Provider,
		Model:            ev.Model,
		AliasUsed:        nullStr(ev.AliasUsed),
		RoutingRationale: ev.RoutingRationale,
		Domain:           nullStr(ev.Domain),
		Effort:           nullStr(ev.Effort),
		CostClass:        nullStr(ev.CostClass),
		Confidence:       &ev.Confidence,
		InputTokens:      int64(ev.Usage.InputTokens),
		OutputTokens:     int64(ev.Usage.OutputTokens),
		CacheReadTokens:  int64(ev.Usage.CacheRead),
		CacheWriteTokens: int64(ev.Usage.CacheWrite),
		CostUsd:          ev.Usage.CostUSD,
		LatencyMs:        ev.LatencyMs,
		StatusCode:       int64(ev.StatusCode),
		Error:            nullStr(ev.Error),
		Stream:           ev.Stream,
		ToolCallsJson:    toolCallsJSON(ev.ToolCalls),
	}
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toolCallsJSON(names []string) *string {
	if len(names) == 0 {
		return nil
	}
	b, err := json.Marshal(names)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}
