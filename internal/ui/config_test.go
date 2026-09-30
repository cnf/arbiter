package ui

import (
	"strings"
	"testing"

	"github.com/cnf/arbiter/internal/config"
)

// The config page (#77 phase 1) has no store dependency, unlike every other
// page — it reads the config the Handler was wired with, not the event
// store. Both cases are covered: nothing wired yet, and a real config.
func TestConfigHandlerNoConfigWired(t *testing.T) {
	h := newTestHandler()
	rec := serve(t, h, "GET", "/admin/ui/config", false)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "no config loaded yet") {
		t.Errorf("page does not explain the unwired state:\n%s", body)
	}
}

// The page must render the live config's YAML with the provider key
// redacted — this is the one property that would make shipping this page a
// secret leak, so it gets its own explicit assertion rather than relying on
// the zero-data render test (which never wires a real config).
func TestConfigHandlerRedactsProviderKey(t *testing.T) {
	h := newTestHandler()
	h.SetConfig(&config.Config{
		Version: "1.0",
		Providers: map[string]config.ProviderConfig{
			"claude": {
				Type:     "anthropic",
				Endpoint: "https://api.anthropic.com",
				Key:      "sk-super-secret",
				Models:   []string{"claude-3-haiku"},
			},
		},
	})

	rec := serve(t, h, "GET", "/admin/ui/config", false)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "sk-super-secret") {
		t.Fatal("the config page leaked a provider API key")
	}
	if !strings.Contains(body, "claude-3-haiku") {
		t.Errorf("page is missing an unredacted field from the config:\n%s", body)
	}
	if !strings.Contains(body, "epoch ") {
		t.Errorf("page is missing the epoch hash:\n%s", body)
	}
}

// The htmx fragment form must render the same content the full page does —
// the split exists so the two can never drift (see configBody.html).
func TestConfigHandlerFragment(t *testing.T) {
	h := newTestHandler()
	h.SetConfig(&config.Config{Version: "1.0"})
	rec := serve(t, h, "GET", "/admin/ui/config", true)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "live config") {
		t.Errorf("fragment did not render the config card:\n%s", body)
	}
	if strings.Contains(body, "<!doctype html>") {
		t.Error("fragment response included a full document, not just the fragment")
	}
}
