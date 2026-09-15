package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cnf/arbiter/internal/logging"
)

func okHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
}

// With no configured header the gate is open: this is the dev default, and
// the reason it must not be the *production* default is the bind, not this.
func TestGateOpenWhenUnset(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/reload", nil)
	resp := httptest.NewRecorder()
	Gate("", okHandler())(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when no header is configured", resp.Code)
	}
}

func TestGateRejectsMissingHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/reload", nil)
	resp := httptest.NewRecorder()
	Gate("X-Forwarded-User", okHandler())(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when the gate header is absent", resp.Code)
	}
}

func TestGateAdmitsPresentHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/reload", nil)
	req.Header.Set("X-Forwarded-User", "anyone")
	resp := httptest.NewRecorder()
	Gate("X-Forwarded-User", okHandler())(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when the gate header is present", resp.Code)
	}
}

func TestReloadHandlerReportsSuccess(t *testing.T) {
	var called bool
	h := NewAdminHandler(func(context.Context) error {
		called = true
		return nil
	}, logging.NewStdoutLogger("error"))

	resp := httptest.NewRecorder()
	h.ReloadHandler(resp, httptest.NewRequest(http.MethodPost, "/admin/reload", nil))

	if !called {
		t.Fatal("reload was not invoked")
	}
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 on a successful reload", resp.Code)
	}
}

// A rejected reload is a 500, and the body must say the reload was rejected —
// the caller needs to know nothing changed, not just that something failed.
func TestReloadHandlerReportsRejection(t *testing.T) {
	h := NewAdminHandler(func(context.Context) error {
		return errors.New("no routers configured")
	}, logging.NewStdoutLogger("error"))

	resp := httptest.NewRecorder()
	h.ReloadHandler(resp, httptest.NewRequest(http.MethodPost, "/admin/reload", nil))

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on a rejected reload", resp.Code)
	}
	if body := resp.Body.String(); !strings.Contains(body, "reload rejected") || !strings.Contains(body, "no routers configured") {
		t.Fatalf("body = %q, want it to name the rejection and its cause", body)
	}
}
