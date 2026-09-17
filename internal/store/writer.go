package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
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

	// ActualModel is the upstream-reported model, when the upstream actually
	// told us and it differs from Model — the case a meta-router alias (e.g.
	// OpenRouter's "openrouter/auto") exists for: Model is what Arbiter asked
	// for, ActualModel is what the upstream says it used. Empty when the
	// upstream didn't report one, or reported the same thing Arbiter asked
	// for — the common case, which is why this is a separate nullable column
	// rather than replacing Model.
	ActualModel string

	// ConfigEpoch identifies the resolved config that served this request
	// (config.Config.Epoch). Empty is written as NULL.
	ConfigEpoch string

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

	// Headers is the inbound request's headers, already redacted by the
	// caller (credential-shaped values masked before this struct is built —
	// the store does not know which header names are sensitive). Nil means
	// nothing was captured.
	Headers map[string]string

	// Content is the request/response body capture (see content.go). Nil when
	// nothing was captured, so the common case costs the writer no extra work.
	// It rides on the Event deliberately: the blobs, their references and the
	// request row are then written in one transaction, which is what makes a
	// half-stored request impossible.
	Content *CapturedContent

	// RejectedID is non-zero for a request that never produced a requests row
	// (see ContentRecorder). The drain path then stores Content under
	// owner_kind="rejected" and skips the request insert entirely.
	RejectedID int64
}

// Writer records completed requests. Record must be non-blocking in the
// common case: it is called on the request path.
type Writer interface {
	Record(ev Event)
}

// ContentRecorder records content for a request that will never get a requests
// row — a normalize failure, a pre-guardrail rejection, a routing failure.
// Those requests are the ones worth asking "why was this rejected?" about, and
// they would otherwise have no stored content at all.
//
// rejectID is caller-chosen and unique per rejection; it is what a later
// promotion to a real request row would key on (see content_refs' owner_kind).
type ContentRecorder interface {
	RecordRejected(rejectID int64, content CapturedContent)
}

// NoopWriter discards every event. It is the default when no store is
// configured, so tests and local dev without a DB file work unchanged.
type NoopWriter struct{}

// Record implements Writer.
func (NoopWriter) Record(Event) {}

// RecordRejected implements ContentRecorder.
func (NoopWriter) RecordRejected(int64, CapturedContent) {}

// Close satisfies the handle main uses for both writer kinds; a no-op here.
func (NoopWriter) Close() error { return nil }

// SQLiteWriter persists events to a sqlite database through a single drain
// goroutine — the same one-goroutine-owns-the-resource pattern the streaming
// path uses. Record enqueues; the goroutine executes the inserts.
type SQLiteWriter struct {
	db     *sql.DB
	logger logging.Logger

	events chan Event
	done   chan struct{}

	mu     sync.RWMutex
	closed bool
}

// dsn builds the connection string for the event store. Both the writer and
// the reader must go through it: WAL lets a reader query while the writer's
// drain goroutine is mid-transaction, and busy_timeout makes the two wait for
// each other instead of failing instantly with SQLITE_BUSY. journal_mode is
// persisted in the database file, but busy_timeout is per-connection, so the
// reader cannot inherit it from the writer.
func dsn(path string) string {
	return "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
}

// NewSQLiteWriter opens (creating if needed) the database at path, applies
// the schema, and starts the drain goroutine. The caller owns the writer's
// lifetime and must Close it to flush the queue.
func NewSQLiteWriter(path string, logger logging.Logger) (*SQLiteWriter, error) {
	db, err := sql.Open("sqlite", dsn(path))
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
	// CREATE TABLE IF NOT EXISTS is a no-op on an existing table, so a column
	// added to schema.sql after a database was first created never appears on
	// it. Add the ones we know about explicitly; an insert referencing a
	// missing column fails every time, which would silently lose events.
	for _, col := range []string{"config_epoch TEXT", "headers_json TEXT", "actual_model TEXT"} {
		if err := addColumnIfMissing(db, "requests", col); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("migrate event store schema: %w", err)
		}
	}

	w := &SQLiteWriter{
		db:     db,
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

// RecordRejected implements ContentRecorder: content for a request that never
// becomes a requests row. It reuses the same queue and drain goroutine as
// Record, so there is one writer and one ordering, and it does not block the
// caller for the same reason Record doesn't.
func (w *SQLiteWriter) RecordRejected(rejectID int64, content CapturedContent) {
	w.Record(Event{
		Ts:      time.Now().UTC(),
		Content: &content,
		// TraceID is empty and no request fields are set: the drain path sees
		// RejectedID and writes content references only.
		RejectedID: rejectID,
	})
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

// addColumnIfMissing runs ALTER TABLE ... ADD COLUMN unless the column
// already exists. SQLite has no ADD COLUMN IF NOT EXISTS, so the check is a
// PRAGMA read. decl is the full column declaration, e.g. "config_epoch TEXT".
func addColumnIfMissing(db *sql.DB, table, decl string) error {
	name := strings.SplitN(decl, " ", 2)[0]

	rows, err := db.QueryContext(context.Background(), "PRAGMA table_info("+table+")")
	if err != nil {
		return fmt.Errorf("read %s columns: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			cid       int
			colName   string
			colType   string
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("scan %s columns: %w", table, err)
		}
		if colName == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read %s columns: %w", table, err)
	}

	if _, err := db.ExecContext(context.Background(),
		"ALTER TABLE "+table+" ADD COLUMN "+decl); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, name, err)
	}
	return nil
}

