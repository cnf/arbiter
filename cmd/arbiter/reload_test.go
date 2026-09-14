package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/config"
	arbiterhttp "github.com/cnf/arbiter/internal/http"
	"github.com/cnf/arbiter/internal/logging"
)

const validConfigA = `
providers:
  a:
    type: "openai"
    endpoint: "http://localhost:1/v1"
    models: ["model-a"]
routers:
  - name: "primary"
    type: "simple"
    config:
      default_provider: "a"
`

const validConfigB = `
providers:
  b:
    type: "anthropic"
    endpoint: "http://localhost:2"
    models: ["model-b"]
routers:
  - name: "primary"
    type: "simple"
    config:
      default_provider: "b"
`

// modelIDs reads the model list currently served by the handler.
func modelIDs(t *testing.T, h *arbiterhttp.Handler) []string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	resp := httptest.NewRecorder()
	h.ModelsHandler(resp, req)

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode models: %v", err)
	}
	ids := make([]string, len(body.Data))
	for i, m := range body.Data {
		ids[i] = m.ID
	}
	return ids
}

// A valid config change must swap the served model list.
func TestWatchConfigReloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lanes.yaml")
	if err := os.WriteFile(path, []byte(validConfigA), 0o600); err != nil {
		t.Fatal(err)
	}

	logger := logging.NewStdoutLogger("error")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load initial config: %v", err)
	}
	p, err := buildPipeline(cfg, logger)
	if err != nil {
		t.Fatalf("build initial pipeline: %v", err)
	}
	h := arbiterhttp.NewHandler(arbiterhttp.NewRuntime(p, configuredModels(cfg)), logger)

	if got := modelIDs(t, h); len(got) != 1 || got[0] != "model-a" {
		t.Fatalf("initial models = %v, want [model-a]", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := watchConfig(ctx, path, h, logger); err != nil {
			t.Errorf("watchConfig: %v", err)
		}
	}()

	// Give the watcher a moment to register before writing.
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(path, []byte(validConfigB), 0o600); err != nil {
		t.Fatal(err)
	}

	waitForModel(t, h, "model-b")
}

// A reload that fails to parse must leave the previous runtime serving.
func TestWatchConfigRejectsInvalidReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lanes.yaml")
	if err := os.WriteFile(path, []byte(validConfigA), 0o600); err != nil {
		t.Fatal(err)
	}

	logger := logging.NewStdoutLogger("error")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := buildPipeline(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	h := arbiterhttp.NewHandler(arbiterhttp.NewRuntime(p, configuredModels(cfg)), logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = watchConfig(ctx, path, h, logger)
	}()
	time.Sleep(100 * time.Millisecond)

	// Write a config that is valid YAML but fails validation (no routers).
	bad := "providers:\n  a:\n    type: \"openai\"\n    endpoint: \"http://x/v1\"\n    models: [\"m\"]\nrouters: []\n"
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}

	// The bad write must not take effect. Give the debounce time to fire, then
	// confirm the model list is unchanged.
	time.Sleep(500 * time.Millisecond)
	if got := modelIDs(t, h); len(got) != 1 || got[0] != "model-a" {
		t.Fatalf("models after invalid reload = %v, want [model-a] preserved", got)
	}
}

func waitForModel(t *testing.T, h *arbiterhttp.Handler, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := modelIDs(t, h)
		if len(got) == 1 && got[0] == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("models never became [%s]; last = %v", want, modelIDs(t, h))
}
