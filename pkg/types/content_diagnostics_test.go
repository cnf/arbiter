package types

import (
	"encoding/json"
	"errors"
	"testing"
)

// #75: an object-valued messages[i].content must report the affected index
// and observed JSON type, and must never carry the value or message text —
// the whole point is a diagnostic that is safe to log.
func TestOpenAIRequestObjectContentReportsMessageIndexAndType(t *testing.T) {
	payload := []byte(`{"model":"m","messages":[
		{"role":"user","content":"hello"},
		{"role":"user","content":"super-secret-message-text"},
		{"role":"user","content":{"unexpected":"shape"}}
	]}`)

	var req OpenAIRequest
	err := json.Unmarshal(payload, &req)
	if err == nil {
		t.Fatal("expected an error for object-valued content")
	}

	var shapeErr *ContentShapeError
	if !errors.As(err, &shapeErr) {
		t.Fatalf("error = %v (%T), want *ContentShapeError", err, err)
	}
	if shapeErr.MessageIndex != 2 {
		t.Errorf("MessageIndex = %d, want 2", shapeErr.MessageIndex)
	}
	if shapeErr.Field != "content" {
		t.Errorf("Field = %q, want %q", shapeErr.Field, "content")
	}
	if shapeErr.GotType != "object" {
		t.Errorf("GotType = %q, want %q", shapeErr.GotType, "object")
	}

	msg := shapeErr.Error()
	if want := "messages[2].content: got object; expected string or array"; msg != want {
		t.Errorf("Error() = %q, want %q", msg, want)
	}
	// The diagnostic must never carry the client's text — not the offending
	// value, not an unrelated message's text.
	for _, leak := range []string{"unexpected", "shape", "super-secret-message-text", "hello"} {
		if contains(msg, leak) {
			t.Errorf("Error() = %q leaks content (%q)", msg, leak)
		}
	}
}

// A number, a boolean, and null are all shapes a client should never send for
// `content`, and each must be named by its own JSON type rather than folded
// into one generic label.
func TestOpenAIRequestNonObjectNonArrayContentTypes(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"number", `42`, "number"},
		{"boolean", `true`, "boolean"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{"model":"m","messages":[{"role":"user","content":` + tc.content + `}]}`)
			var req OpenAIRequest
			err := json.Unmarshal(payload, &req)
			if err == nil {
				t.Fatal("expected an error")
			}
			var shapeErr *ContentShapeError
			if !errors.As(err, &shapeErr) {
				t.Fatalf("error = %v (%T), want *ContentShapeError", err, err)
			}
			if shapeErr.GotType != tc.want {
				t.Errorf("GotType = %q, want %q", shapeErr.GotType, tc.want)
			}
			if shapeErr.MessageIndex != 0 {
				t.Errorf("MessageIndex = %d, want 0", shapeErr.MessageIndex)
			}
		})
	}
}

// Existing accepted forms — bare string and the array-of-parts form — must
// keep normalizing successfully; the new diagnostic path must never fire on
// them.
func TestOpenAIRequestAcceptedContentFormsStillNormalize(t *testing.T) {
	payload := []byte(`{"model":"m","messages":[
		{"role":"user","content":"plain string"},
		{"role":"user","content":[{"type":"text","text":"array form"}]}
	]}`)
	var req OpenAIRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(req.Messages))
	}
	if req.Messages[0].Content.String() != "plain string" {
		t.Errorf("message 0 content = %q", req.Messages[0].Content.String())
	}
	if req.Messages[1].Content.String() != "array form" {
		t.Errorf("message 1 content = %q", req.Messages[1].Content.String())
	}
}

// Malformed JSON and other parse failures unrelated to content shape must
// retain the plain json error rather than being coerced into a
// ContentShapeError or losing their message.
func TestOpenAIRequestMalformedJSONKeepsGenericError(t *testing.T) {
	var req OpenAIRequest
	err := json.Unmarshal([]byte(`{"model":`), &req)
	if err == nil {
		t.Fatal("expected an error for truncated JSON")
	}
	var shapeErr *ContentShapeError
	if errors.As(err, &shapeErr) {
		t.Fatalf("malformed JSON should not produce a ContentShapeError, got %+v", shapeErr)
	}
}

// A type mismatch on an unrelated field (max_tokens as a string, say) is not
// a content-shape problem; UnmarshalJSON must not misattribute it to a
// message index it never investigated.
func TestOpenAIRequestUnrelatedFieldMismatchKeepsGenericError(t *testing.T) {
	var req OpenAIRequest
	err := json.Unmarshal([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":"not-a-number"}`), &req)
	if err == nil {
		t.Fatal("expected an error")
	}
	var shapeErr *ContentShapeError
	if errors.As(err, &shapeErr) {
		t.Fatalf("unrelated field mismatch should not produce a ContentShapeError, got %+v", shapeErr)
	}
}

func contains(s, substr string) bool {
	return len(substr) > 0 && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}

