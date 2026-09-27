package errors

import (
	stderrors "errors"
	"fmt"
	"time"
)

// ArbiterError is the base error type.
type ArbiterError struct {
	Code    string
	Message string
	Cause   error
}

func (e *ArbiterError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (%v)", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *ArbiterError) Unwrap() error {
	return e.Cause
}

// RoutingError is raised when routing fails.
type RoutingError struct {
	*ArbiterError
}

// NewRoutingError creates a new routing error.
func NewRoutingError(msg string, cause error) *RoutingError {
	return &RoutingError{
		ArbiterError: &ArbiterError{
			Code:    "ROUTING_ERROR",
			Message: msg,
			Cause:   cause,
		},
	}
}

// UnknownModelError is raised when a client's requested model is neither a
// real model any configured provider declares nor a configured alias — the
// two categories REQUIREMENTS.md §1 allows in the `model` field. Unlike
// RoutingError (Arbiter's own routing/config failure), this is a client
// mistake, so it maps to 400 rather than 500.
type UnknownModelError struct {
	*ArbiterError
	StatusCode int
}

// NewUnknownModelError creates a new unknown-model error.
func NewUnknownModelError(msg string) *UnknownModelError {
	return &UnknownModelError{
		ArbiterError: &ArbiterError{
			Code:    "UNKNOWN_MODEL",
			Message: msg,
		},
		StatusCode: 400,
	}
}

// ClassificationError is raised when classification fails.
type ClassificationError struct {
	*ArbiterError
}

// NewClassificationError creates a new classification error.
func NewClassificationError(msg string, cause error) *ClassificationError {
	return &ClassificationError{
		ArbiterError: &ArbiterError{
			Code:    "CLASSIFICATION_ERROR",
			Message: msg,
			Cause:   cause,
		},
	}
}

// GuardrailError is raised when a guardrail blocks or fails.
type GuardrailError struct {
	*ArbiterError
	StatusCode int
}

// NewGuardrailError creates a new guardrail error.
func NewGuardrailError(msg string, statusCode int, cause error) *GuardrailError {
	return &GuardrailError{
		ArbiterError: &ArbiterError{
			Code:    "GUARDRAIL_ERROR",
			Message: msg,
			Cause:   cause,
		},
		StatusCode: statusCode,
	}
}

// TranslationError is raised when format conversion fails.
type TranslationError struct {
	*ArbiterError
	StatusCode int
	Phase      string // "pre_routing" or "post_routing"
}

// NewTranslationError creates a new translation error.
func NewTranslationError(phase, msg string, cause error) *TranslationError {
	statusCode := 400
	if phase == "post_routing" {
		statusCode = 500
	}
	return &TranslationError{
		ArbiterError: &ArbiterError{
			Code:    "TRANSLATION_ERROR",
			Message: msg,
			Cause:   cause,
		},
		StatusCode: statusCode,
		Phase:      phase,
	}
}

// UpstreamError is raised when upstream API fails.
type UpstreamError struct {
	*ArbiterError
	Provider   string
	StatusCode int
	// RetryAfter is the cooldown the upstream asked for via its Retry-After
	// header on a 429 (0 when absent or non-429). The pipeline records a
	// cooldown of at least this long before trying the provider again.
	RetryAfter time.Duration
}

// NewUpstreamError creates a new upstream error.
func NewUpstreamError(provider string, statusCode int, msg string, cause error) *UpstreamError {
	return &UpstreamError{
		ArbiterError: &ArbiterError{
			Code:    "UPSTREAM_ERROR",
			Message: msg,
			Cause:   cause,
		},
		Provider:   provider,
		StatusCode: statusCode,
	}
}

// StopError is raised by a router's terminal `stop` target: the operator
// wants this exact request refused cleanly, with its own status and
// message, rather than routed anywhere. It is deliberately a distinct type
// from RoutingError (a config/routing FAILURE — no rule matched, or the
// matched target is misconfigured): a stop is a decision, not a failure,
// and ChainedRouter must never treat it as "this router doesn't apply here,
// try the next one" the way it does an ordinary routing error — see
// ChainedRouter.Route.
type StopError struct {
	*ArbiterError
	StatusCode int
}

// NewStopError creates a new stop error.
func NewStopError(statusCode int, message string) *StopError {
	return &StopError{
		ArbiterError: &ArbiterError{
			Code:    "STOP",
			Message: message,
		},
		StatusCode: statusCode,
	}
}

// ConfigError is raised when config loading/validation fails.
type ConfigError struct {
	*ArbiterError
}

// NewConfigError creates a new config error.
func NewConfigError(msg string, cause error) *ConfigError {
	return &ConfigError{
		ArbiterError: &ArbiterError{
			Code:    "CONFIG_ERROR",
			Message: msg,
			Cause:   cause,
		},
	}
}

// StatusFor is the single source of truth for "what HTTP status does this
// pipeline error map to", shared by internal/http (what the client is told)
// and internal/pipeline (what gets recorded in the requests row for #5 —
// "nothing invisible"). Keeping one function means the two can never drift
// apart the way status_code=0-vs-502 already did once for upstream failures
// (see pipeline.upstreamFailureStatus's comment).
//
// GuardrailError, StopError, UnknownModelError, and UpstreamError carry their
// own status; everything else (RoutingError, ClassificationError,
// TranslationError's post_routing phase, and any error this package doesn't
// know about) is Arbiter's own bug or misconfiguration, not client error, so
// it maps to 500.
func StatusFor(err error) int {
	var guardrailErr *GuardrailError
	var stopErr *StopError
	var unknownModelErr *UnknownModelError
	var upstreamErr *UpstreamError
	var translationErr *TranslationError

	switch {
	case stderrors.As(err, &guardrailErr):
		return guardrailErr.StatusCode
	case stderrors.As(err, &stopErr):
		return stopErr.StatusCode
	case stderrors.As(err, &unknownModelErr):
		return unknownModelErr.StatusCode
	case stderrors.As(err, &upstreamErr):
		status := upstreamErr.StatusCode
		if status < 400 || status > 599 {
			return 502
		}
		return status
	case stderrors.As(err, &translationErr):
		return translationErr.StatusCode
	default:
		return 500
	}
}
