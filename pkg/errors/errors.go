package errors

import (
	"fmt"
)

// ArbiterError is the base error type.
type ArbiterError struct {
	Code       string
	Message    string
	Cause      error
	Attributes map[string]interface{}
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
			Code:       "ROUTING_ERROR",
			Message:    msg,
			Cause:      cause,
			Attributes: make(map[string]interface{}),
		},
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
			Code:       "CLASSIFICATION_ERROR",
			Message:    msg,
			Cause:      cause,
			Attributes: make(map[string]interface{}),
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
			Code:       "GUARDRAIL_ERROR",
			Message:    msg,
			Cause:      cause,
			Attributes: make(map[string]interface{}),
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
			Code:       "TRANSLATION_ERROR",
			Message:    msg,
			Cause:      cause,
			Attributes: make(map[string]interface{}),
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
	Retriable  bool
}

// NewUpstreamError creates a new upstream error.
func NewUpstreamError(provider string, statusCode int, msg string, cause error) *UpstreamError {
	retriable := statusCode >= 500 || statusCode == 429
	return &UpstreamError{
		ArbiterError: &ArbiterError{
			Code:       "UPSTREAM_ERROR",
			Message:    msg,
			Cause:      cause,
			Attributes: make(map[string]interface{}),
		},
		Provider:   provider,
		StatusCode: statusCode,
		Retriable:  retriable,
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
			Code:       "CONFIG_ERROR",
			Message:    msg,
			Cause:      cause,
			Attributes: make(map[string]interface{}),
		},
	}
}
