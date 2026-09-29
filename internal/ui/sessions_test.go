package ui

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
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

	// live_only=0: these lanes carry no affinity pin, so the live-only
	// filter (on by default) would hide them before this test ever gets
	// to check the render — this test is about foldRequestLines/
	// attachTraceChildren's node classes, not the filter.
	body := serve(t, h, "GET", "/admin/ui/sessions?live_only=0", false).Body.String()

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

	// live_only=0: this session has no affinity pin, and the live-only
	// filter (on by default) would hide it before this test could check
	// the preview-note rendering, which is what it's actually about.
	body := serve(t, h, "GET", "/admin/ui/sessions?live_only=0", false).Body.String()
	if !strings.Contains(body, "content capture is off") {
		t.Errorf("expected the capture-off preview note, got:\n%s", body)
	}
}

// Every lane node's "open" link (data-open-href) must point at the session's
// own transcript page — the old destination (a flat, filtered requests list)
// was removed along with the requests page (#54); a node whose href still
// pointed there would be a dead link with no way to reach the request it
// names. This covers all three node shapes: a plain client node, a
// classifier satellite nested under it, and (implicitly, via the same code
// path) a folded stack — sessionNodeHref does not special-case any of them.
func TestSessionsLaneNodeOpenHrefPointsAtTranscript(t *testing.T) {
	now := time.Now().UTC()
	mk := func(kind, traceID, sessionKey, provider string, status int, offset time.Duration) store.Event {
		return store.Event{
			TraceID: traceID, SessionKey: sessionKey, Kind: kind,
			Provider: provider, Model: "m", StatusCode: status,
			Ts: now.Add(offset), LatencyMs: 100,
		}
	}
	events := []store.Event{
		mk("classifier", "tr-1", "sess-one", "openrouter-decisions", 200, 0),
		mk("client", "tr-1", "sess-one", "anthropic", 200, 1*time.Second),
	}
	h, _ := newSeededHandler(t, events...)

	body := serve(t, h, "GET", "/admin/ui/sessions?live_only=0", false).Body.String()

	if strings.Contains(body, "/admin/ui/requests") {
		t.Errorf("a lane node still links to the removed requests page:\n%s", body)
	}
	if n := strings.Count(body, "data-open-href=\"/admin/ui/session?"); n != 2 {
		t.Errorf("got %d nodes linking to /admin/ui/session, want 2 (the client node and its satellite)\n%s", n, body)
	}
	if !strings.Contains(body, "key=sess-one") {
		t.Errorf("a node's open href does not carry the session key:\n%s", body)
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

	// live_only=0 on every request here: this test is about the
	// errors-only radio, and neither seeded session has an affinity pin,
	// so the live-only filter (on by default) would hide both regardless
	// of the errors filter under test.
	// Default (no ?errors param at all): both lanes present.
	body := serve(t, h, "GET", "/admin/ui/sessions?live_only=0", false).Body.String()
	if !strings.Contains(body, "sess-ok") || !strings.Contains(body, "sess-bad") {
		t.Fatalf("default view should show both sessions, got:\n%s", body)
	}

	// "errors only" (?errors=1): only the errored lane.
	body = serve(t, h, "GET", "/admin/ui/sessions?errors=1&live_only=0", false).Body.String()
	if strings.Contains(body, "sess-ok") {
		t.Errorf("errors=1 should hide the clean session, got:\n%s", body)
	}
	if !strings.Contains(body, "sess-bad") {
		t.Errorf("errors=1 should still show the errored session, got:\n%s", body)
	}

	// "all" (?errors=, empty value — what the toolbar's own radio submits):
	// must behave identically to no param at all, showing both lanes again.
	body = serve(t, h, "GET", "/admin/ui/sessions?errors=&live_only=0", false).Body.String()
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
	if !strings.Contains(body, `<span class="stat" data-nav-stat="sessions">1<span class="u">active</span></span>`) {
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

	// live_only=0: this test is about the hot dot, not the live-only
	// filter, and sess-cold (no pin at all) would otherwise be dropped by
	// the filter's new default-on behavior before it ever got a chance to
	// prove the dot is absent.
	body := serve(t, h, "GET", "/admin/ui/sessions?live_only=0", false).Body.String()

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

// The lane header's relative "started 12m ago" text must stay inside its own
// .ago element, in BOTH response shapes the lane is rendered through.
//
// This is a coupling between the template and laneLive.js, and it fails
// silently in the dangerous direction: stableHTML() blanks any .ago node's
// text before comparing a polled lane against the one on screen, so that a
// relative clock ticking ("0s ago" → "1m ago") cannot make an unchanged lane
// look changed. If the span is dropped or renamed, the clock's text is
// compared instead — every session younger than an hour then reads as changed
// on every poll, its lane gets replaced, and the spacing fit laneTimeline.js
// applied is discarded with it. Nothing errors; the dense-lane fit and any
// node selection just stop surviving a live update.
//
// Both the page set and the partials set are asserted: the page renders the
// lane on load, renderLaneRow renders it for the live tail, and the tail is
// where the comparison actually happens.
func TestSessionsLaneAgoTextIsIsolatedForTheLiveComparison(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	logger := logging.NewStdoutLogger("error")

	w, err := store.NewSQLiteWriter(path, logger)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	now := time.Now().UTC()
	// Deliberately a session started minutes ago, not hours: at this age the
	// relative text changes every minute, which is exactly when the
	// comparison would break if the span were missing.
	started := now.Add(-12 * time.Minute)
	for i, ts := range []time.Time{started, started.Add(time.Minute)} {
		w.Record(store.Event{TraceID: "t" + strconv.Itoa(i), SessionKey: "sess-fresh",
			Kind: "client", Provider: "p", Model: "m", StatusCode: 200, Ts: ts, LatencyMs: 1})
	}
	// The tail deliberately refreshes only sessions holding a live affinity
	// pin (see SessionsTailHandler), so the fixture needs one or the tail
	// would return an empty lane list and prove nothing.
	if err := w.SavePin(context.Background(), store.AffinityPin{
		SessionKey: "sess-fresh", RequestedModel: "auto", Provider: "p", Model: "m",
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

	page := serve(t, h, "GET", "/admin/ui/sessions?live_only=0", false).Body.String()
	if !strings.Contains(page, `started <span class="ago">`) {
		t.Errorf("the page's lane header no longer isolates the relative time in a .ago span; "+
			"laneLive.js's stableHTML relies on that class to ignore the ticking clock:\n%s", page)
	}

	// The tail path is where the comparison runs, so assert the same shape
	// through the JSON the poller actually receives. serveJSON registers only
	// the tail route — which is exactly this endpoint.
	code, tail := serveJSON(t, h, "/admin/ui/sessions/tail?live_only=0")
	if code != 200 {
		t.Fatalf("tail returned %d, want 200: %s", code, tail)
	}
	laneHTML := extractLaneHTML(t, tail, "sess-fresh")
	if !strings.Contains(laneHTML, `started <span class="ago">`) {
		t.Errorf("the tail's lane markup no longer isolates the relative time in a .ago span:\n%s", laneHTML)
	}
}

// extractLaneHTML pulls one lane's rendered markup out of a tail response's
// JSON, so a test can assert on the exact bytes a poll would splice into the
// page rather than on the page's own render.
func extractLaneHTML(t *testing.T, body, key string) string {
	t.Helper()
	var resp struct {
		Lanes []struct {
			Key  string `json:"key"`
			HTML string `json:"html"`
		} `json:"lanes"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("tail response is not the expected JSON: %v\n%s", err, body)
	}
	for _, l := range resp.Lanes {
		if l.Key == key {
			return l.HTML
		}
	}
	t.Fatalf("tail response carried no lane for %q:\n%s", key, body)
	return ""
}

// TestSessionsLaneRowCarriesItsOwnCountsAndSortKey pins the contract between
// laneRow.html and laneLive.js that makes live lane INSERTION work.
//
// The poller reconciles the lane list against a payload of freshly rendered
// lanes, and nothing else travels with them: it has to place a lane the page
// is not showing yet (so it needs the same MAX(ts) the server orders by), and
// it has to keep the toolbar caption honest when the set of lanes changes (so
// it needs the same per-lane turn count the server sums into InViewRequests).
// Both facts therefore have to be ON the element, and both are easy to drop
// silently: the obvious "the header already shows 'N requests'" is
// human-formatted text that cannot be summed, and the obvious "the header
// shows '12m ago'" is not a comparable timestamp.
//
// This asserts the attributes on BOTH render paths, because they must not
// drift: a page-loaded lane and a polled lane are spliced together in the same
// list, and the caption sums whichever of them is on screen.
func TestSessionsLaneRowCarriesItsOwnCountsAndSortKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	logger := logging.NewStdoutLogger("error")

	w, err := store.NewSQLiteWriter(path, logger)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	now := time.Now().UTC()
	for i, ts := range []time.Time{now.Add(-30 * time.Minute), now.Add(-29 * time.Minute), now.Add(-28 * time.Minute)} {
		w.Record(store.Event{TraceID: "t" + strconv.Itoa(i), SessionKey: "sess-attrs",
			Kind: "client", Provider: "p", Model: "m", StatusCode: 200, Ts: ts, LatencyMs: 1})
	}
	if err := w.SavePin(context.Background(), store.AffinityPin{
		SessionKey: "sess-attrs", RequestedModel: "auto", Provider: "p", Model: "m",
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

	// The page's own render.
	page := serve(t, h, "GET", "/admin/ui/sessions?live_only=0", false).Body.String()
	laneOpen := laneRowOpenTag(t, page, "sess-attrs")
	for _, want := range []struct{ attr, why string }{
		{`data-turns="3"`, "the caption sums data-turns to reproduce InViewRequests"},
		{`data-last-seen="`, "insertInOrder places a new lane by data-last-seen"},
	} {
		if !strings.Contains(laneOpen, want.attr) {
			t.Errorf("the page's lane row is missing %s — %s:\n%s", want.attr, want.why, laneOpen)
		}
	}

	// The tail's render — the bytes the poller actually splices in.
	code, tail := serveJSON(t, h, "/admin/ui/sessions/tail?live_only=0")
	if code != 200 {
		t.Fatalf("tail returned %d, want 200: %s", code, tail)
	}
	tailLane := laneRowOpenTag(t, extractLaneHTML(t, tail, "sess-attrs"), "sess-attrs")
	if tailLane != laneOpen {
		t.Errorf("the polled lane's opening tag differs from the page's, so the two render\npaths have drifted:\n  page: %s\n  tail: %s", laneOpen, tailLane)
	}

	// data-last-seen has to be a fixed-format RFC3339 UTC timestamp: the
	// client's placement is a plain string compare, which is only
	// chronological for exactly that shape (see store/reader.go's formatTime).
	if !regexp.MustCompile(`data-last-seen="\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z"`).MatchString(laneOpen) {
		t.Errorf("data-last-seen is not an RFC3339 UTC timestamp, so laneLive.js's "+
			"lexicographic ordering would not match the server's ORDER BY MAX(ts) DESC:\n%s", laneOpen)
	}
}

// laneRowOpenTag returns the .lane-row opening tag for one session — the whole
// element is unnecessary here and enormous (a dense lane embeds every node's
// facts), so the attributes are read from the tag alone.
func laneRowOpenTag(t *testing.T, html, key string) string {
	t.Helper()
	marker := `class="lane-row" data-session="` + key + `"`
	i := strings.Index(html, marker)
	if i < 0 {
		t.Fatalf("no .lane-row for %q in:\n%.2000s", key, html)
	}
	start := strings.LastIndex(html[:i], "<div")
	end := strings.Index(html[i:], ">")
	if start < 0 || end < 0 {
		t.Fatalf("could not delimit the lane row's opening tag near %q", marker)
	}
	return html[start : i+end+1]
}

// "live only" (default on, per its own doc comment on sessionsView.LiveOnly)
// must show a lane with a live pin, keep showing a lane whose pin expired
// recently enough to be inside liveOnlyGraceWindow, and drop a lane whose
// pin expired before that window (or that never had one) — then explicit
// live_only=0 must bring everything back, including the one with no pin at
// all. This is the filter itself, not the hot dot (TestSessionsLaneHotDotReflectsActivePin
// covers that instead — the dot and the filter share a data source but ask
// two different questions about it).
func TestSessionsLiveOnlyFilter(t *testing.T) {
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
	w.Record(store.Event{TraceID: "t2", SessionKey: "sess-lingering", Kind: "client",
		Provider: "p", Model: "m", StatusCode: 200, Ts: now.Add(time.Second), LatencyMs: 1})
	w.Record(store.Event{TraceID: "t3", SessionKey: "sess-longgone", Kind: "client",
		Provider: "p", Model: "m", StatusCode: 200, Ts: now.Add(2 * time.Second), LatencyMs: 1})
	w.Record(store.Event{TraceID: "t4", SessionKey: "sess-nopin", Kind: "client",
		Provider: "p", Model: "m", StatusCode: 200, Ts: now.Add(3 * time.Second), LatencyMs: 1})

	if err := w.SavePin(context.Background(), store.AffinityPin{
		SessionKey: "sess-live", RequestedModel: "auto", Provider: "p", Model: "m",
		ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("SavePin sess-live: %v", err)
	}
	if err := w.SavePin(context.Background(), store.AffinityPin{
		SessionKey: "sess-lingering", RequestedModel: "auto", Provider: "p", Model: "m",
		ExpiresAt: now.Add(-liveOnlyGraceWindow / 2),
	}); err != nil {
		t.Fatalf("SavePin sess-lingering: %v", err)
	}
	if err := w.SavePin(context.Background(), store.AffinityPin{
		SessionKey: "sess-longgone", RequestedModel: "auto", Provider: "p", Model: "m",
		ExpiresAt: now.Add(-liveOnlyGraceWindow * 2),
	}); err != nil {
		t.Fatalf("SavePin sess-longgone: %v", err)
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

	// Default (no live_only param at all): on, per LiveOnly's own default.
	body := serve(t, h, "GET", "/admin/ui/sessions", false).Body.String()
	if !strings.Contains(body, "sess-live") {
		t.Errorf("default (live-only on) should show a lane with a live pin, got:\n%s", body)
	}
	if !strings.Contains(body, "sess-lingering") {
		t.Errorf("default (live-only on) should still show a lane inside its grace window, got:\n%s", body)
	}
	if strings.Contains(body, "sess-longgone") {
		t.Errorf("default (live-only on) must hide a lane whose pin expired past the grace window, got:\n%s", body)
	}
	if strings.Contains(body, "sess-nopin") {
		t.Errorf("default (live-only on) must hide a lane with no pin at all, got:\n%s", body)
	}

	// Explicit live_only=0: everything, including the never-pinned lane.
	body = serve(t, h, "GET", "/admin/ui/sessions?live_only=0", false).Body.String()
	for _, want := range []string{"sess-live", "sess-lingering", "sess-longgone", "sess-nopin"} {
		if !strings.Contains(body, want) {
			t.Errorf("live_only=0 should show every lane regardless of pin state, missing %q in:\n%s", want, body)
		}
	}
}
