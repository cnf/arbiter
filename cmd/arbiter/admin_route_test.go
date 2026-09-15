package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/cnf/arbiter/internal/config"
	arbiterhttp "github.com/cnf/arbiter/internal/http"
	"github.com/cnf/arbiter/internal/logging"
)

// newTestRouter builds the real route table (newRouter) against a handler
// serving validConfigA, so tests exercise the actual /admin/* wiring rather
// than a hand-rolled mux that could drift from main.
func newTestRouter(t *testing.T, configPath, forwardAuthHeader string) (*arbiterhttp.Handler, http.Handler) {
	t.Helper()
	logger := logging.NewStdoutLogger("error")
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p, err := buildPipeline(cfg, logger)
	if err != nil {
		t.Fatalf("build pipeline: %v", err)
	}
	h := arbiterhttp.NewHandler(arbiterhttp.NewRuntime(p, configuredModels(cfg), cfg.SessionAffinity.Header), logger)
	admin := arbiterhttp.NewAdminHandler(func(ctx context.Context) error {
		return reload(ctx, configPath, h, logger)
	}, logger)
	return h, newRouter(h, admin, forwardAuthHeader)
}

// The gate must be wired onto the *route*, not just available as a helper:
// with a header configured, an ungated POST to /admin/reload must 401.
func TestAdminReloadRouteIsGated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lanes.yaml")
	if err := os.WriteFile(path, []byte(validConfigA), 0o600); err != nil {
		t.Fatal(err)
	}
	_, r := newTestRouter(t, path, "X-Forwarded-User")

	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/admin/reload", nil))
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an ungated /admin/reload", resp.Code)
	}
}

// With the header present, the route reaches the reload — and the reload is
// the real one, so a config change on disk is picked up by the call.
func TestAdminReloadRouteSwapsRuntime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lanes.yaml")
	if err := os.WriteFile(path, []byte(validConfigA), 0o600); err != nil {
		t.Fatal(err)
	}
	h, r := newTestRouter(t, path, "X-Forwarded-User")

	if got := modelIDs(t, h); len(got) != 1 || got[0] != "model-a" {
		t.Fatalf("initial models = %v, want [model-a]", got)
	}

	if err := os.WriteFile(path, []byte(validConfigB), 0o600); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/reload", nil)
	req.Header.Set("X-Forwarded-User", "op")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", resp.Code, resp.Body.String())
	}
	if got := modelIDs(t, h); len(got) != 1 || got[0] != "model-b" {
		t.Fatalf("models after reload = %v, want [model-b]", got)
	}
}

// A rejected reload must be reported (500) and leave the old runtime serving.
func TestAdminReloadRouteRejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lanes.yaml")
	if err := os.WriteFile(path, []byte(validConfigA), 0o600); err != nil {
		t.Fatal(err)
	}
	h, r := newTestRouter(t, path, "")

	bad := "providers:\n  a:\n    type: \"openai\"\n    endpoint: \"http://x/v1\"\n    models: [\"m\"]\nrouters: []\n"
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}

	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/admin/reload", nil))
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for a rejected reload", resp.Code)
	}
	if got := modelIDs(t, h); len(got) != 1 || got[0] != "model-a" {
		t.Fatalf("models after rejected reload = %v, want [model-a] preserved", got)
	}
}

// The reload endpoint is POST-only; a GET must not match the route.
func TestAdminReloadRejectsGet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lanes.yaml")
	if err := os.WriteFile(path, []byte(validConfigA), 0o600); err != nil {
		t.Fatal(err)
	}
	_, r := newTestRouter(t, path, "")

	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/reload", nil))
	if resp.Code == http.StatusOK {
		t.Fatalf("GET /admin/reload returned 200; the route must be POST-only")
	}
}
