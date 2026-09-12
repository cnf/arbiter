package logging

import (
	"context"
	"time"

	"github.com/cnf/arbiter/pkg/types"
)

// Logger provides structured logging throughout the pipeline.
type Logger interface {
	LogRouting(ctx context.Context, route types.Route, signals types.Signals, duration time.Duration)
	LogGuardrail(ctx context.Context, guardrail string, decision string, mutation bool)
	LogUpstream(ctx context.Context, provider string, statusCode int, latency time.Duration, usage types.Usage)
	LogError(ctx context.Context, severity string, err error, context map[string]interface{})
	ExtractTraceID(ctx context.Context) string
	WithTraceID(ctx context.Context, traceID string) context.Context
}

// LogEntry is a single structured log entry.
type LogEntry struct {
	Timestamp   time.Time
	TraceID     string
	RequestID   string
	Severity    string
	Component   string
	Message     string
	Details     map[string]interface{}
	Duration    time.Duration
	Attributes  map[string]interface{}
}

// StdoutLogger logs to stdout in JSON format.
type StdoutLogger struct {
	level string // "debug", "info", "warn", "error"
}

// NewStdoutLogger creates a stdout logger.
func NewStdoutLogger(level string) *StdoutLogger {
	return &StdoutLogger{level: level}
}

func (sl *StdoutLogger) LogRouting(ctx context.Context, route types.Route, signals types.Signals, duration time.Duration) {
	// TODO: implement structured logging to stdout
}

func (sl *StdoutLogger) LogGuardrail(ctx context.Context, guardrail string, decision string, mutation bool) {
	// TODO: implement structured logging
}

func (sl *StdoutLogger) LogUpstream(ctx context.Context, provider string, statusCode int, latency time.Duration, usage types.Usage) {
	// TODO: implement structured logging
}

func (sl *StdoutLogger) LogError(ctx context.Context, severity string, err error, context map[string]interface{}) {
	// TODO: implement error logging
}

func (sl *StdoutLogger) ExtractTraceID(ctx context.Context) string {
	// TODO: implement trace ID extraction from context
	return ""
}

func (sl *StdoutLogger) WithTraceID(ctx context.Context, traceID string) context.Context {
	// TODO: implement trace ID injection into context
	return ctx
}
