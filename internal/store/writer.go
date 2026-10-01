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
// Domain, Difficulty, CostClass, Error) are written as SQL NULL — an absent
// value and an empty one are the same thing here.
type Event struct {
	TraceID    string
	SessionKey string
	ClientID   string

	Ts time.Time // zero means "now", filled in by Record
	// ArrivalTs is when the request reached Execute — the request's own
	// start, not when this row got written. Zero means unknown (a call
	// site that hasn't been updated to set it, or a non-client kind that
	// has no meaningful arrival distinct from Ts). See schema.sql's comment
	// on requests.arrival_ts and issue #8 for why this exists.
	ArrivalTs        time.Time
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
	Difficulty string
	CostClass  string
	Confidence float64
	// RequiredCapabilities is nil when classification is unavailable and an
	// empty non-nil slice when it ran but detected none.
	RequiredCapabilities []string

	// RequestKind is what the request IS ("title", later "subagent"), as
	// opposed to who sent it — that is Kind below. See
	// types.Signals.RequestKind for why the two are not the same column.
	RequestKind string

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

	// Kind distinguishes real client traffic ("client", the default when
	// empty) from Arbiter's own internal requests ("classifier", and later
	// "title_gen"/"subagent"). Every kind gets a full row — fully visible for
	// debugging on the request detail and session trajectory views — but the
	// default request-list view and the cost/latency aggregates filter to
	// "client" so a classifier call's own spend doesn't skew them.
	Kind string
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
	for _, col := range []string{"config_epoch TEXT", "headers_json TEXT", "actual_model TEXT", "kind TEXT NOT NULL DEFAULT 'client'", "request_kind TEXT", "arrival_ts TIMESTAMP", "required_capabilities_json TEXT"} {
		if err := addColumnIfMissing(db, "requests", col); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("migrate event store schema: %w", err)
		}
	}
	// name (#22): an attachment's filename, added to content_refs after the
	// table already existed in deployed databases.
	if err := addColumnIfMissing(db, "content_refs", "name TEXT"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate event store schema: %w", err)
	}
	// requests.effort was renamed to requests.difficulty (#79) — the routing
	// axis was renamed so the bare word "effort" can mean the client-facing
	// reasoning-effort knob. CREATE TABLE IF NOT EXISTS above is a no-op on an
	// existing table, so a deployed database still has the old column name and
	// every read/write referencing `difficulty` would fail. Guarded, because a
	// fresh database already has `difficulty` straight from schema.sql.
	if err := renameColumnIfPresent(db, "requests", "effort", "difficulty"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate event store schema: %w", err)
	}
	// affinity_pins predates prompt_hash and its composite primary key (see
	// schema.sql): a database created before that change has session_key as
	// its sole PRIMARY KEY, and CREATE TABLE IF NOT EXISTS above is a no-op
	// against it — an ADD COLUMN alone cannot widen a primary key, so this
	// needs a real rebuild.
	if err := migrateAffinityPinsPromptHash(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate event store schema: %w", err)
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
	// ArrivalTs is always set explicitly on every real write path (see
	// pipeline/record.go, execute.go, streaming.go) — this fallback exists
	// so the many test fixtures that only ever set Ts don't silently write a
	// NULL arrival_ts that sorts before every real row and breaks every
	// arrival-ordered read. A NULL here in production would mean a caller
	// forgot to stamp arrival, which is worth treating as "arrived when it
	// finished" rather than "arrived at the beginning of time".
	if ev.ArrivalTs.IsZero() {
		ev.ArrivalTs = ev.Ts
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

// renameColumnIfPresent renames a column only when `from` still exists and
// `to` does not. SQLite's ALTER TABLE ... RENAME COLUMN has no IF EXISTS
// form, so the check is a PRAGMA read (same shape as addColumnIfMissing).
//
// Both directions are guarded: a fresh database (created from the current
// schema.sql) already has `to`, and a database already migrated has neither
// `from`. The `to` check also makes the helper idempotent and safe to re-run.
func renameColumnIfPresent(db *sql.DB, table, from, to string) error {
	rows, err := db.QueryContext(context.Background(), "PRAGMA table_info("+table+")")
	if err != nil {
		return fmt.Errorf("read %s columns: %w", table, err)
	}
	hasFrom, hasTo := false, false
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
			_ = rows.Close()
			return fmt.Errorf("scan %s columns: %w", table, err)
		}
		switch colName {
		case from:
			hasFrom = true
		case to:
			hasTo = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read %s columns: %w", table, err)
	}
	_ = rows.Close()

	if !hasFrom || hasTo {
		return nil
	}
	if _, err := db.ExecContext(context.Background(),
		"ALTER TABLE "+table+" RENAME COLUMN "+from+" TO "+to); err != nil {
		return fmt.Errorf("rename %s.%s to %s: %w", table, from, to, err)
	}
	return nil
}

// migrateAffinityPinsPromptHash rebuilds affinity_pins onto the composite
// (session_key, prompt_hash) primary key when an older database still has
// session_key alone as its PRIMARY KEY. A no-op on a fresh database (the
// embedded schema.sql already created the new shape) and on one already
// migrated.
//
// This cannot be addColumnIfMissing: SQLite has no ALTER TABLE to widen a
// PRIMARY KEY, so an existing table must be rebuilt — create the new shape,
// copy every row across with prompt_hash defaulted to ” (every pin recorded
// before this migration existed for the whole session, which is exactly what
// an empty prompt_hash means going forward), drop the old table, rename the
// new one into place. Losing a live pin here is not a correctness risk: the
// next request for that session just re-routes once and re-pins, the same
// outcome as an idle-timeout expiry.
func migrateAffinityPinsPromptHash(db *sql.DB) error {
	rows, err := db.QueryContext(context.Background(), "PRAGMA table_info(affinity_pins)")
	if err != nil {
		return fmt.Errorf("read affinity_pins columns: %w", err)
	}
	hasPromptHash := false
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
			_ = rows.Close()
			return fmt.Errorf("scan affinity_pins columns: %w", err)
		}
		if colName == "prompt_hash" {
			hasPromptHash = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read affinity_pins columns: %w", err)
	}
	_ = rows.Close()
	if hasPromptHash {
		return nil
	}

	const rebuild = `
CREATE TABLE affinity_pins_new (
    session_key     TEXT NOT NULL,
    prompt_hash     TEXT NOT NULL DEFAULT '',
    requested_model TEXT NOT NULL,
    provider        TEXT NOT NULL,
    model           TEXT NOT NULL,
    expires_at      TIMESTAMP NOT NULL,
    PRIMARY KEY (session_key, prompt_hash)
);
INSERT INTO affinity_pins_new (session_key, prompt_hash, requested_model, provider, model, expires_at)
    SELECT session_key, '', requested_model, provider, model, expires_at FROM affinity_pins;
DROP TABLE affinity_pins;
ALTER TABLE affinity_pins_new RENAME TO affinity_pins;
CREATE INDEX IF NOT EXISTS idx_affinity_expires ON affinity_pins(expires_at);`
	if _, err := db.ExecContext(context.Background(), rebuild); err != nil {
		return fmt.Errorf("rebuild affinity_pins with prompt_hash: %w", err)
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
func writeEvent(ctx context.Context, db *sql.DB, ev Event) error {
	if ev.Content == nil || ev.Content.Empty() {
		// The common path: metadata only, no transaction needed.
		return insertRequest(ctx, db, ev)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin event transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

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
    trace_id, session_key, client_id, ts, arrival_ts, format, provider, model, actual_model,
    alias_used, routing_rationale, domain, difficulty, cost_class, confidence, required_capabilities_json,
    input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
    cost_usd, latency_ms, status_code, error, stream, tool_calls_json,
    config_epoch, headers_json, kind, request_kind
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?
)`

	// kind is NOT NULL with a schema default, but this INSERT always binds it
	// explicitly, so SQLite's column default never kicks in (that only
	// applies when a column is omitted from the statement entirely) — a
	// direct Writer.Record call that bypasses Pipeline.record's own
	// defaulting would otherwise insert an empty string instead of "client".
	kind := ev.Kind
	if kind == "" {
		kind = "client"
	}

	res, err := db.ExecContext(ctx, q,
		ev.TraceID,
		nullStr(ev.SessionKey),
		nullStr(ev.ClientID),
		ev.Ts,
		nullTime(ev.ArrivalTs),
		ev.Format,
		ev.Provider,
		ev.Model,
		nullStr(ev.ActualModel),
		nullStr(ev.AliasUsed),
		ev.RoutingRationale,
		nullStr(ev.Domain),
		nullStr(ev.Difficulty),
		nullStr(ev.CostClass),
		ev.Confidence,
		jsonList(ev.RequiredCapabilities),
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
		headersJSON(ev.Headers),
		kind,
		nullStr(ev.RequestKind))
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

// nullTime returns nil for the zero time.Time (arrival not set by this call
// site) so it is written as SQL NULL rather than 0001-01-01, matching how
// nullStr treats "" as absent rather than a real empty string.
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func jsonList(values []string) *string {
	if values == nil {
		return nil
	}
	b, err := json.Marshal(values)
	if err != nil {
		return nil
	}
	s := string(b)
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