// run drains the queue until Close closes it, inserting each event. A failed
// insert is logged and skipped — one bad row must not stop the drain.
func (w *SQLiteWriter) run() {
	defer close(w.done)
	for ev := range w.events {
		if err := writeEvent(context.Background(), w.db, ev); err != nil {
			w.logger.LogError(context.Background(), "error", err,
				map[string]interface{}{"component": "event_store", "trace_id": ev.TraceID})
		}
	}
}

// writeEvent stores one event and, when present, its captured content — all in
// a single transaction, so a request can never be half-stored (row without
// bodies, or bodies without the row that references them).
//
// A RejectedID event is the same transaction minus the request insert: content
// for a request Arbiter refused, which has no requests row to hang off.
func writeEvent(ctx context.Context, db *sql.DB, ev Event) error {
	if ev.RejectedID == 0 && (ev.Content == nil || ev.Content.Empty()) {
		// The common path: metadata only, no transaction needed.
		return insertRequest(ctx, db, ev)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin event transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if ev.RejectedID != 0 {
		// Reference rows point at a caller-supplied id, not a rowid, because
		// there is no row. Promoting a rejection to a real request later is an
		// INSERT plus an UPDATE of owner_kind/owner_id — no rewrite of the
		// reference queries.
		if ev.Content != nil && !ev.Content.Empty() {
			if err := writeContent(ctx, tx, "rejected", ev.RejectedID, *ev.Content); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit rejected content: %w", err)
		}
		return nil
	}

	id, err := insertRequestTx(ctx, tx, ev)
	if err != nil {
		return err
	}
	if ev.Content != nil && !ev.Content.Empty() {
		if err := writeContent(ctx, tx, "request", id, *ev.Content); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit event: %w", err)
	}
	return nil
}

// insertRequest writes one event as a row outside any transaction. It is the
// no-content fast path; insertRequestTx is the one that returns the row id the
// content references need.
func insertRequest(ctx context.Context, db *sql.DB, ev Event) error {
	_, err := insertRequestTx(ctx, db, ev)
	return err
}

// execer is satisfied by both *sql.DB and *sql.Tx, so the insert runs either
// standalone or inside writeEvent's transaction without a second copy of the
// statement.
type execer interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
}

// insertRequestTx writes one event's row and returns its id. Hand-written
// rather than generated: sqlc was retired (see README's "Event store" section)
// after the read side turned out to need queries its sqlite parser cannot
// express, leaving one generated function in use and two generator defects to
// work around.
//
// Empty strings in the nullable columns are written as NULL — an absent value
// and an empty one mean the same thing here, and the nullable-column readers
// flatten NULL back to "".
func insertRequestTx(ctx context.Context, db execer, ev Event) (int64, error) {
	const q = `
INSERT INTO requests (
    trace_id, session_key, client_id, ts, format, provider, model, actual_model,
    alias_used, routing_rationale, domain, effort, cost_class, confidence,
    input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
    cost_usd, latency_ms, status_code, error, stream, tool_calls_json,
    config_epoch, headers_json
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?, ?, ?, ?, ?,
    ?
)`

	res, err := db.ExecContext(ctx, q,
		ev.TraceID,
		nullStr(ev.SessionKey),
		nullStr(ev.ClientID),
		ev.Ts,
		ev.Format,
		ev.Provider,
		ev.Model,
		nullStr(ev.ActualModel),
		nullStr(ev.AliasUsed),
		ev.RoutingRationale,
		nullStr(ev.Domain),
		nullStr(ev.Effort),
		nullStr(ev.CostClass),
		ev.Confidence,
		int64(ev.Usage.InputTokens),
		int64(ev.Usage.OutputTokens),
		int64(ev.Usage.CacheRead),
		int64(ev.Usage.CacheWrite),
		ev.Usage.CostUSD,
		ev.LatencyMs,
		int64(ev.StatusCode),
		nullStr(ev.Error),
		ev.Stream,
		toolCallsJSON(ev.ToolCalls),
		nullStr(ev.ConfigEpoch),
		headersJSON(ev.Headers))
	if err != nil {
		return 0, fmt.Errorf("insert request: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert request: read id: %w", err)
	}
	return id, nil
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

func headersJSON(h map[string]string) *string {
	if len(h) == 0 {
		return nil
	}
	b, err := json.Marshal(h)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}
