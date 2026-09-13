package http

import (
	"net/http/httptest"
	"testing"
)

func TestClientIPPrefersFirstForwardedAddress(t *testing.T) {
	req := httptest.NewRequest("GET", "http://arbiter.internal/v1/models", nil)
	req.RemoteAddr = "10.0.0.2:54321"
	req.Header.Set("X-Forwarded-For", "203.0.113.10, 10.0.0.1")

	if got := clientIP(req); got != "203.0.113.10" {
		t.Fatalf("clientIP = %q, want 203.0.113.10", got)
	}
}

func TestRequestPathRestoresForwardedPrefix(t *testing.T) {
	req := httptest.NewRequest("GET", "http://arbiter.internal/v1/models", nil)
	req.Header.Set("X-Forwarded-Prefix", "/arbiter/")

	if got := requestPath(req); got != "/arbiter/v1/models" {
		t.Fatalf("requestPath = %q, want /arbiter/v1/models", got)
	}
}
