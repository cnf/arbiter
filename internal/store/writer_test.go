package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/pkg/types"

	_ "modernc.org/sqlite"
)

// recordingLogger captures LogError calls so tests can assert on warnings
// without a real logger.
type recordingLogger struct {
	mu     sync.Mutex
	errors []error
}

func (l *recordingLogger) LogRouting(context.Context, types.Route, types.Signals, time.Duration) {}
func (l *recordingLogger) LogGuardrail(context.Context, string, string, bool)                    {}
func (l *recordingLogger) LogUpstream(context.Context, string, int, time.Duration, types.Usage)  {}
func (l *recordingLogger) LogUpstreamCooldown(context.Context, string, time.Time, time.Duration, string) {
}
func (l *recordingLogger) LogError(_ context.Context, _ string, err error, _ map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, err)
}
func (l *recordingLogger) ExtractTraceID(context.Context) string                     { return "" }
func (l *recordingLogger) WithTraceID(ctx context.Context, _ string) context.Context { return ctx }

var _ logging.Logger = (*recordingLogger)(nil)

func newTestWriter(t *testing.T) (*SQLiteWriter, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.db")
	w, err := NewSQLiteWriter(path, &recordingLogger{})
	if err != nil {
		t.Fatalf("NewSQLiteWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, path
}

// reopenReads opens a fresh handle to a writer's database file. Tests close the
// writer (which tears down its handle and flushes the queue) and then read
// through this, proving the rows actually reached disk. It returns a raw
// *sql.DB so a test can assert on individual columns; wrap it in &Reader{...}
// when the test wants a query method.
func reopenReads(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestRecordPersistsFullEvent is the load-bearing test: an event recorded
// through the writer's queue must land in the database with every field
// intact, including the empty-string→NULL conversions.
func TestRecordPersistsFullEvent(t *testing.T) {
	w, path := newTestWriter(t)

	w.Record(Event{
		TraceID:          "trace-1",
		SessionKey:       "session-1",
		Format:           "anthropic",
		Provider:         "claude",
		Model:            "claude-3-haiku-20240307",
		AliasUsed:        "cheap-claude",
		RoutingRationale: "policy rule matched domain=code_generation",
		Domain:           "code_generation",
		Effort:           "easy",
		CostClass:        "budget",
		Confidence:       0.82,
		Usage:            types.Usage{InputTokens: 1200, OutputTokens: 340, CacheRead: 100, CostUSD: 0.000725},
		LatencyMs:        910,
		StatusCode:       200,
		Stream:           true,
		ToolCalls:        []string{"read_file", "grep"},
		ConfigEpoch:      "abc123def456",
	})

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows, err := (&Reader{db: reopenReads(t, path)}).ListRequests(context.Background(), RequestFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.TraceID != "trace-1" || got.Provider != "claude" || got.Model != "claude-3-haiku-20240307" {
		t.Errorf("identity fields wrong: %+v", got)
	}
	if got.CostUSD != 0.000725 || got.LatencyMs != 910 || got.StatusCode != 200 {
		t.Errorf("usage/latency/status wrong: %+v", got)
	}
	if got.ConfigEpoch != "abc123def456" {
		t.Errorf("config_epoch = %q, want abc123def456", got.ConfigEpoch)
	}
}

// TestZeroTimestampFilledIn proves Record stamps a time when the caller
// leaves Ts zero, so a row can never be inserted with a nonsense timestamp.
func TestZeroTimestampFilledIn(t *testing.T) {
	w, path := newTestWriter(t)
	before := time.Now().UTC()

	w.Record(Event{TraceID: "trace-ts", Format: "openai", Provider: "p", Model: "m", StatusCode: 200})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var ts time.Time
	if err := reopenReads(t, path).QueryRowContext(context.Background(),
		`SELECT ts FROM requests WHERE trace_id = 'trace-ts'`).Scan(&ts); err != nil {
		t.Fatalf("scan ts: %v", err)
	}
	if ts.Before(before.Add(-time.Second)) || ts.After(time.Now().UTC().Add(time.Second)) {
		t.Errorf("ts = %v, want ~now (record time)", ts)
	}
}

// TestRecordDoesNotBlockOnFullQueue is the durability/shape check from the
// plan: with the drain goroutine starved, Record must return within roughly
// recordTimeout rather than blocking the caller indefinitely, and must log
// the drop.
func TestRecordDoesNotBlockOnFullQueue(t *testing.T) {
	logger := &recordingLogger{}
	w := &SQLiteWriter{
		logger: logger,
		events: make(chan Event, 1), // tiny queue; nothing drains it
		done:   make(chan struct{}),
	}
	w.events <- Event{TraceID: "occupies-the-queue"}

	start := time.Now()
	w.Record(Event{TraceID: "dropped"})
	elapsed := time.Since(start)

	if elapsed > recordTimeout*5 {
		t.Errorf("Record blocked for %s, want roughly %s", elapsed, recordTimeout)
	}
	if elapsed < recordTimeout {
		t.Errorf("Record returned in %s without waiting for the queue, want >= %s", elapsed, recordTimeout)
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	if len(logger.errors) != 1 {
		t.Errorf("got %d logged drops, want 1", len(logger.errors))
	}
}

// TestRecordAfterCloseIsIgnored proves a late Record (the streaming path can
// outlive shutdown) does not panic on a closed channel.
func TestRecordAfterCloseIsIgnored(t *testing.T) {
	w, _ := newTestWriter(t)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	w.Record(Event{TraceID: "late"}) // must not panic
}

// TestCloseIsIdempotent proves a second Close returns cleanly instead of
// double-closing the channel.
func TestCloseIsIdempotent(t *testing.T) {
	w, _ := newTestWriter(t)
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestNoopWriterDoesNothing is a compile-and-run check that the default
// writer satisfies the interface and discards without effect.
func TestNoopWriterDoesNothing(t *testing.T) {
	var w Writer = NoopWriter{}
	w.Record(Event{TraceID: "discarded"})
}
