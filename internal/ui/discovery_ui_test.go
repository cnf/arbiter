package ui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/store"
)

// discoverySeed returns events in which one system block appears in five
// different sessions (so it clears the default min_sessions=5 threshold) and a
// second block appears in only one. The second is what proves the session-count
// gate is doing something, rather than the page simply listing every hash.
func discoverySeed() []store.Event {
	preamble := "You are a helpful assistant. Always follow the user's instructions exactly."
	// Relative to now, not the package's fixed timeAt(): the ledger defaults to
	// a 7-day window, so a fixed instant would fall outside it as soon as the
	// clock moved past that date and the test would start passing vacuously.
	now := time.Now().UTC().Add(-10 * time.Minute)
	var events []store.Event
	for s := 0; s < 5; s++ {
		for turn := 0; turn < 2; turn++ {
			events = append(events, store.Event{
				TraceID:    fmt.Sprintf("t-%d-%d", s, turn),
				SessionKey: fmt.Sprintf("sess-%d", s),
				Provider:   "anthropic",
				Model:      "claude-opus-4-6",
				StatusCode: 200,
				Ts:         now.Add(-time.Duration(s*2+turn) * time.Minute),
				Content: &store.CapturedContent{
					Request: []store.Block{
						{Kind: "text", Body: []byte(preamble), Role: "system", MsgIndex: 0, Position: 0},
						{Kind: "text", Body: []byte(fmt.Sprintf("unique turn %d", turn)), Role: "user", MsgIndex: 1, Position: 0},
					},
				},
			})
		}
	}
	// Repeated twice inside ONE session: enough requests to clear the
	// min_requests floor, too few sessions to clear the default gate. That
	// combination is exactly what the session-count signal is for.
	for turn := 0; turn < 2; turn++ {
		events = append(events, store.Event{
			TraceID:    fmt.Sprintf("solo-%d", turn),
			SessionKey: "only-one-session",
			Provider:   "anthropic",
			Model:      "claude-opus-4-6",
			StatusCode: 200,
			Ts:         now.Add(-time.Duration(turn) * time.Minute),
			Content: &store.CapturedContent{
				Request: []store.Block{
					{Kind: "text", Body: []byte("a block seen in exactly one session"), Role: "system", MsgIndex: 0, Position: 0},
				},
			},
		})
	}
	return events
}

// The ledger's default view must list a block repeated across sessions and must
// NOT list one confined to a single session — that gate is the whole point of
// the page (per the ticket, request-count sorting is useless and session-count
// is the signal).
func TestDiscoveryGatesOnSessionCount(t *testing.T) {
	h, _ := newSeededHandler(t, discoverySeed()...)

	body := serve(t, h, "GET", "/admin/ui/content/repeated", false).Body.String()
	if !strings.Contains(body, "Always follow the user&#39;s instructions exactly") {
		t.Errorf("the cross-session block is missing from the ledger; body = %s", firstLine(body))
	}
	if strings.Contains(body, "seen in exactly one session") {
		t.Error("a block confined to one session was listed at the default min_sessions=5")
	}

	// Lowering the gate must surface it — otherwise the gate is indistinguishable
	// from a block simply never being found.
	body = serve(t, h, "GET", "/admin/ui/content/repeated?min_sessions=0", false).Body.String()
	if !strings.Contains(body, "seen in exactly one session") {
		t.Error("min_sessions=0 did not surface the single-session block")
	}
}

