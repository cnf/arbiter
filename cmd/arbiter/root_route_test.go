package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bare root is a convenience redirect to the admin UI's own entry point
// (which itself redirects on to /admin/ui/requests), rather than a 404 —
// there is nothing else to serve at "/".
func TestRootRedirectsToAdminUI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lanes.yaml")
	if err := os.WriteFile(path, []byte(validConfigA), 0o600); err != nil {
		t.Fatal(err)
	}
	_, r := newTestRouter(t, path, "")

	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/", nil))
	if resp.Code != http.StatusFound {
		t.Fatalf("GET / = %d, want 302", resp.Code)
	}
	if loc := resp.Header().Get("Location"); loc != "/admin/ui/" {
		t.Fatalf("redirect Location = %q, want /admin/ui/", loc)
	}
}

// An unmatched path — under /, /v1, or anywhere else — gets a JSON body
// rather than Go's default plain-text 404, so a client that parses every
// response as JSON doesn't choke on the one response that isn't.
func TestUnknownRouteReturnsJSON404(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lanes.yaml")
	if err := os.WriteFile(path, []byte(validConfigA), 0o600); err != nil {
		t.Fatal(err)
	}
	_, r := newTestRouter(t, path, "")

	for _, target := range []string{"/nope", "/v1/nope", "/v1/embeddings"} {
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, target, nil))
		if resp.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, resp.Code)
		}
		if ct := resp.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("GET %s Content-Type = %q, want application/json", target, ct)
		}
		want := `{"code":404,"detail":"Not Found"}`
		if got := resp.Body.String(); got != want {
			t.Errorf("GET %s body = %q, want %q", target, got, want)
		}
	}
}

// Both chat endpoints must be reachable plain and /v1-prefixed: a client's
// base_url convention (whether it already includes /v1) shouldn't decide
// whether Arbiter has a route. Malformed bodies are used so the assertion is
// purely about routing (all four paths must reach the same handler and fail
// identically), not about a specific pipeline outcome.
func TestChatEndpointsRegisteredBothPlainAndV1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lanes.yaml")
	if err := os.WriteFile(path, []byte(validConfigA), 0o600); err != nil {
		t.Fatal(err)
	}
	_, r := newTestRouter(t, path, "")

	pairs := [][2]string{
		{"/v1/messages", "/messages"},
		{"/chat/completions", "/v1/chat/completions"},
	}
	for _, pair := range pairs {
		var codes [2]int
		for i, target := range pair {
			resp := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, target, strings.NewReader("not json"))
			r.ServeHTTP(resp, req)
			codes[i] = resp.Code
			if resp.Code == http.StatusNotFound {
				t.Errorf("POST %s = 404, want the route to exist", target)
			}
		}
		if codes[0] != codes[1] {
			t.Errorf("%s and %s disagree: %d vs %d", pair[0], pair[1], codes[0], codes[1])
		}
	}
}
