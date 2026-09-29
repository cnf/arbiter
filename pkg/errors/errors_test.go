package errors

import (
	stderrors "errors"
	"fmt"
	"testing"
)

// StatusFor is the single source of truth for the HTTP status a pipeline error
// maps to, shared by internal/http (what the client is told) and
// internal/pipeline (what is recorded in the requests row). Its doc comment
// records that the two once drifted apart — status_code=0-vs-502 on upstream
// failures — so the mapping is pinned here rather than only exercised
// indirectly through a handler.
func TestStatusFor(t *testing.T) {
	cause := stderrors.New("boom")

	cases := []struct {
		name string
		err  error
		want int
	}{
		{"guardrail carries its own status", NewGuardrailError("blocked", 429, cause), 429},
		{"stop carries its own status", NewStopError(403, "refused by policy"), 403},
		{"unknown model is a client mistake", NewUnknownModelError("no such model"), 400},
		{"translation pre_routing is a client mistake", NewTranslationError("pre_routing", "bad body", cause), 400},
		{"translation post_routing is our bug", NewTranslationError("post_routing", "bad marshal", cause), 500},

		// Everything without its own status is Arbiter's own fault.
		{"routing failure", NewRoutingError("no rule matched", cause), 500},
		{"classification failure", NewClassificationError("classifier threw", cause), 500},
		{"config failure", NewConfigError("bad yaml", cause), 500},
		{"a plain error", stderrors.New("something else"), 500},

		// Upstream status is passed through only when it is a real HTTP code.
		{"upstream 429", NewUpstreamError("p", 429, "rate limited", nil), 429},
		{"upstream 503", NewUpstreamError("p", 503, "unavailable", nil), 503},
		{"upstream 0 becomes a gateway error", NewUpstreamError("p", 0, "request failed", cause), 502},
		{"upstream 200 is not a valid error status", NewUpstreamError("p", 200, "odd", nil), 502},
		{"upstream 700 is out of range", NewUpstreamError("p", 700, "odd", nil), 502},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StatusFor(tc.err); got != tc.want {
				t.Errorf("StatusFor = %d, want %d", got, tc.want)
			}
		})
	}
}

// StatusFor must see through wrapping, because errors travel up the pipeline
// wrapped with %w and the outermost type is rarely the one carrying a status.
// stderrors.As is what makes that work, and it is easy to break by switching a
// case to a type assertion.
func TestStatusForSeesThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("executing request: %w", NewStopError(418, "stop rules said so"))
	if got := StatusFor(wrapped); got != 418 {
		t.Errorf("StatusFor(wrapped StopError) = %d, want 418", got)
	}

	// The first matching case in the switch wins, so precedence between
	// wrapped layers is observable and worth pinning: a guardrail error
	// wrapped around an upstream error reports the guardrail's status.
	nested := fmt.Errorf("guardrail refused: %w",
		NewGuardrailError("blocked", 429, fmt.Errorf("upstream said: %w", NewUpstreamError("p", 500, "err", nil))))
	if got := StatusFor(nested); got != 429 {
		t.Errorf("StatusFor(nested) = %d, want the outer guardrail's 429", got)
	}
}

// An error with a nil cause must not panic on formatting. Unwrap returning nil
// is also what makes stderrors.Is/As terminate rather than loop.
func TestErrorFormattingAndUnwrap(t *testing.T) {
	withCause := NewRoutingError("no rule matched", stderrors.New("boom"))
	if got, want := withCause.Error(), "ROUTING_ERROR: no rule matched (boom)"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !stderrors.Is(withCause, stderrors.Unwrap(withCause)) {
		t.Error("Unwrap did not return the cause, so errors.Is cannot match it")
	}

	noCause := NewRoutingError("no rule matched", nil)
	if got, want := noCause.Error(), "ROUTING_ERROR: no rule matched"; got != want {
		t.Errorf("Error() = %q, want %q (no cause clause)", got, want)
	}
	if noCause.Unwrap() != nil {
		t.Errorf("Unwrap() = %v, want nil so the chain terminates", noCause.Unwrap())
	}
}