// The nav bar's Discovery stat (Handler.base) must count unseen patterns
// gated on sessions alone (discoveryDefaultMinSessions, currently 5) —
// discoverySeed() puts three distinct blocks (the system preamble plus each
// of the two per-turn user blocks) across five sessions each, all initially
// unseen, and one block confined to a single session that never counts.
// Marking one of the three seen must drop the stat by exactly one.
func TestNavDiscoveryStatCountsUnseenPatterns(t *testing.T) {
	h, _ := newSeededHandler(t, discoverySeed()...)

	body := serve(t, h, "GET", "/admin/ui/overview", false).Body.String()
	if !strings.Contains(body, `>Discovery</span>`) || !strings.Contains(body, "3<span class=\"u\">new</span>") {
		t.Errorf("nav bar's Discovery stat should read 3 new (three cross-session unseen patterns), got:\n%s", body)
	}

	hash := contentHashOf(t, h, "Always follow the user&#39;s instructions exactly")
	if hash == "" {
		t.Fatal("could not find the block's hash on the ledger")
	}
	// Cycle the state dot once (unseen -> seen): last_seen must be at or
	// after the block's real LastSeen, or the mark is immediately
	// re-derived as unseen again (see UnseenDiscoveryCount's doc comment)
	// — a future timestamp is always safely past it.
	lastSeen := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	req := "/admin/ui/content/repeated/state?hash=" + hash + "&last_seen=" + lastSeen
	if resp := serve(t, h, "POST", req, false); resp.Code != http.StatusOK {
		t.Fatalf("mark seen: status = %d, body = %s", resp.Code, resp.Body.String())
	}

	body = serve(t, h, "GET", "/admin/ui/overview", false).Body.String()
	if !strings.Contains(body, "2<span class=\"u\">new</span>") {
		t.Errorf("nav bar's Discovery stat should read 2 new after marking one block seen, got:\n%s", body)
	}
}

// The drill-down lists the requests containing a block and, per row, offers the
// on-demand diff link that reaches GuardrailDiffHandler.
func TestBlockDrillDownLinksRequestsToTheirDiff(t *testing.T) {
	h, _ := newSeededHandler(t, discoverySeed()...)

	hash := contentHashOf(t, h, "Always follow the user&#39;s instructions exactly")
	if hash == "" {
		t.Fatal("could not find the block's hash on the ledger")
	}

	body := serve(t, h, "GET", "/admin/ui/content/block?hash="+hash, false).Body.String()
	if got := strings.Count(body, "sess-"); got < 3 {
		t.Errorf("drill-down lists %d session references, want at least 3; body = %s", got, firstLine(body))
	}
	if !strings.Contains(body, "/guardrail-diff?msg=0") {
		t.Error("no row carries a diff link to the guardrail-diff endpoint")
	}
}

// contentHashOf reads the first data-hash whose row preview contains marker.
func contentHashOf(t *testing.T, h *Handler, marker string) string {
	t.Helper()
	body := serve(t, h, "GET", "/admin/ui/content/repeated?min_sessions=0", false).Body.String()
	rows := strings.Split(body, "<tr class=\"repeated-row\" data-hash=\"")
	for _, r := range rows[1:] {
		hash := r[:strings.IndexByte(r, '"')]
		end := strings.Index(r, "</tr>")
		if end < 0 {
			end = len(r)
		}
		if strings.Contains(r[:end], marker) {
			return hash
		}
	}
	return ""
}

// A malformed or absent hash is a 400 that names the parameter — never a page
// rendered for a block that was not identified.
func TestBlockHashIsValidated(t *testing.T) {
	h, _ := newSeededHandler(t, discoverySeed()...)
	for _, tc := range []struct{ q, want string }{
		{"", "hash"},
		{"?hash=nothex", "hash"},
		{"?hash=abcd", "hash"},
	} {
		rec := serve(t, h, "GET", "/admin/ui/content/block"+tc.q, false)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("block%s = %d, want 400", tc.q, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("block%s: the 400 does not name %q", tc.q, tc.want)
		}
	}
}

// A hash that is well-formed but unknown is a normal empty page, not an error:
// the content it referred to may simply have aged out of the retention window.
func TestUnknownButValidHashRendersEmpty(t *testing.T) {
	h, _ := newSeededHandler(t, discoverySeed()...)
	unknown := strings.Repeat("ab", 32)
	rec := serve(t, h, "GET", "/admin/ui/content/block?hash="+unknown, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown hash = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No sessions found") {
		t.Error("an unknown hash did not render the empty-state message")
	}
}

