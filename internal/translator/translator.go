// Package translator converts between wire formats (Anthropic /v1/messages,
// OpenAI /chat/completions) and the canonical NormalizedRequest/Response
// types. Conversion is hub-and-spoke: every wire format only ever converts
// to/from Normalized, never directly to another wire format. That keeps the
// translation surface at 2*N instead of N^2 as more formats are added, and
// it's the only translation Arbiter actually needs today (Omniroute,
// OpenRouter and OpenCode Zen are all OpenAI-compatible upstream).
package translator

import (
	"encoding/json"
	"fmt"

	"github.com/cnf/arbiter/pkg/types"
)

// Translator converts NormalizedRequest/Response to/from a specific
// upstream wire format. One implementation (DefaultTranslator) handles both
// directions for both formats — there's no need for a translator-per-format
// since the logic is small and shares helpers.
type Translator interface {
	NormalizedToAnthropicRequest(req *types.NormalizedRequest) (*types.AnthropicRequest, error)
	NormalizedToOpenAIRequest(req *types.NormalizedRequest) (*types.OpenAIRequest, error)
	AnthropicResponseToNormalized(resp *types.AnthropicResponse) (*types.NormalizedResponse, error)
	OpenAIResponseToNormalized(resp *types.OpenAIResponse) (*types.NormalizedResponse, error)
}

// Normalizer detects the wire format of an inbound request and converts it
// to a NormalizedRequest.
type Normalizer interface {
	// Detect sniffs the wire format from a raw JSON payload. Callers that
	// already know the format (e.g. because the request hit /v1/messages)
	// should skip this and call ToNormalized directly.
	Detect(payload []byte) (string, error) // returns "anthropic" or "openai"
	ToNormalized(payload []byte, format string) (*types.NormalizedRequest, error)
}

// Denormalizer converts a NormalizedResponse back into whatever wire format
// the original client spoke, so the response shape matches the request shape.
type Denormalizer interface {
	FromNormalized(resp *types.NormalizedResponse, originalFormat string) (interface{}, error)
}

// DefaultTranslator implements Translator, Normalizer and Denormalizer.
// A single implementation is enough — there's no per-format variation that
// justifies splitting it up, and the pipeline wires the same instance into
// all three roles.
type DefaultTranslator struct{}

// NewDefaultTranslator creates a translator.
func NewDefaultTranslator() *DefaultTranslator {
	return &DefaultTranslator{}
}

// Detect sniffs Anthropic vs OpenAI from the raw JSON. Anthropic requests
// carry the system prompt as a top-level "system" field; OpenAI folds it
// into a role:"system" message instead. That's the cheapest reliable signal
// without fully unmarshalling. Callers that already know the format (the
// normal case — Arbiter exposes distinct /v1/messages and /chat/completions
// endpoints) should skip Detect entirely.
func (dt *DefaultTranslator) Detect(payload []byte) (string, error) {
	var probe struct {
		System json.RawMessage `json:"system"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return "", fmt.Errorf("translator: detect format: %w", err)
	}
	if probe.System != nil {
		return "anthropic", nil
	}
	return "openai", nil
}

// ToNormalized parses a raw payload of the given format into a NormalizedRequest.
func (dt *DefaultTranslator) ToNormalized(payload []byte, format string) (*types.NormalizedRequest, error) {
	switch format {
	case "anthropic":
		var req types.AnthropicRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("translator: parse anthropic request: %w", err)
		}
		norm := anthropicRequestToNormalized(&req)
		norm.OriginalFormat = "anthropic"
		norm.OriginalPayload = payload
		return norm, nil
	case "openai":
		var req types.OpenAIRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("translator: parse openai request: %w", err)
		}
		norm := openAIRequestToNormalized(&req)
		norm.OriginalFormat = "openai"
		norm.OriginalPayload = payload
		return norm, nil
	default:
		return nil, fmt.Errorf("translator: unknown format %q", format)
	}
}

// FromNormalized renders a NormalizedResponse back into the client's
// original wire format.
func (dt *DefaultTranslator) FromNormalized(resp *types.NormalizedResponse, originalFormat string) (interface{}, error) {
	switch originalFormat {
	case "anthropic":
		return normalizedToAnthropicResponse(resp), nil
	case "openai":
		return normalizedToOpenAIResponse(resp), nil
	default:
		return nil, fmt.Errorf("translator: unknown format %q", originalFormat)
	}
}

// NormalizedToAnthropicRequest converts a NormalizedRequest to Anthropic
// wire format, for calling an Anthropic-speaking upstream.
func (dt *DefaultTranslator) NormalizedToAnthropicRequest(req *types.NormalizedRequest) (*types.AnthropicRequest, error) {
	return normalizedToAnthropicRequest(req), nil
}

// NormalizedToOpenAIRequest converts a NormalizedRequest to OpenAI wire
// format, for calling an OpenAI-speaking upstream.
func (dt *DefaultTranslator) NormalizedToOpenAIRequest(req *types.NormalizedRequest) (*types.OpenAIRequest, error) {
	return normalizedToOpenAIRequest(req), nil
}

// AnthropicResponseToNormalized converts a raw Anthropic response to Normalized.
func (dt *DefaultTranslator) AnthropicResponseToNormalized(resp *types.AnthropicResponse) (*types.NormalizedResponse, error) {
	return anthropicResponseToNormalized(resp), nil
}

// OpenAIResponseToNormalized converts a raw OpenAI response to Normalized.
func (dt *DefaultTranslator) OpenAIResponseToNormalized(resp *types.OpenAIResponse) (*types.NormalizedResponse, error) {
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("translator: openai response has no choices")
	}
	return openAIResponseToNormalized(resp), nil
}
