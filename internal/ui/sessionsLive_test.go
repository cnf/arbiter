package ui

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/store"
)

// The tail must return exactly the sessions with a *live* affinity pin — not
// merely one inside the "live only" filter's grace window (TestSessionsLiveOnlyFilter
// already covers that separate question) — plus a nav ActiveCount matching
// ActiveSessionCount's own definition. A session with no pin at all, and one
// whose pin already expired, must both be absent from the poll: refreshing
// either would be work spent on a lane the poller's own doc comment says is
// not expected to change.
func TestSessionsTailReturnsOnlyLivePinnedLanes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	logger := logging.NewStdoutLogger("error")

	w, err := store.NewSQLiteWriter(path, logger)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	now := time.Now().UTC()
	w.Record(store.Event{TraceID: "t1", SessionKey: "sess-live", Kind: "client",
		Provider: "p", Model: "m", StatusCode: 200, Ts: now, LatencyMs: 1})
	w.Record(store.Event{TraceID: "t2", SessionKey: "sess-expired", Kind: "client",
		Provider: "p", Model: "m", StatusCode: 200, Ts: now.Add(time.Second), LatencyMs: 1})
	w.Record(store.Event{TraceID: "t3", SessionKey: "sess-nopin", Kind: "client",
		Provider: "p", Model: "m", StatusCode: 200, Ts: now.Add(2 * time.Second), LatencyMs: 1})

	if err := w.SavePin(context.Background(), store.AffinityPin{
		SessionKey: "sess-live", RequestedModel: "auto", Provider: "p", Model: "m",
		ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("SavePin sess-live: %v", err)
	}
	if err := w.SavePin(context.Background(), store.AffinityPin{
		SessionKey: "sess-expired", RequestedModel: "auto", Provider: "p", Model: "m",
		ExpiresAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("SavePin sess-expired: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	r, err := store.OpenReader(path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, logger)

	code, body := serveJSON(t, h, "/admin/ui/sessions/tail?since=720h")
	if code != 200 {
		t.Fatalf("tail = %d, want 200; body: %s", code, body)
	}

	var resp sessionTailResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode tail response: %v; body: %s", err, body)
	}
	if resp.ActiveCount != 1 {
		t.Errorf("ActiveCount = %d, want 1 (only sess-live holds a live pin)", resp.ActiveCount)
	}
	if len(resp.Lanes) != 1 {
		t.Fatalf("got %d lanes, want 1; body: %s", len(resp.Lanes), body)
	}
	if resp.Lanes[0].Key != "sess-live" {
		t.Errorf("lane key = %q, want sess-live", resp.Lanes[0].Key)
	}
	if !strings.Contains(resp.Lanes[0].HTML, "sess-live") {
		t.Errorf("lane HTML does not render the session's own id: %s", resp.Lanes[0].HTML)
	}
	if !strings.Contains(resp.Lanes[0].HTML, `class="lane-row"`) {
		t.Errorf("lane HTML does not look like a rendered lane-row: %s", resp.Lanes[0].HTML)
	}
}

// The tail's own filters (errors-only, q, client_only) must narrow its result
// the same way the page's own toolbar does — a poller that ignored them would
// resurrect a lane the reader had just filtered out from underneath them.
func TestSessionsTailRespectsErrorsOnlyFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	logger := logging.NewStdoutLogger("error")

	w, err := store.NewSQLiteWriter(path, logger)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	now := time.Now().UTC()
	w.Record(store.Event{TraceID: "t1", SessionKey: "sess-ok", Kind: "client",
		Provider: "p", Model: "m", StatusCode: 200, Ts: now, LatencyMs: 1})
	w.Record(store.Event{TraceID: "t2", SessionKey: "sess-bad", Kind: "client",
		Provider: "p", Model: "m", StatusCode: 500, Ts: now.Add(time.Second), LatencyMs: 1})

	for _, key := range []string{"sess-ok", "sess-bad"} {
		if err := w.SavePin(context.Background(), store.AffinityPin{
			SessionKey: key, RequestedModel: "auto", Provider: "p", Model: "m",
			ExpiresAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatalf("SavePin %s: %v", key, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	r, err := store.OpenReader(path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, logger)

	_, body := serveJSON(t, h, "/admin/ui/sessions/tail?since=720h&errors=1")
	var resp sessionTailResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode: %v; body: %s", err, body)
	}
	if len(resp.Lanes) != 1 || resp.Lanes[0].Key != "sess-bad" {
		t.Errorf("errors=1 should return only sess-bad, got: %+v", resp.Lanes)
	}
	// Both sessions still hold live pins — the nav count is unaffected by
	// the page-local errors filter, since it answers the same "how many
	// sessions are routing-pinned right now" question the nav bar always
	// asks, filters or not.
	if resp.ActiveCount != 2 {
		t.Errorf("ActiveCount = %d, want 2 regardless of the errors filter", resp.ActiveCount)
	}
}

// since must parse as a positive Go duration, the same rule SessionsHandler
// itself enforces — a malformed value silently defaulting would show a
// poller quietly following a different window than the page believes it is on.
func TestSessionsTailRejectsMalformedSince(t *testing.T) {
	h, _ := newSeededHandler(t)
	code, body := serveJSON(t, h, "/admin/ui/sessions/tail?since=7d")
	if code != 400 {
		t.Errorf("since=7d = %d, want 400", code)
	}
	if !strings.Contains(body, "since") {
		t.Error("the 400 does not name the offending parameter")
	}
}

// A disabled store (no reader) must answer the tail's poll with a JSON error
// body, not the HTML disabled-banner every page renders — the client fetches
// this endpoint expecting JSON and would otherwise fail to parse the response
// entirely, turning "the store is off" into a silent, unexplained stop.
func TestSessionsTailDisabledStoreIsJSON(t *testing.T) {
	h := newTestHandler()
	code, body := serveJSON(t, h, "/admin/ui/sessions/tail")
	if code != 503 {
		t.Errorf("disabled store tail = %d, want 503", code)
	}
	var resp map[string]string
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("disabled-store response is not JSON: %v; body: %s", err, body)
	}
	if resp["error"] == "" {
		t.Error("disabled-store response has no error field")
	}
}
