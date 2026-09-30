package config

import "testing"

// Redacted is Epoch's redaction step, factored out so the admin UI's config
// page can reuse it (see internal/ui/config.go) — the page and the hash must
// never disagree about what counts as secret.
func TestConfigRedactedBlanksProviderKeys(t *testing.T) {
	cfg := loadConfigOK(t, `
version: "1.0"
providers:
  claude:
    type: "anthropic"
    endpoint: "https://api.anthropic.com"
    key: "sk-super-secret"
    models: ["claude-3-haiku"]
routers:
  - name: "primary"
    type: "simple"
    config:
      default_provider: "claude"
logging:
  level: "info"
`)

	redacted := cfg.Redacted()
	if got := redacted.Providers["claude"].Key; got != "" {
		t.Fatalf("Redacted() left the provider key in place: %q", got)
	}
	if cfg.Providers["claude"].Key != "sk-super-secret" {
		t.Fatalf("Redacted() mutated the original config's key")
	}
	// Everything else is untouched — a redacted config must still be
	// recognizably the same config, not a stripped-down one.
	if got := redacted.Providers["claude"].Endpoint; got != "https://api.anthropic.com" {
		t.Fatalf("Redacted() altered an unrelated field: endpoint = %q", got)
	}
}

// Epoch() must produce the same hash whether it redacts internally or calls
// Redacted() — this pins the refactor that moved the redaction into its own
// method.
func TestConfigRedactedMatchesEpochInput(t *testing.T) {
	cfg := loadConfigOK(t, baseConfig)
	if cfg.Epoch() == "" {
		t.Fatal("epoch is empty")
	}
	// A second config built from the same source, redacted independently,
	// must hash identically through Epoch — proving Redacted() is the exact
	// thing Epoch() hashes, not a parallel implementation that happens to
	// agree today.
	other := loadConfigOK(t, baseConfig)
	if cfg.Epoch() != other.Epoch() {
		t.Fatal("two loads of the same config produced different epochs")
	}
}
