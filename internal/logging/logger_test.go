package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/pkg/types"
)

// newCaptureLogger builds a StdoutLogger writing to a buffer instead of stdout,
// so the emitted line can be asserted. The real constructor writes to
// os.Stdout, which a test cannot read back.
func newCaptureLogger(t *testing.T, level string) (*StdoutLogger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: parseLevel(level)})
	return &StdoutLogger{slog: slog.New(handler)}, &buf
}

// parseLevel is the only place a config string becomes a level, and an
// unrecognized value silently defaulting to info is how a misconfigured
// "verbose" or "warning" turns into quieter logging than intended.
func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"", slog.LevelInfo},
		{"verbose", slog.LevelInfo},
		{"DEBUG", slog.LevelInfo}, // case-sensitive: downgrades rather than panicking
	}
	for _, tc := range cases {
		if got := parseLevel(tc.in); got != tc.want {
			t.Errorf("parseLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// The trace ID is the only way to grep one request's lines together across
// pipeline stages, and the context plumbing that carries it has two halves that
// must agree. A missing tag is invisible in production output — the log line is
// still well-formed — so it is asserted directly.
func TestTraceIDRoundTripsThroughContext(t *testing.T) {
	l, _ := newCaptureLogger(t, "debug")

	ctx := context.Background()
	if got := l.ExtractTraceID(ctx); got != "" {
		t.Errorf("ExtractTraceID(bare ctx) = %q, want empty", got)
	}

	ctx = l.WithTraceID(ctx, "trace-abc")
	if got := l.ExtractTraceID(ctx); got != "trace-abc" {
		t.Errorf("ExtractTraceID = %q, want trace-abc", got)
	}

	// A derived context inherits it, which is what makes the tag survive
	// across stage boundaries.
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	if got := l.ExtractTraceID(child); got != "trace-abc" {
		t.Errorf("ExtractTraceID(child) = %q, want the inherited trace-abc", got)
	}

	// Overwriting replaces rather than appends, so a derived request does not
	// log under its parent's trace.
	if got := l.ExtractTraceID(l.WithTraceID(ctx, "trace-def")); got != "trace-def" {
		t.Errorf("ExtractTraceID after overwrite = %q, want trace-def", got)
	}
}

// A trace ID must appear on every line once set, and must be absent — not
// present-and-empty — when there is none. An empty trace_id field would make
// "grep all lines for this request" match every untraced line.
func TestTraceIDIsTaggedOnEveryLogCall(t *testing.T) {
	route := types.Route{Provider: "claude", Model: "claude/claude-sonnet-5", Rationale: "default rule"}
	signals := types.Signals{Domain: "code", Difficulty: "high", CostClass: "medium", Confidence: 0.9}
	usage := types.Usage{InputTokens: 10, OutputTokens: 20, CacheRead: 3, CacheWrite: 4, CostUSD: 0.01}

	calls := []struct {
		name string
		log  func(l *StdoutLogger, ctx context.Context)
	}{
		{"LogRouting", func(l *StdoutLogger, ctx context.Context) {
			l.LogRouting(ctx, route, signals, 5*time.Millisecond)
		}},
		{"LogGuardrail", func(l *StdoutLogger, ctx context.Context) {
			l.LogGuardrail(ctx, "system_prompt", "applied", true)
		}},
		{"LogUpstream", func(l *StdoutLogger, ctx context.Context) {
			l.LogUpstream(ctx, "claude", 200, 12*time.Millisecond, usage)
		}},
		{"LogUpstreamCooldown", func(l *StdoutLogger, ctx context.Context) {
			l.LogUpstreamCooldown(ctx, "claude", time.Now(), 30*time.Second, "recorded")
		}},
		{"LogError", func(l *StdoutLogger, ctx context.Context) {
			l.LogError(ctx, "error", errStub("boom"), map[string]interface{}{"phase": "test"})
		}},
	}

	for _, tc := range calls {
		t.Run(tc.name+" with trace", func(t *testing.T) {
			l, buf := newCaptureLogger(t, "debug")
			ctx := l.WithTraceID(context.Background(), "trace-xyz")
			tc.log(l, ctx)
			if got := field(t, buf, "trace_id"); got != "trace-xyz" {
				t.Errorf("trace_id = %v, want trace-xyz; line: %s", got, buf.String())
			}
		})
		t.Run(tc.name+" without trace", func(t *testing.T) {
			l, buf := newCaptureLogger(t, "debug")
			tc.log(l, context.Background())
			if strings.Contains(buf.String(), "trace_id") {
				t.Errorf("trace_id present when no trace was set — grep for one request would match this line: %s", buf.String())
			}
		})
	}
}

// Router decisions are the log line a human reads to answer "why did this go
// there", so the fields that answer it must actually be emitted.
func TestLogRoutingEmitsDecisionFields(t *testing.T) {
	l, buf := newCaptureLogger(t, "info")
	route := types.Route{
		Provider:  "claude",
		Model:     "claude/claude-opus-4",
		Rationale: "domain=code matched the coding rule",
	}
	signals := types.Signals{
		Domain: "code", Difficulty: "high", CostClass: "expensive",
		RequiredCapabilities: []string{"thinking"}, Confidence: 0.75,
	}
	l.LogRouting(context.Background(), route, signals, 1500*time.Millisecond)

	rec := decode(t, buf)
	want := map[string]interface{}{
		"component":   "router",
		"msg":         "routing_decision",
		"provider":    "claude",
		"model":       "claude/claude-opus-4",
		"rationale":   "domain=code matched the coding rule",
		"domain":      "code",
		"difficulty":  "high",
		"cost_class":  "expensive",
		"confidence":  0.75,
		"duration_ms": float64(1500),
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("%s = %v, want %v", k, rec[k], v)
		}
	}
	caps, ok := rec["capabilities"].([]interface{})
	if !ok || len(caps) != 1 || caps[0] != "thinking" {
		t.Errorf("capabilities = %v, want [thinking]", rec["capabilities"])
	}
}

// A cooldown is the one log line that explains an absent upstream call, so the
// action and the retry-after must both be recorded.
func TestLogUpstreamCooldownRecordsActionAndRetryAfter(t *testing.T) {
	l, buf := newCaptureLogger(t, "info")
	until := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	l.LogUpstreamCooldown(context.Background(), "claude", until, 30*time.Second, "skipped")

	rec := decode(t, buf)
	if rec["action"] != "skipped" {
		t.Errorf("action = %v, want skipped", rec["action"])
	}
	if rec["retry_after_ms"] != float64(30000) {
		t.Errorf("retry_after_ms = %v, want 30000", rec["retry_after_ms"])
	}
	if rec["cooldown_until"] != until.Format(time.RFC3339) {
		t.Errorf("cooldown_until = %v, want %v", rec["cooldown_until"], until.Format(time.RFC3339))
	}
	if rec["provider"] != "claude" {
		t.Errorf("provider = %v, want claude", rec["provider"])
	}
}

// Severity is a string parameter, so an unknown value silently becomes an
// error-level line. That is the safe direction, and pinning it stops a future
// "info" case being added without thought.
func TestLogErrorSeverityMapping(t *testing.T) {
	cases := []struct {
		severity string
		want     slog.Level
	}{
		{"warn", slog.LevelWarn},
		{"debug", slog.LevelDebug},
		{"error", slog.LevelError},
		{"info", slog.LevelError},    // not a case: falls to default
		{"warning", slog.LevelError}, // not a case: falls to default
	}
	for _, tc := range cases {
		l, buf := newCaptureLogger(t, "debug")
		l.LogError(context.Background(), tc.severity, errStub("boom"), nil)
		if got := decode(t, buf)["level"]; got != tc.want.String() {
			t.Errorf("severity %q -> level %v, want %v", tc.severity, got, tc.want)
		}
	}
}

// Context fields are appended verbatim, and the error text is always present.
// Callers pass them as an ad-hoc map, so a dropped key loses the only
// structured detail a log line carries.
func TestLogErrorIncludesContextAndErrorText(t *testing.T) {
	l, buf := newCaptureLogger(t, "debug")
	l.LogError(context.Background(), "error", errStub("upstream refused"),
		map[string]interface{}{"phase": "admin_ui_write", "template": "sessions"})

	rec := decode(t, buf)
	if rec["error"] != "upstream refused" {
		t.Errorf("error = %v, want the error text", rec["error"])
	}
	if rec["phase"] != "admin_ui_write" || rec["template"] != "sessions" {
		t.Errorf("context fields lost: %v", rec)
	}
	if rec["component"] != "error" {
		t.Errorf("component = %v, want error", rec["component"])
	}
}

// A nil context map must not panic — LogError is called with nil in several
// places, and iterating a nil map is only safe because Go allows it.
func TestLogErrorToleratesNilContext(t *testing.T) {
	l, _ := newCaptureLogger(t, "debug")
	l.LogError(context.Background(), "error", errStub("boom"), nil)
}

// The level must actually gate: a debug log under an info logger emits nothing,
// which is what keeps the output readable in production.
func TestLevelGatesOutput(t *testing.T) {
	l, buf := newCaptureLogger(t, "info")
	l.LogError(context.Background(), "debug", errStub("quiet"), nil)
	if buf.Len() != 0 {
		t.Errorf("debug line emitted at info level: %s", buf.String())
	}

	l2, buf2 := newCaptureLogger(t, "error")
	l2.LogRouting(context.Background(), types.Route{}, types.Signals{}, 0)
	if buf2.Len() != 0 {
		t.Errorf("info line emitted at error level: %s", buf2.String())
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }

// field reads one top-level field from the single JSON line in buf.
func field(t *testing.T, buf *bytes.Buffer, key string) interface{} {
	t.Helper()
	return decode(t, buf)[key]
}

func decode(t *testing.T, buf *bytes.Buffer) map[string]interface{} {
	t.Helper()
	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("no log line was emitted")
	}
	var rec map[string]interface{}
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("log line is not JSON: %v\n%s", err, line)
	}
	return rec
}