// The state endpoint cycles unseen -> seen -> ignored -> unseen, and each
// response is the dot fragment carrying the new state — the swap target for the
// button that issued it.
func TestDiscoveryStateCyclesAndPersists(t *testing.T) {
	h, _ := newSeededHandler(t, discoverySeed()...)
	hash := contentHashOf(t, h, "Always follow the user&#39;s instructions exactly")
	if hash == "" {
		t.Fatal("could not find the block's hash on the ledger")
	}
	lastSeen := time.Now().UTC().Add(-9 * time.Minute).Format(time.RFC3339)
	url := "/admin/ui/content/repeated/state?hash=" + hash + "&last_seen=" + lastSeen

	for _, want := range []string{"st-seen", "st-ignored", "st-unseen"} {
		rec := serve(t, h, "POST", url, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("state POST = %d, want 200", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("state POST did not return the %s dot; body = %s", want, rec.Body.String())
		}
	}

	// After the full cycle the mark is cleared, so the ledger shows it unseen
	// again rather than keeping the third state.
	body := serve(t, h, "GET", "/admin/ui/content/repeated", false).Body.String()
	if !strings.Contains(body, "st-unseen") {
		t.Error("the ledger does not show the cycled block as unseen after a full cycle")
	}
}

// A mark survives a reload: the state is stored, not recomputed per render.
func TestDiscoveryStateSurvivesReload(t *testing.T) {
	h, _ := newSeededHandler(t, discoverySeed()...)
	hash := contentHashOf(t, h, "Always follow the user&#39;s instructions exactly")
	url := "/admin/ui/content/repeated/state?hash=" + hash + "&last_seen=" + time.Now().UTC().Add(-9*time.Minute).Format(time.RFC3339)

	if rec := serve(t, h, "POST", url, true); rec.Code != http.StatusOK {
		t.Fatalf("first state POST = %d", rec.Code)
	}
	body := serve(t, h, "GET", "/admin/ui/content/repeated", false).Body.String()
	if !strings.Contains(body, "st-seen") {
		t.Error("a seen mark did not survive into the next page render")
	}
}

// A state write demands a hash; without one there is no row to advance.
func TestDiscoveryStateRequiresHash(t *testing.T) {
	h, _ := newSeededHandler(t, discoverySeed()...)
	rec := serve(t, h, "POST", "/admin/ui/content/repeated/state", true)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("state POST with no hash = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "hash") {
		t.Error("the 400 does not name the missing parameter")
	}
}

// A malformed hash is rejected by the store, not written: a bad key would
// otherwise sit in discovery_state forever, matching nothing.
func TestDiscoveryStateRejectsBadHash(t *testing.T) {
	h, _ := newSeededHandler(t, discoverySeed()...)
	rec := serve(t, h, "POST", "/admin/ui/content/repeated/state?hash=zzzz", true)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("state POST with a bad hash = %d, want 400", rec.Code)
	}
}

// A pattern that reappears after being marked seen must display — and cycle
// from — unseen, not the stale stored "seen" mark. DiscoveryHandler already
// re-derives this per row (a stored seen mark whose MarkedAtLastSeen predates
// the block's current LastSeen is displayed as unseen again); the state-set
// handler must apply the exact same rule when deciding what state a click
// advances from, or a click on a reappeared (visibly unseen) block silently
// advances the OLD state instead — landing on ignored instead of seen.
func TestDiscoveryStateResetsOnReappearance(t *testing.T) {
	h, _ := newSeededHandler(t, discoverySeed()...)
	hash := contentHashOf(t, h, "Always follow the user&#39;s instructions exactly")
	if hash == "" {
		t.Fatal("could not find the block's hash on the ledger")
	}

	// Mark it seen as of an early last_seen.
	earlySeen := time.Now().UTC().Add(-20 * time.Minute).Format(time.RFC3339)
	rec := serve(t, h, "POST", "/admin/ui/content/repeated/state?hash="+hash+"&last_seen="+earlySeen, true)
	if !strings.Contains(rec.Body.String(), "st-seen") {
		t.Fatalf("first click did not mark it seen; body = %s", rec.Body.String())
	}

	// The pattern reappears: the page now carries a newer last_seen than the
	// mark, so it would render as unseen (mirroring DiscoveryHandler's own
	// re-derivation). A click must therefore behave like a click on an
	// unseen row — landing on seen, not on ignored.
	laterSeen := time.Now().UTC().Format(time.RFC3339)
	rec = serve(t, h, "POST", "/admin/ui/content/repeated/state?hash="+hash+"&last_seen="+laterSeen, true)
	if !strings.Contains(rec.Body.String(), "st-seen") {
		t.Errorf("clicking a reappeared (visibly-unseen) block should advance unseen -> seen, got: %s", rec.Body.String())
	}
}

