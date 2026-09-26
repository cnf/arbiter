package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/store"
)

// The lane view is what #52 actually changes: one conversation renders as one
// lane (session id header + a timeline of its own requests) instead of a
// separate sessions index cross-referenced against a flat requests list. This
// exercises the whole render path — the real reader queries, foldRequestLines/
// attachTraceChildren, and the lane/node templates — over a seeded store, not
// a zero-data render (TestEveryPageRendersWithZeroData already covers that).
func TestSessionsLaneRendersRequestsAndSatellites(t *testing.T) {
	now := time.Now().UTC()
	mk := func(kind, traceID, sessionKey, provider string, status int, offset time.Duration) store.Event {
		return store.Event{
			TraceID: traceID, SessionKey: sessionKey, Kind: kind,
			Provider: provider, Model: "m", StatusCode: status,
			Ts: now.Add(offset), LatencyMs: 100,
		}
	}
	events := []store.Event{
		// A lane with a client request and a classifier satellite sharing its
		// trace_id — the satellite must nest under the client node, not
		// appear as a second top-level line (attachTraceChildren's job).
		mk("classifier", "tr-1", "sess-one", "openrouter-decisions", 200, 0),
		mk("client", "tr-1", "sess-one", "anthropic", 200, 1*time.Second),
		// A second, separate lane so the page renders more than one row.
		mk("client", "tr-2", "sess-two", "anthropic", 500, 2*time.Second),
	}
	h, _ := newSeededHandler(t, events...)

	body := serve(t, h, "GET", "/admin/ui/sessions", false).Body.String()

	for _, want := range []string{
		// Both lanes rendered, each under its own session id (both keys are
		// short enough that shortSessionKey returns them unchanged).
		"sess-one",
		"sess-two",
		// The classifier satellite's own node class, distinct from a client node.
		`class="node sat classifier`,
		// The client node for the same lane.
		`class="node client`,
		// The failed lane's error status reaches the page (s-err from statusClass).
		"s-err",
		// The persistent detail panel, present on load with no selection.
		"default-panel",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("sessions page missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// A session whose only client row has no captured content (capture off) must
// say so in the lane header rather than rendering an empty preview — the
// three-way distinction lanePreview exists for (see noClientBodyPreview).
func TestSessionsLanePreviewNoteWhenCaptureOff(t *testing.T) {
	now := time.Now().UTC()
	h, _ := newSeededHandler(t, store.Event{
		TraceID: "t", SessionKey: "sess-nocap", Kind: "client",
		Provider: "p", Model: "m", StatusCode: 200, Ts: now, LatencyMs: 1,
	})
	// captureContent defaults to false on a fresh Handler/newSeededHandler
	// (no pipeline sets it), which is exactly the case lanePreview's first
	// branch handles.

	body := serve(t, h, "GET", "/admin/ui/sessions", false).Body.String()
	if !strings.Contains(body, "content capture is off") {
		t.Errorf("expected the capture-off preview note, got:\n%s", body)
	}
}

// The toolbar's "all"/"errors only" radio pair must round-trip both ways:
// selecting "all" (?errors=, empty value) has to actually clear the filter,
// and "errors only" (?errors=1) has to be reversible by going back to "all".
// Regression for a bug where ErrorsOnly was q.Has("errors") — true for both
// states, since the "all" radio still submits the key with an empty value —
// so "all" never worked and "errors only" could never be turned back off.
func TestSessionsErrorsFilterRoundTrips(t *testing.T) {
	now := time.Now().UTC()
	h, _ := newSeededHandler(t,
		store.Event{TraceID: "ok", SessionKey: "sess-ok", Kind: "client",
			Provider: "p", Model: "m", StatusCode: 200, Ts: now, LatencyMs: 1},
		store.Event{TraceID: "bad", SessionKey: "sess-bad", Kind: "client",
			Provider: "p", Model: "m", StatusCode: 500, Ts: now.Add(time.Second), LatencyMs: 1},
	)

	// Default (no ?errors param at all): both lanes present.
	body := serve(t, h, "GET", "/admin/ui/sessions", false).Body.String()
	if !strings.Contains(body, "sess-ok") || !strings.Contains(body, "sess-bad") {
		t.Fatalf("default view should show both sessions, got:\n%s", body)
	}

	// "errors only" (?errors=1): only the errored lane.
	body = serve(t, h, "GET", "/admin/ui/sessions?errors=1", false).Body.String()
	if strings.Contains(body, "sess-ok") {
		t.Errorf("errors=1 should hide the clean session, got:\n%s", body)
	}
	if !strings.Contains(body, "sess-bad") {
		t.Errorf("errors=1 should still show the errored session, got:\n%s", body)
	}

	// "all" (?errors=, empty value — what the toolbar's own radio submits):
	// must behave identically to no param at all, showing both lanes again.
	body = serve(t, h, "GET", "/admin/ui/sessions?errors=", false).Body.String()
	if !strings.Contains(body, "sess-ok") || !strings.Contains(body, "sess-bad") {
		t.Errorf("errors= (the toolbar's \"all\" radio) should show both sessions again, got:\n%s", body)
	}
}

// The nav bar's "N active" Sessions stat (Handler.base) must reflect real
// affinity_pins rows still inside their TTL, not the "3" literal the mockup
// shipped with. This seeds a pin directly (bypassing the pipeline, which is
// the only other writer of affinity_pins) and checks the rendered nav.
func TestNavSessionsStatReflectsActivePins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	logger := logging.NewStdoutLogger("error")

	w, err := store.NewSQLiteWriter(path, logger)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	if err := w.SavePin(context.Background(), store.AffinityPin{
		SessionKey: "sess-live", RequestedModel: "auto", Provider: "anthropic", Model: "m",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("SavePin: %v", err)
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

	body := serve(t, h, "GET", "/admin/ui/sessions", false).Body.String()
	if !strings.Contains(body, `<span class="stat">1<span class="u">active</span></span>`) {
		t.Errorf("nav bar's Sessions stat should read 1 active (one live pin), got:\n%s", body)
	}
}

// The lane list's "hot" dot must appear only on a lane whose session has a
// live affinity pin, and only for the duration that pin is live — the same
// "still within cache TTL" definition the nav bar's "N active" stat and
// store.ActiveSessionKeys use. Seeds a client request for two sessions and a
// pin for only one of them, so the dot's presence/absence is a real
// discriminator, not just "always on" or "always off".
func TestSessionsLaneHotDotReflectsActivePin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	logger := logging.NewStdoutLogger("error")

	w, err := store.NewSQLiteWriter(path, logger)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	now := time.Now().UTC()
	w.Record(store.Event{TraceID: "t1", SessionKey: "sess-hot", Kind: "client",
		Provider: "p", Model: "m", StatusCode: 200, Ts: now, LatencyMs: 1})
	w.Record(store.Event{TraceID: "t2", SessionKey: "sess-cold", Kind: "client",
		Provider: "p", Model: "m", StatusCode: 200, Ts: now.Add(time.Second), LatencyMs: 1})

	if err := w.SavePin(context.Background(), store.AffinityPin{
		SessionKey: "sess-hot", RequestedModel: "auto", Provider: "p", Model: "m",
		ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("SavePin: %v", err)
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

	body := serve(t, h, "GET", "/admin/ui/sessions", false).Body.String()

	hotIdx := strings.Index(body, "sess-hot")
	coldIdx := strings.Index(body, "sess-cold")
	if hotIdx == -1 || coldIdx == -1 {
		t.Fatalf("expected both lanes rendered, got:\n%s", body)
	}
	// Each lane's own head line is a bounded window right after its sid
	// chip — checking for "hot" class within that window (up to the next
	// lane, or a generous cap) rather than a whole-page substring check,
	// since the legend itself also contains a "hot" span.
	laneSlice := func(start int) string {
		end := start + 400
		if end > len(body) {
			end = len(body)
		}
		return body[start:end]
	}
	if !strings.Contains(laneSlice(hotIdx), `class="hot"`) {
		t.Errorf("sess-hot's lane should carry the hot dot, got:\n%s", laneSlice(hotIdx))
	}
	if strings.Contains(laneSlice(coldIdx), `class="hot"`) {
		t.Errorf("sess-cold has no live pin and must not carry the hot dot, got:\n%s", laneSlice(coldIdx))
	}
}