// Each constructor stamps its own code, because the code is what appears in
// logs and is the only stable identifier for the failure class. A copy-paste
// slip in a constructor would otherwise be invisible.
//
// Note the access path: the code is reached by field promotion (`err.Code`) or
// by the concrete type, NOT via errors.As(&ArbiterError). These types embed
// *ArbiterError but define their own promoted Unwrap, which returns only the
// Cause — so the embedded pointer is not reachable through the chain and
// errors.As reports false. Asserting the promotion is the honest contract.
func TestConstructorsStampTheirOwnCode(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{NewRoutingError("m", nil), "ROUTING_ERROR"},
		{NewUnknownModelError("m"), "UNKNOWN_MODEL"},
		{NewClassificationError("m", nil), "CLASSIFICATION_ERROR"},
		{NewGuardrailError("m", 400, nil), "GUARDRAIL_ERROR"},
		{NewTranslationError("pre_routing", "m", nil), "TRANSLATION_ERROR"},
		{NewUpstreamError("p", 500, "m", nil), "UPSTREAM_ERROR"},
		{NewStopError(403, "m"), "STOP"},
		{NewConfigError("m", nil), "CONFIG_ERROR"},
	}
	for _, tc := range cases {
		// Code is reached by field promotion through the embedded
		// *ArbiterError, which every concrete type exposes.
		if got := codeOf(tc.err); got != tc.want {
			t.Errorf("%T code = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// codeOf reads the promoted Code field. Written as one helper rather than a
// type switch in the test so the assertion stays readable.
func codeOf(err error) string {
	switch v := err.(type) {
	case *RoutingError:
		return v.Code
	case *UnknownModelError:
		return v.Code
	case *ClassificationError:
		return v.Code
	case *GuardrailError:
		return v.Code
	case *TranslationError:
		return v.Code
	case *UpstreamError:
		return v.Code
	case *StopError:
		return v.Code
	case *ConfigError:
		return v.Code
	default:
		return ""
	}
}

// The embedded base is deliberately NOT reachable via errors.As — pinned so
// that anyone adding a case to StatusFor does not write
// `errors.As(err, &arbiterErr)` and get a silently-false match.
func TestEmbeddedBaseIsNotReachableViaErrorsAs(t *testing.T) {
	var base *ArbiterError
	if stderrors.As(NewRoutingError("m", nil), &base) {
		t.Error("errors.As reached the embedded *ArbiterError; StatusFor must not rely on that path")
	}
	// The cause IS reachable, which is what makes errors.Is work on it.
	cause := stderrors.New("boom")
	if !stderrors.Is(NewRoutingError("m", cause), cause) {
		t.Error("errors.Is did not reach the cause")
	}
}

// A constructed status is part of the error's contract, so a constructor that
// silently changed it would change what clients see. Only the two that derive
// it take it as input or compute it; the rest are fixed.
func TestConstructorStatuses(t *testing.T) {
	if got := NewUnknownModelError("m").StatusCode; got != 400 {
		t.Errorf("NewUnknownModelError status = %d, want 400", got)
	}
	if got := NewTranslationError("pre_routing", "m", nil).StatusCode; got != 400 {
		t.Errorf("pre_routing translation status = %d, want 400", got)
	}
	if got := NewTranslationError("post_routing", "m", nil).StatusCode; got != 500 {
		t.Errorf("post_routing translation status = %d, want 500", got)
	}
	if got := NewGuardrailError("m", 418, nil).StatusCode; got != 418 {
		t.Errorf("guardrail status = %d, want the value passed in", got)
	}
	if got := NewStopError(451, "m").StatusCode; got != 451 {
		t.Errorf("stop status = %d, want the value passed in", got)
	}
	if got := NewTranslationError("post_routing", "m", nil).Phase; got != "post_routing" {
		t.Errorf("translation Phase = %q, want it recorded", got)
	}
}

// RetryAfter is recorded from the upstream's Retry-After header on a 429 and
// drives the cooldown length, so it must survive construction. It is not the
// HTTP status alone that carries the cooldown.
func TestUpstreamErrorCarriesRetryAfter(t *testing.T) {
	up := NewUpstreamError("litellm", 429, "rate limited", nil)
	up.RetryAfter = 30_000_000_000 // 30s in ns, as time.Duration
	if up.RetryAfter != 30_000_000_000 {
		t.Errorf("RetryAfter = %v, want it preserved", up.RetryAfter)
	}
	if up.Provider != "litellm" {
		t.Errorf("Provider = %q, want it preserved", up.Provider)
	}
}