// TestDiscoveryCacheServesStaleWithinTTL proves the ledger's expensive query
// result is actually cached: a block written AFTER the first page load must
// not appear on an immediate second load with the same parameters, because
// the second load is served from the discoveryCache rather than re-querying
// the store. This is the externally observable behavior a TTL cache
// produces — if this ever starts failing because the second load DOES see
// the new block, the cache has silently stopped being consulted.
func TestDiscoveryCacheServesStaleWithinTTL(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.db"
	logger := logging.NewStdoutLogger("error")

	w, err := store.NewSQLiteWriter(path, logger)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	for _, ev := range discoverySeed() {
		w.Record(ev)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer (seed): %v", err)
	}

	r, err := store.OpenReader(path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	// See newSeededHandler's comment: the ledger reads from a rollup, not a
	// live aggregation, so seeded data needs one rollup pass to become
	// visible. Deliberately not run again after the post-load write below —
	// that write staying invisible within the cache TTL is the property
	// this test proves, and it would stay invisible either way (the cache
	// sits in front of the rollup too), but running the rollup on it would
	// muddy what's actually being tested.
	if _, _, err := r.RollupContentHashStats(context.Background()); err != nil {
		t.Fatalf("rollup content hash stats: %v", err)
	}
	h := New(r, logger)

	first := serve(t, h, "GET", "/admin/ui/content/repeated", false).Body.String()
	if !strings.Contains(first, "Always follow the user&#39;s instructions exactly") {
		t.Fatalf("seeded block missing from the first load; body = %s", firstLine(first))
	}
	if strings.Contains(first, "a brand new block written after the first load") {
		t.Fatal("the new block is already present before it was ever written")
	}

	// Write a new cross-session block directly through a second writer
	// against the same file, exactly as the live process's Writer and this
	// page's Reader are two separate handles on one database.
	w2, err := store.NewSQLiteWriter(path, logger)
	if err != nil {
		t.Fatalf("open writer (post-load write): %v", err)
	}
	now := time.Now().UTC()
	for s := 0; s < 3; s++ {
		w2.Record(store.Event{
			TraceID:    fmt.Sprintf("post-load-%d", s),
			SessionKey: fmt.Sprintf("post-load-sess-%d", s),
			Provider:   "anthropic",
			Model:      "claude-opus-4-6",
			StatusCode: 200,
			Ts:         now,
			Content: &store.CapturedContent{
				Request: []store.Block{
					{Kind: "text", Body: []byte("a brand new block written after the first load"), Role: "system", MsgIndex: 0, Position: 0},
				},
			},
		})
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("close writer (post-load write): %v", err)
	}

	second := serve(t, h, "GET", "/admin/ui/content/repeated", false).Body.String()
	if strings.Contains(second, "a brand new block written after the first load") {
		t.Error("the second load (same params, well within the cache TTL) saw data written after the first load — the cache was not consulted")
	}
	if !strings.Contains(second, "Always follow the user&#39;s instructions exactly") {
		t.Error("the second load lost the originally-seeded block — the cache returned something other than the first load's result")
	}
}

// TestDiscoveryCacheKeysOnParameters proves a different min_sessions value is
// not served from the min_sessions=5 (default) cache entry — a shared key
// across different parameter combinations would silently mix results.
func TestDiscoveryCacheKeysOnParameters(t *testing.T) {
	h, _ := newSeededHandler(t, discoverySeed()...)

	// Warm the default (min_sessions=5) entry first.
	_ = serve(t, h, "GET", "/admin/ui/content/repeated", false)

	// A different min_sessions must still see the single-session block, not
	// whatever the default-params entry cached.
	body := serve(t, h, "GET", "/admin/ui/content/repeated?min_sessions=0", false).Body.String()
	if !strings.Contains(body, "seen in exactly one session") {
		t.Error("min_sessions=0 was served the min_sessions=5 cache entry instead of its own result")
	}
}

// TestDiscoveryBlockBodyServesFullText proves the workspace-mode body
// endpoint returns the full stored text, not the 200-char SQL preview the
// ledger row carries — the whole point of the fix.
func TestDiscoveryBlockBodyServesFullText(t *testing.T) {
	h, _ := newSeededHandler(t, discoverySeed()...)

	// Locate the seeded preamble's hash — the row whose preview names it,
	// not just the first row on the page (the "unique turn N" per-session
	// blocks sort ahead of it and would falsely pass a first-hash check).
	ledger := serve(t, h, "GET", "/admin/ui/content/repeated", false).Body.String()
	marker := "Always follow the user&#39;s instructions exactly"
	pos := strings.Index(ledger, marker)
	if pos < 0 {
		t.Fatal("seeded preamble block not found on the ledger")
	}
	rowStart := strings.LastIndex(ledger[:pos], "data-hash=\"")
	if rowStart < 0 {
		t.Fatal("no data-hash before the preamble marker")
	}
	hash := ledger[rowStart+len(`data-hash="`):]
	hash = hash[:strings.Index(hash, `"`)]

	rec := serve(t, h, "GET", "/admin/ui/content/block/body?hash="+hash, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("block body fragment = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Always follow the user&#39;s instructions exactly") {
		t.Errorf("block body fragment missing the full seeded text; body = %s", firstLine(rec.Body.String()))
	}
}

// TestDiscoveryBlockBodyRejectsNonFragmentRequest proves the endpoint refuses
// a plain (non-htmx) GET: it exists purely as workspace mode's on-demand
// fetch and has no full-page form.
func TestDiscoveryBlockBodyRejectsNonFragmentRequest(t *testing.T) {
	h, _ := newSeededHandler(t, discoverySeed()...)
	rec := serve(t, h, "GET", "/admin/ui/content/block/body?hash=deadbeef", false)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("non-fragment GET = %d, want 400", rec.Code)
	}
}

// TestDiscoveryBlockBodyRequiresHash mirrors TestDiscoveryStateRequiresHash:
// without a hash there is no body to fetch.
func TestDiscoveryBlockBodyRequiresHash(t *testing.T) {
	h, _ := newSeededHandler(t, discoverySeed()...)
	rec := serve(t, h, "GET", "/admin/ui/content/block/body", true)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("body fetch with no hash = %d, want 400", rec.Code)
	}
}

