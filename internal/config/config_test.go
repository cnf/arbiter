package config

import (
	"os"
	"testing"
)

func TestLoadAcceptsOllamaProvider(t *testing.T) {
	path := t.TempDir() + "/lanes.yaml"
	contents := `
version: "1.0"
providers:
  local:
    type: "ollama"
    endpoint: "http://localhost:11434/v1"
    models: ["llama2"]
routers:
  - name: "primary"
    type: "simple"
    config:
      default_provider: "local"
logging:
  level: "info"
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Providers["local"].Type != "ollama" {
		t.Fatalf("provider type = %q, want ollama", cfg.Providers["local"].Type)
	}
}

func TestLoadExpandsEnvironmentVariables(t *testing.T) {
	path := t.TempDir() + "/lanes.yaml"
	contents := `
version: "1.0"
providers:
  provider:
    type: "openai"
    endpoint: "http://localhost:1234/v1"
    key: "${ARBITER_TEST_KEY}"
    models: ["test-model"]
routers:
  - name: "primary"
    type: "simple"
    config:
      default_provider: "provider"
logging:
  level: "info"
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.Setenv("ARBITER_TEST_KEY", "secret-value"); err != nil {
		t.Fatalf("set env: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("ARBITER_TEST_KEY") })

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Providers["provider"].Key; got != "secret-value" {
		t.Fatalf("provider key = %q, want secret-value", got)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := t.TempDir() + "/lanes.yaml"
	contents := `
version: "1.0"
providers:
  provider:
    type: "openai"
    endpoint: "http://localhost:1234/v1"
    api_key: "typo-should-be-key"
    models: ["test-model"]
routers:
  - name: "primary"
    type: "simple"
    config:
      default_provider: "provider"
logging:
  level: "info"
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load: want error for unknown field \"api_key\", got nil")
	}
}

func TestLoadStripsTrailingSlashFromEndpoint(t *testing.T) {
	path := t.TempDir() + "/lanes.yaml"
	contents := `
version: "1.0"
providers:
  provider:
    type: "openai"
    endpoint: "http://localhost:1234/v1/"
    key: "test-key"
    models: ["test-model"]
routers:
  - name: "primary"
    type: "simple"
    config:
      default_provider: "provider"
logging:
  level: "info"
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Providers["provider"].Endpoint; got != "http://localhost:1234/v1" {
		t.Fatalf("provider endpoint = %q, want trailing slash stripped", got)
	}
}

func TestLoadSessionAffinityAndCacheTTL(t *testing.T) {
	path := t.TempDir() + "/lanes.yaml"
	contents := `
version: "1.0"
providers:
  provider:
    type: "openai"
    endpoint: "http://localhost:1234/v1"
    models: ["test-model"]
    cache_ttl: "30s"
routers:
  - name: "primary"
    type: "simple"
    config:
      default_provider: "provider"
session_affinity:
  header: "X-Custom-Session"
  default_ttl: "10m"
logging:
  level: "info"
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SessionAffinity.Header != "X-Custom-Session" {
		t.Fatalf("session header = %q, want X-Custom-Session", cfg.SessionAffinity.Header)
	}
	if cfg.SessionAffinity.DefaultTTL != "10m" {
		t.Fatalf("default_ttl = %q, want 10m", cfg.SessionAffinity.DefaultTTL)
	}
	if cfg.Providers["provider"].CacheTTL != "30s" {
		t.Fatalf("cache_ttl = %q, want 30s", cfg.Providers["provider"].CacheTTL)
	}
}

func TestLoadRejectsInvalidCacheTTL(t *testing.T) {
	path := t.TempDir() + "/lanes.yaml"
	contents := `
version: "1.0"
providers:
  provider:
    type: "openai"
    endpoint: "http://localhost:1234/v1"
    models: ["test-model"]
    cache_ttl: "not-a-duration"
routers:
  - name: "primary"
    type: "simple"
    config:
      default_provider: "provider"
logging:
  level: "info"
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load: want error for invalid cache_ttl, got nil")
	}
}

func TestLoadRejectsInvalidSessionAffinityTTL(t *testing.T) {
	path := t.TempDir() + "/lanes.yaml"
	contents := `
version: "1.0"
providers:
  provider:
    type: "openai"
    endpoint: "http://localhost:1234/v1"
    models: ["test-model"]
routers:
  - name: "primary"
    type: "simple"
    config:
      default_provider: "provider"
session_affinity:
  default_ttl: "5 minutes"
logging:
  level: "info"
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load: want error for invalid session_affinity.default_ttl, got nil")
	}
}
