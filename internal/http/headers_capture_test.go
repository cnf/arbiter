package http

import (
	"net/http"
	"testing"
)

// captureHeaders must mask anything credential-shaped and leave everything
// else — especially User-Agent, the header this feature exists for —
// untouched. This is a single-operator tool (REQUIREMENTS.md), so the bar is
// "don't persist secrets into the db file", not general privacy redaction.
func TestCaptureHeadersRedactsCredentials(t *testing.T) {
	h := http.Header{}
	h.Set("User-Agent", "opencode/1.0")
	h.Set("Authorization", "Bearer sk-real-secret")
	h.Set("X-Api-Key", "sk-another-secret")
	h.Set("Cookie", "session=abc123")
	h.Set("X-Session-Id", "chat-42")

	got := captureHeaders(h)

	if got["User-Agent"] != "opencode/1.0" {
		t.Errorf("User-Agent = %q, want untouched", got["User-Agent"])
	}
	if got["X-Session-Id"] != "chat-42" {
		t.Errorf("X-Session-Id = %q, want untouched", got["X-Session-Id"])
	}
	for _, name := range []string{"Authorization", "X-Api-Key", "Cookie"} {
		if got[name] != "[REDACTED]" {
			t.Errorf("%s = %q, want [REDACTED]", name, got[name])
		}
	}
}

func TestCaptureHeadersEmptyIsNil(t *testing.T) {
	if got := captureHeaders(http.Header{}); got != nil {
		t.Errorf("captureHeaders(empty) = %v, want nil", got)
	}
}

func TestCaptureHeadersJoinsMultiValue(t *testing.T) {
	h := http.Header{}
	h.Add("X-Forwarded-For", "1.1.1.1")
	h.Add("X-Forwarded-For", "2.2.2.2")

	got := captureHeaders(h)
	if got["X-Forwarded-For"] != "1.1.1.1, 2.2.2.2" {
		t.Errorf("X-Forwarded-For = %q, want joined values", got["X-Forwarded-For"])
	}
}
