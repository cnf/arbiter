// Package logging provides the single structured logging path for Arbiter.
// Every stage of the pipeline logs through this interface — the explicit
// goal (per project scope) is one mechanism, not LiteLLM's five.
package logging

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/cnf/arbiter/pkg/types"
)

// Logger provides structured logging throughout the pipeline.
type Logger interface {
	LogRouting(ctx context.Context, route types.Route, signals types.Signals, duration time.Duration)
	LogGuardrail(ctx context.Context, guardrail string, decision string, mutation bool)
	LogUpstream(ctx context.Context, provider string, statusCode int, latency time.Duration, usage types.Usage)
	LogUpstreamCooldown(ctx context.Context, provider string, until time.Time, retryAfter time.Duration, action string)
	LogError(ctx context.Context, severity string, err error, context map[string]interface{})
	ExtractTraceID(ctx context.Context) string
	WithTraceID(ctx context.Context, traceID string) context.Context
}

// traceIDKey is the context key trace IDs are stored under. An unexported
// type prevents collisions with keys set by other packages.
type traceIDKey struct{}

// StdoutLogger logs to stdout in JSON format via log/slog.
type StdoutLogger struct {
	slog *slog.Logger
}

// NewStdoutLogger creates a stdout logger at the given level
// ("debug", "info", "warn", "error"; unrecognized values default to info).
func NewStdoutLogger(level string) *StdoutLogger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(level)})
	return &StdoutLogger{slog: slog.New(handler)}
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// withTrace prepends the trace ID (if any) as the first log attribute so
// every line from a single request can be grepped together regardless of
// which pipeline stage emitted it.
func (sl *StdoutLogger) withTrace(ctx context.Context) *slog.Logger {
	if traceID := sl.ExtractTraceID(ctx); traceID != "" {
		return sl.slog.With("trace_id", traceID)
	}
	return sl.slog
}

func (sl *StdoutLogger) LogRouting(ctx context.Context, route types.Route, signals types.Signals, duration time.Duration) {
	sl.withTrace(ctx).Info("routing_decision",
		"component", "router",
		"provider", route.Provider,
		"model", route.Model,
		"rationale", route.Rationale,
		"domain", signals.Domain,
		"effort", signals.Effort,
		"cost_class", signals.CostClass,
		"capabilities", signals.RequiredCapabilities,
		"confidence", signals.Confidence,
		"duration_ms", duration.Milliseconds(),
	)
}

func (sl *StdoutLogger) LogGuardrail(ctx context.Context, guardrail string, decision string, mutation bool) {
	sl.withTrace(ctx).Info("guardrail_applied",
		"component", "guardrail",
		"guardrail", guardrail,
		"decision", decision,
		"mutated", mutation,
	)
}

func (sl *StdoutLogger) LogUpstream(ctx context.Context, provider string, statusCode int, latency time.Duration, usage types.Usage) {
	sl.withTrace(ctx).Info("upstream_call",
		"component", "upstream",
		"provider", provider,
		"status_code", statusCode,
		"latency_ms", latency.Milliseconds(),
		"input_tokens", usage.InputTokens,
		"output_tokens", usage.OutputTokens,
		"cache_read_tokens", usage.CacheRead,
		"cache_write_tokens", usage.CacheWrite,
		"cost_usd", usage.CostUSD,
	)
}

// LogUpstreamCooldown records 429 cooldown events: either "recorded" (a 429
// with Retry-After came back, provider is cooling down until `until`) or
// "skipped" (a routing attempt was not even made because the provider is
// still in cooldown). retryAfter is 0 for skips.
func (sl *StdoutLogger) LogUpstreamCooldown(ctx context.Context, provider string, until time.Time, retryAfter time.Duration, action string) {
	sl.withTrace(ctx).Info("upstream_cooldown",
		"component", "upstream",
		"provider", provider,
		"cooldown_until", until.Format(time.RFC3339),
		"retry_after_ms", retryAfter.Milliseconds(),
		"action", action,
	)
}

func (sl *StdoutLogger) LogError(ctx context.Context, severity string, err error, context map[string]interface{}) {
	args := []interface{}{"component", "error", "error", err.Error()}
	for k, v := range context {
		args = append(args, k, v)
	}
	logger := sl.withTrace(ctx)
	switch severity {
	case "warn":
		logger.Warn("error", args...)
	case "debug":
		logger.Debug("error", args...)
	default:
		logger.Error("error", args...)
	}
}

// ExtractTraceID reads the trace ID stashed in ctx by WithTraceID, or ""
// if none was ever set.
func (sl *StdoutLogger) ExtractTraceID(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey{}).(string); ok {
		return v
	}
	return ""
}

// WithTraceID returns a derived context carrying traceID, retrievable via
// ExtractTraceID. Every log call made with the derived context (or any
// context built from it) will be tagged with this trace ID.
func (sl *StdoutLogger) WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey{}, traceID)
}