// #75 (Anthropic side): an object-valued messages[i].content must report the
// affected index and observed JSON type, mirroring the OpenAI behavior
// above, without leaking the value or message text.
func TestAnthropicRequestObjectContentReportsMessageIndexAndType(t *testing.T) {
	payload := []byte(`{"model":"m","max_tokens":10,"messages":[
		{"role":"user","content":"hello"},
		{"role":"user","content":"super-secret-message-text"},
		{"role":"user","content":{"unexpected":"shape"}}
	]}`)

	var req AnthropicRequest
	err := json.Unmarshal(payload, &req)
	if err == nil {
		t.Fatal("expected an error for object-valued content")
	}

	var shapeErr *ContentShapeError
	if !errors.As(err, &shapeErr) {
		t.Fatalf("error = %v (%T), want *ContentShapeError", err, err)
	}
	if !shapeErr.PerMessage {
		t.Error("PerMessage = false, want true")
	}
	if shapeErr.MessageIndex != 2 {
		t.Errorf("MessageIndex = %d, want 2", shapeErr.MessageIndex)
	}
	if shapeErr.Field != "content" {
		t.Errorf("Field = %q, want %q", shapeErr.Field, "content")
	}
	if shapeErr.GotType != "object" {
		t.Errorf("GotType = %q, want %q", shapeErr.GotType, "object")
	}

	msg := shapeErr.Error()
	if want := "messages[2].content: got object; expected string or array"; msg != want {
		t.Errorf("Error() = %q, want %q", msg, want)
	}
	for _, leak := range []string{"unexpected", "shape", "super-secret-message-text", "hello"} {
		if contains(msg, leak) {
			t.Errorf("Error() = %q leaks content (%q)", msg, leak)
		}
	}
}

// The request's top-level `system` field has no message index at all — an
// object-valued system must be reported as a top-level shape error, not
// coerced into (or confused with) a per-message one.
func TestAnthropicRequestObjectSystemReportsTopLevelError(t *testing.T) {
	payload := []byte(`{"model":"m","max_tokens":10,"system":{"unexpected":"shape"},"messages":[{"role":"user","content":"hi"}]}`)

	var req AnthropicRequest
	err := json.Unmarshal(payload, &req)
	if err == nil {
		t.Fatal("expected an error for object-valued system")
	}

	var shapeErr *ContentShapeError
	if !errors.As(err, &shapeErr) {
		t.Fatalf("error = %v (%T), want *ContentShapeError", err, err)
	}
	if shapeErr.PerMessage {
		t.Error("PerMessage = true, want false for a top-level field")
	}
	if shapeErr.Field != "system" {
		t.Errorf("Field = %q, want %q", shapeErr.Field, "system")
	}
	if shapeErr.GotType != "object" {
		t.Errorf("GotType = %q, want %q", shapeErr.GotType, "object")
	}

	msg := shapeErr.Error()
	if want := "system: got object; expected string or array"; msg != want {
		t.Errorf("Error() = %q, want %q", msg, want)
	}
	if contains(msg, "unexpected") || contains(msg, "shape") {
		t.Errorf("Error() = %q leaks content", msg)
	}
}

// A number and a boolean are shapes a client should never send for Anthropic
// `content` either, and each must be named by its own JSON type.
func TestAnthropicRequestNonObjectNonArrayContentTypes(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"number", `42`, "number"},
		{"boolean", `true`, "boolean"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{"model":"m","max_tokens":10,"messages":[{"role":"user","content":` + tc.content + `}]}`)
			var req AnthropicRequest
			err := json.Unmarshal(payload, &req)
			if err == nil {
				t.Fatal("expected an error")
			}
			var shapeErr *ContentShapeError
			if !errors.As(err, &shapeErr) {
				t.Fatalf("error = %v (%T), want *ContentShapeError", err, err)
			}
			if shapeErr.GotType != tc.want {
				t.Errorf("GotType = %q, want %q", shapeErr.GotType, tc.want)
			}
			if shapeErr.MessageIndex != 0 {
				t.Errorf("MessageIndex = %d, want 0", shapeErr.MessageIndex)
			}
		})
	}
}

// Existing accepted forms for Anthropic — bare string and the block-array
// form, for both `content` and `system` — must keep normalizing
// successfully.
func TestAnthropicRequestAcceptedContentFormsStillNormalize(t *testing.T) {
	payload := []byte(`{"model":"m","max_tokens":10,"system":[{"type":"text","text":"be concise"}],"messages":[
		{"role":"user","content":"plain string"},
		{"role":"user","content":[{"type":"text","text":"array form"}]}
	]}`)
	var req AnthropicRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.System != "be concise" {
		t.Errorf("System = %q, want %q", req.System, "be concise")
	}
	if len(req.Messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(req.Messages))
	}
	if len(req.Messages[0].Content) != 1 || req.Messages[0].Content[0].Text != "plain string" {
		t.Errorf("message 0 content = %+v", req.Messages[0].Content)
	}
	if len(req.Messages[1].Content) != 1 || req.Messages[1].Content[0].Text != "array form" {
		t.Errorf("message 1 content = %+v", req.Messages[1].Content)
	}
}

// Malformed JSON must retain the plain json error for the Anthropic request
// too, rather than being coerced into a ContentShapeError.
func TestAnthropicRequestMalformedJSONKeepsGenericError(t *testing.T) {
	var req AnthropicRequest
	err := json.Unmarshal([]byte(`{"model":`), &req)
	if err == nil {
		t.Fatal("expected an error for truncated JSON")
	}
	var shapeErr *ContentShapeError
	if errors.As(err, &shapeErr) {
		t.Fatalf("malformed JSON should not produce a ContentShapeError, got %+v", shapeErr)
	}
}

// A type mismatch on an unrelated field (max_tokens as a string) must not be
// misattributed to a message index.
func TestAnthropicRequestUnrelatedFieldMismatchKeepsGenericError(t *testing.T) {
	var req AnthropicRequest
	err := json.Unmarshal([]byte(`{"model":"m","max_tokens":"not-a-number","messages":[{"role":"user","content":"hi"}]}`), &req)
	if err == nil {
		t.Fatal("expected an error")
	}
	var shapeErr *ContentShapeError
	if errors.As(err, &shapeErr) {
		t.Fatalf("unrelated field mismatch should not produce a ContentShapeError, got %+v", shapeErr)
	}
}