// TestDiscoveryBlockBodyIsNotTruncated proves workspace mode's body fragment
// is exempt from blockPreviewBytes (the 8KB cap every other body render on
// the site respects) — the user explicitly asked for "the full text,
// untruncated" in workspace mode, unlike the drill-down page which does cap.
func TestDiscoveryBlockBodyIsNotTruncated(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.db"
	logger := logging.NewStdoutLogger("error")

	w, err := store.NewSQLiteWriter(path, logger)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	// Comfortably longer than blockPreviewBytes (8KB) so a truncating
	// render would visibly cut it and a non-truncating one would not.
	long := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 400) // ~18.4KB
	for i, sess := range []string{"sess-long-a", "sess-long-b"} {
		w.Record(store.Event{
			TraceID:    "long-block-" + sess,
			SessionKey: sess,
			Provider:   "anthropic",
			Model:      "claude-opus-4-6",
			StatusCode: 200,
			Ts:         time.Now().UTC().Add(time.Duration(i) * time.Minute),
			Content: &store.CapturedContent{
				Request: []store.Block{
					{Kind: "text", Body: []byte(long), Role: "system", MsgIndex: 0, Position: 0},
				},
			},
		})
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	r, err := store.OpenReader(path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	// See newSeededHandler's comment: the ledger reads from a rollup, not a
	// live aggregation.
	if _, _, err := r.RollupContentHashStats(context.Background()); err != nil {
		t.Fatalf("rollup content hash stats: %v", err)
	}
	h := New(r, logger)

	ledger := serve(t, h, "GET", "/admin/ui/content/repeated?min_sessions=0", false).Body.String()
	idx := strings.Index(ledger, `data-hash="`)
	if idx < 0 {
		t.Fatal("no seeded row found on the ledger")
	}
	hash := ledger[idx+len(`data-hash="`):]
	hash = hash[:strings.Index(hash, `"`)]

	rec := serve(t, h, "GET", "/admin/ui/content/block/body?hash="+hash, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("block body fragment = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, long[len(long)-40:]) {
		t.Error("workspace body fragment is missing the tail of a >8KB body — it was truncated")
	}
	if strings.Contains(body, "truncated at") {
		t.Error("workspace body fragment carries a truncation notice — it should never truncate")
	}
}

// Ignored rows are hidden by default — an ignored block is boilerplate the
// operator already dismissed, so it should not keep occupying a row on
// every future visit. show_ignored is the opt-IN to seeing them again.
func TestDiscoveryHideIgnoredDropsRows(t *testing.T) {
	h, _ := newSeededHandler(t, discoverySeed()...)
	hash := contentHashOf(t, h, "Always follow the user&#39;s instructions exactly")
	if hash == "" {
		t.Fatal("could not find the block's hash on the ledger")
	}
	lastSeen := time.Now().UTC().Add(-9 * time.Minute).Format(time.RFC3339)

	// Cycle unseen -> seen -> ignored.
	base := "/admin/ui/content/repeated/state?hash=" + hash + "&last_seen=" + lastSeen
	serve(t, h, "POST", base, true)
	serve(t, h, "POST", base, true)

	hidden := serve(t, h, "GET", "/admin/ui/content/repeated", false).Body.String()
	if strings.Contains(hidden, "data-hash=\""+hash+"\"") {
		t.Fatal("ignored row is present on the default (unfiltered) ledger — ignored should be hidden by default")
	}

	shown := serve(t, h, "GET", "/admin/ui/content/repeated?show_ignored=1", false).Body.String()
	if !strings.Contains(shown, "data-hash=\""+hash+"\"") {
		t.Error("show_ignored=1 did not render the ignored row")
	}
	if !strings.Contains(shown, "checked") {
		t.Error("show ignored checkbox did not render checked when the filter is active")
	}
}

// TestDiscoveryPaginationReachesTrailingBlocks is the pagination fix itself
// (#54 follow-up: "you can NEVER get to the trailing ones... which is a
// bug"): discoverySeed() has exactly three blocks that clear the default
// min_sessions=5 gate. A limit of 2 confines the first page to a
// session-count-ranked slice — before pagination existed, the third block
// was simply unreachable in any filter combination, because the fetch limit
// truncated the ranked query itself with no offset to page past it. This
// proves offset=2 (MoreURL's own query) surfaces exactly what the first
// page could not.
func TestDiscoveryPaginationReachesTrailingBlocks(t *testing.T) {
	h, _ := newSeededHandler(t, discoverySeed()...)

	first := serve(t, h, "GET", "/admin/ui/content/repeated?limit=2", false)
	if first.Code != http.StatusOK {
		t.Fatalf("first page = %d, want 200", first.Code)
	}
	firstBody := first.Body.String()
	if got := strings.Count(firstBody, "data-hash=\""); got != 2 {
		t.Fatalf("first page rendered %d rows, want 2 (limit=2)", got)
	}
	if !strings.Contains(firstBody, "3</b> patterns") {
		t.Errorf("first page's Matching count should still read the true total (3), got: %s", firstLine(firstBody))
	}
	if !strings.Contains(firstBody, "offset=2") {
		t.Fatalf("first page has no load-more control pointing past its own limit; body = %s", firstLine(firstBody))
	}

	// Follow the load-more control exactly as the browser would: same
	// query plus offset=2, as an htmx fragment request.
	more := serve(t, h, "GET", "/admin/ui/content/repeated?limit=2&offset=2", true)
	if more.Code != http.StatusOK {
		t.Fatalf("second page = %d, want 200", more.Code)
	}
	moreBody := more.Body.String()
	if got := strings.Count(moreBody, "data-hash=\""); got != 1 {
		t.Fatalf("second page (offset=2) rendered %d rows, want 1 (the trailing block)", got)
	}
	// Exhausted: no further load-more control once every matching block
	// has been reached.
	if strings.Contains(moreBody, "load 2 more patterns") {
		t.Error("the second page still offers \"load more\" after reaching every matching block")
	}
}

// TestDiscoveryDefaultLimitIsOneHundred pins the raised first-page size
// (#54 follow-up): a first page of 100 clears most operators' real pattern
// count before pagination is ever needed, while still capping a
// pathologically large result the way the JSON endpoint's own default (50)
// always has.
func TestDiscoveryDefaultLimitIsOneHundred(t *testing.T) {
	if discoveryDefaultLimit != 100 {
		t.Errorf("discoveryDefaultLimit = %d, want 100", discoveryDefaultLimit)
	}
}
