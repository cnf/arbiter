package http

import (
	"testing"
)

func TestRuntimeSessionHeaderDefaults(t *testing.T) {
	rt := NewRuntime(nil, nil, "")
	if rt.sessionHeader != defaultSessionHeader {
		t.Fatalf("sessionHeader = %q, want default %q", rt.sessionHeader, defaultSessionHeader)
	}
}

func TestRuntimeSessionHeaderOverride(t *testing.T) {
	rt := NewRuntime(nil, nil, "X-My-Session")
	if rt.sessionHeader != "X-My-Session" {
		t.Fatalf("sessionHeader = %q, want the configured override", rt.sessionHeader)
	}
}
