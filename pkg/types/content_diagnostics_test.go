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
