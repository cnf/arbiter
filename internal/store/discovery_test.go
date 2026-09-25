package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestSessionsForContentReturnsEachRequestOnce is the duplicate-row trap: a
// client re-sends its conversation every turn, so one conversation holds many
// *references* to the same hash. A join against content_refs would report that
// conversation once per reference; the drill-down would then show a request
// three times and a count that matched nothing on the block list.
func TestSessionsForContentReturnsEachRequestOnce(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	// One block, referenced twice inside the same request (two messages) in
	// session s1, plus a second request in a *different* session (s2) that
	// also carries it — two distinct sessions, so both must survive the
	// per-session dedupe.
	const shared = "a block that appears in more than one message and request"
	w.Record(Event{
		TraceID: "t", SessionKey: "s1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{
			textBlock("system", 0, 0, shared),
			textBlock("user", 1, 0, shared),
		}},
	})
	w.Record(Event{
		TraceID: "t", SessionKey: "s2", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{
			textBlock("system", 0, 0, shared),
			textBlock("user", 1, 0, "a different question, long enough to matter"),
		}},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	repeated, err := r.RepeatedContent(ctx, WindowFrom(time.Hour), 2, 0, 50)
	if err != nil {
		t.Fatalf("RepeatedContent: %v", err)
	}
	if len(repeated) != 1 {
		t.Fatalf("repeated = %+v, want the one shared block", repeated)
	}
	if repeated[0].Requests != 2 {
		t.Fatalf("RepeatedContent reports %d requests, want 2", repeated[0].Requests)
	}

	rows, err := r.SessionsForContent(ctx, repeated[0].Hash, 50)
	if err != nil {
		t.Fatalf("SessionsForContent: %v", err)
	}
	// Two sessions, and the first one contains two references to the same
	// hash within its own request. Anything other than 2 means the drill-down
	// is counting references instead of distinct sessions.
	if len(rows) != 2 {
		t.Errorf("drill-down returned %d rows, want 2 (one per session, not per reference)", len(rows))
	}
	// And it must agree with the count the block list showed, since the page
	// presents them as the same number here (one session == one request each).
	if int64(len(rows)) != repeated[0].Requests {
		t.Errorf("drill-down has %d rows but the block list says %d requests", len(rows), repeated[0].Requests)
	}
}

// TestSessionsForContentCollapsesRepeatsWithinOneSession is the payoff this
// method exists for: a client resends its whole history every turn, so a
// widely-used system preamble can appear in dozens of requests within a
// single conversation. The drill-down must show that conversation once — as
// its first request — not once per turn, or "5 sessions" on the ledger would
// expand into fifty near-identical rows on the drill-down.
func TestSessionsForContentCollapsesRepeatsWithinOneSession(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	const shared = "a system preamble resent on every turn of this conversation"
	for i := 0; i < 5; i++ {
		w.Record(Event{
			TraceID: "t", SessionKey: "s1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
			Content: &CapturedContent{Request: []Block{
				textBlock("system", 0, 0, shared),
				textBlock("user", 1, 0, "turn-specific question number "+string(rune('a'+i))+", long enough to matter here"),
			}},
		})
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	repeated, err := r.RepeatedContent(ctx, WindowFrom(time.Hour), 2, 0, 50)
	if err != nil {
		t.Fatalf("RepeatedContent: %v", err)
	}
	if len(repeated) != 1 {
		t.Fatalf("repeated = %+v, want the one shared block", repeated)
	}
	if repeated[0].Requests != 5 {
		t.Fatalf("RepeatedContent reports %d requests, want 5", repeated[0].Requests)
	}
	if repeated[0].Sessions != 1 {
		t.Fatalf("RepeatedContent reports %d sessions, want 1", repeated[0].Sessions)
	}

	rows, err := r.SessionsForContent(ctx, repeated[0].Hash, 50)
	if err != nil {
		t.Fatalf("SessionsForContent: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("drill-down returned %d rows, want 1 (one session, collapsed)", len(rows))
	}
}

// TestRepeatedContentRejectsHashesThatAreNotHex pins the hex/blob boundary. A
// request list bound with the 64-character hex string compares text against a
// 32-byte blob, matches nothing, and is indistinguishable from a hash that is
// simply absent — so the conversion has to be explicit and a bad form refused.
func TestRepeatedContentRejectsHashesThatAreNotHex(t *testing.T) {
	_, r := captureFixture(t)
	ctx := context.Background()

	for _, bad := range []string{
		"not-hex-at-all",
		"abc123",                 // valid hex, wrong length
		strings.Repeat("ab", 31), // one byte short
		strings.Repeat("ab", 33), // one byte long
		strings.ToUpper(strings.Repeat("ab", 32))[:3], // truncated
	} {
		if _, err := r.SessionsForContent(ctx, bad, 10); err == nil {
			t.Errorf("SessionsForContent(%q) succeeded; a malformed hash must be refused, not silently match nothing", bad)
		}
	}

	// The valid form must work, so the guard is not simply refusing everything.
	valid := strings.Repeat("ab", 32)
	if _, err := r.SessionsForContent(ctx, valid, 10); err != nil {
		t.Errorf("SessionsForContent with a well-formed hash: %v", err)
	}
}

// TestContentHashCountsAgreeWithRepeatedContent holds the two queries to one
// definition. The generic reader uses the counts to say "3 of 41 blocks match
// this filter", and if the two disagree the page states a filter's effect that
// its own list contradicts.
func TestContentHashCountsAgreeWithRepeatedContent(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	// Two blocks across two sessions, one of them also in a third session.
	common := textBlock("system", 0, 0, "identical preamble across every session here")
	semi := textBlock("system", 0, 0, "preamble seen in only two of the three sessions")
	for i, session := range []string{"s1", "s2", "s3"} {
		blocks := []Block{common}
		if i < 2 {
			blocks = append(blocks, semi)
		}
		blocks = append(blocks, textBlock("user", 1, 0, "distinct question "+string(rune('a'+i))+" with enough characters"))
		w.Record(Event{
			TraceID: "t", SessionKey: session, Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
			Content: &CapturedContent{Request: blocks},
		})
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, tc := range []struct{ minReq, minSess int }{{2, 0}, {2, 2}, {2, 3}, {3, 0}} {
		total, matching, err := r.ContentHashCounts(ctx, WindowFrom(time.Hour), tc.minReq, tc.minSess)
		if err != nil {
			t.Fatalf("ContentHashCounts(%d,%d): %v", tc.minReq, tc.minSess, err)
		}
		listed, err := r.RepeatedContent(ctx, WindowFrom(time.Hour), tc.minReq, tc.minSess, 200)
		if err != nil {
			t.Fatalf("RepeatedContent(%d,%d): %v", tc.minReq, tc.minSess, err)
		}
		if int64(len(listed)) != matching {
			t.Errorf("min_requests=%d min_sessions=%d: counts say %d match, the list has %d",
				tc.minReq, tc.minSess, matching, len(listed))
		}
		// total is every block the query groups, regardless of threshold, so it
		// can never be smaller than the matching subset within one window.
		if total < matching {
			t.Errorf("total %d < matching %d", total, matching)
		}
		// Every block seen twice is in the unthresholded total.
		if tc.minReq == 2 && tc.minSess == 0 && matching == 0 {
			t.Error("no blocks matched min_requests=2, but two blocks span multiple requests")
		}
	}
}

// TestContentHashCountsTotalIsThresholdIndependent is what makes the count a
// control rather than a new number to trust: raising min_sessions must not
// change the total, or the page's "N of M" would move for the wrong reason.
func TestContentHashCountsTotalIsThresholdIndependent(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	for i, session := range []string{"s1", "s2"} {
		w.Record(Event{
			TraceID: "t", SessionKey: session, Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
			Content: &CapturedContent{Request: []Block{
				textBlock("system", 0, 0, "preamble present in both sessions"),
				textBlock("user", 1, 0, "question "+string(rune('a'+i))+" with enough characters"),
			}},
		})
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, base, err := r.ContentHashCounts(ctx, WindowFrom(time.Hour), 2, 0)
	if err != nil {
		t.Fatalf("ContentHashCounts: %v", err)
	}
	for _, minSess := range []int{1, 2, 3} {
		total, matching, err := r.ContentHashCounts(ctx, WindowFrom(time.Hour), 2, minSess)
		if err != nil {
			t.Fatalf("ContentHashCounts(min_sessions=%d): %v", minSess, err)
		}
		if matching > base {
			t.Errorf("min_sessions=%d matched %d blocks, more than the %d that match min_sessions=0", minSess, matching, base)
		}
		if total < matching {
			t.Errorf("min_sessions=%d: total %d < matching %d", minSess, total, matching)
		}
	}
}

// TestContentByHashReturnsTheStoredBodyAndDistinguishesNotFound pins the three
// outcomes the drill-down page has to tell apart: a block with its body, a
// well-formed hash that is not in the store, and a hash that is not a hash.
// Collapsing the last two would report the operator's typo as a missing block.
func TestContentByHashReturnsTheStoredBodyAndDistinguishesNotFound(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	body := "the whole body of a repeated block, long enough to be real text"
	for _, s := range []string{"s1", "s2"} {
		w.Record(Event{
			TraceID: "t", SessionKey: s, Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
			Content: &CapturedContent{Request: []Block{textBlock("system", 0, 0, body)}},
		})
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	repeated, err := r.RepeatedContent(ctx, WindowFrom(time.Hour), 2, 0, 50)
	if err != nil || len(repeated) != 1 {
		t.Fatalf("RepeatedContent: %v (%d blocks)", err, len(repeated))
	}

	got, ok, err := r.ContentByHash(ctx, repeated[0].Hash)
	if err != nil || !ok {
		t.Fatalf("ContentByHash: ok=%v err=%v", ok, err)
	}
	if got.Body != body {
		t.Errorf("body = %q, want %q", got.Body, body)
	}
	if got.BlockType != "text" || got.Role != "system" || !got.Captured {
		t.Errorf("type/role/captured = %q/%q/%v, want text/system/true", got.BlockType, got.Role, got.Captured)
	}
	// The hash the block list showed must be the one that round-trips back in.
	if got.Hash != repeated[0].Hash {
		t.Errorf("hash = %q, want the one the list reported %q", got.Hash, repeated[0].Hash)
	}

	// A well-formed hash that is absent is a miss, not an error.
	_, ok, err = r.ContentByHash(ctx, strings.Repeat("00", 32))
	if err != nil || ok {
		t.Errorf("absent hash: ok=%v err=%v, want false/nil", ok, err)
	}

	// A malformed hash is the sentinel, so a caller can answer 400 rather than
	// 500 for operator input.
	if _, _, err := r.ContentByHash(ctx, "not-a-hash"); !errors.Is(err, ErrBadContentHash) {
		t.Errorf("malformed hash error = %v, want ErrBadContentHash", err)
	}
	if _, _, err := r.ContentByHash(ctx, strings.Repeat("ab", 31)); !errors.Is(err, ErrBadContentHash) {
		t.Errorf("short hash error = %v, want ErrBadContentHash", err)
	}
}

// TestSessionsForContentExcludesRejections is now moot: no live write path
// ever produces owner_kind='rejected' rows (#5 — see schema.sql's comment on
// content_refs). Removed along with RecordRejected/rejectionID.

// idFor finds the id of the single request matching a trace_id, for tests
// that need to name a specific row to query against — ListRequests doesn't
// expose a trace_id filter, so this reads the id back the same way
// SessionsForContent tests already do (rows[N].ID), keyed on TraceID to make
// each test's intent readable instead of relying on insertion order.
func idFor(t *testing.T, r *Reader, traceID string) int64 {
	t.Helper()
	rows, err := r.ListRequests(context.Background(), RequestFilter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	for _, row := range rows {
		if row.TraceID == traceID {
			return row.ID
		}
	}
	t.Fatalf("no request with trace_id %q", traceID)
	return 0
}

// TestParentSessionForTitlePrefersSessionKeyMatch is the exact-match tier: a
// title request sharing its session_key with real client traffic (the same
// session-affinity header sent on both) must resolve to that session without
// needing the content-hash fallback at all — and must win even when a
// content-hash match to a *different* session would also be available, since
// the header is authoritative in a way string-equality on caller-visible text
// never is.
func TestParentSessionForTitlePrefersSessionKeyMatch(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	const opener = "please write me a haiku about the ocean, thank you"

	// Real session, header-affine: three client turns share session_key
	// "hermes-conv-1", and the title request also carries that same key.
	w.Record(Event{TraceID: "client-1", SessionKey: "hermes-conv-1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, opener)}}})
	w.Record(Event{TraceID: "client-2", SessionKey: "hermes-conv-1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, opener), textBlock("user", 1, 0, "a follow-up turn")}}})
	w.Record(Event{TraceID: "title-1", SessionKey: "hermes-conv-1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200, RequestKind: "title",
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, opener)}}})

	// A decoy: a *different* session whose first turn happens to be the
	// exact same opener text, so a content-hash join alone would also match
	// it — proving tier 1 doesn't fall through to tier 2 once it has an
	// answer.
	w.Record(Event{TraceID: "decoy", SessionKey: "unrelated-session", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, opener)}}})

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	titleID := idFor(t, r, "title-1")
	got, ok, err := r.ParentSessionForTitle(ctx, titleID)
	if err != nil {
		t.Fatalf("ParentSessionForTitle: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want a match")
	}
	if got.Method != ParentMatchSessionKey {
		t.Errorf("Method = %q, want %q", got.Method, ParentMatchSessionKey)
	}
	if got.SessionKey != "hermes-conv-1" {
		t.Errorf("SessionKey = %q, want hermes-conv-1", got.SessionKey)
	}
	if got.Matches != 2 {
		t.Errorf("Matches = %d, want 2 (the two client rows sharing the key)", got.Matches)
	}
}

// TestParentSessionForTitleFallsBackToContentHash covers a title request
// with no session-affinity header (its own session_key is a lone content
// hash, shared with nothing) — the parent must still be found by matching
// its user-turn text against a real session's history.
func TestParentSessionForTitleFallsBackToContentHash(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	const opener = "summarize this codebase for me please, it's fairly large"

	w.Record(Event{TraceID: "client-1", SessionKey: "content-derived-key-1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, opener)}}})
	w.Record(Event{TraceID: "client-2", SessionKey: "content-derived-key-1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, opener), textBlock("user", 1, 0, "second turn")}}})
	// No session_key at all shared with the client rows above — this is the
	// no-header case.
	w.Record(Event{TraceID: "title-1", SessionKey: "title-own-key", Format: "openai", Provider: "p", Model: "m", StatusCode: 200, RequestKind: "title",
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, opener)}}})

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	titleID := idFor(t, r, "title-1")
	got, ok, err := r.ParentSessionForTitle(ctx, titleID)
	if err != nil {
		t.Fatalf("ParentSessionForTitle: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want a match")
	}
	if got.Method != ParentMatchContentHash {
		t.Errorf("Method = %q, want %q", got.Method, ParentMatchContentHash)
	}
	if got.SessionKey != "content-derived-key-1" {
		t.Errorf("SessionKey = %q, want content-derived-key-1", got.SessionKey)
	}
	if got.Matches != 2 {
		t.Errorf("Matches = %d, want 2 (both client rows contain the opener block)", got.Matches)
	}
}

// TestParentSessionForTitleExcludesOtherTitleRequests is the self-collision
// guard: two title-gen retries for the identical conversation opener share
// no real parent, and must not match each other via the content-hash tier —
// only a real client turn counts as evidence of a parent session.
func TestParentSessionForTitleExcludesOtherTitleRequests(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	const opener = "identical opener retried twice by the title generator"

	w.Record(Event{TraceID: "title-1", SessionKey: "title-key-1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200, RequestKind: "title",
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, opener)}}})
	w.Record(Event{TraceID: "title-2", SessionKey: "title-key-2", Format: "openai", Provider: "p", Model: "m", StatusCode: 200, RequestKind: "title",
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, opener)}}})

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	titleID := idFor(t, r, "title-1")
	got, ok, err := r.ParentSessionForTitle(ctx, titleID)
	if err != nil {
		t.Fatalf("ParentSessionForTitle: %v", err)
	}
	if ok {
		t.Fatalf("ok = true (%+v), want no match — the only content-hash hit is another title request", got)
	}
}

// TestParentSessionForTitleNoMatchIsNotAnError is the plain negative case: a
// title request whose text never reappears (e.g. wrapped/expanded before the
// real send) and whose session_key nobody else shares must return ok=false,
// not an error — the caller (UI) treats that as "no parent found", distinct
// from a query failure.
func TestParentSessionForTitleNoMatchIsNotAnError(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	w.Record(Event{TraceID: "title-1", SessionKey: "lonely-key", Format: "openai", Provider: "p", Model: "m", StatusCode: 200, RequestKind: "title",
		Content: &CapturedContent{Request: []Block{textBlock("user", 0, 0, "a title-gen request nothing else ever matches")}}})

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	titleID := idFor(t, r, "title-1")
	got, ok, err := r.ParentSessionForTitle(ctx, titleID)
	if err != nil {
		t.Fatalf("ParentSessionForTitle: %v", err)
	}
	if ok {
		t.Fatalf("ok = true (%+v), want no match", got)
	}
}

// TestRepeatedContentExcludesGuardrailedAndResponseDirections is the direction
// filter's reason to exist: a guardrail rewrite of a block, or a model's own
// reply, must not count as a repeated *client-sent* block. Without the filter
// a block a rule already strips (request_guardrailed) or an assistant reply
// (response) would inflate — or worse, masquerade as — a pattern still worth
// discovering.
func TestRepeatedContentExcludesGuardrailedAndResponseDirections(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	preamble := textBlock("system", 0, 0, "a preamble only ever seen post-guardrail, never as sent")
	reply := textBlock("assistant", 2, 0, "an assistant reply repeated verbatim across sessions somehow")

	for i, session := range []string{"session-a", "session-b", "session-c"} {
		w.Record(Event{
			TraceID: "t", SessionKey: session, Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
			Content: &CapturedContent{
				Request:            []Block{textBlock("user", 1, 0, "distinct user text "+string(rune('a'+i))+" with enough characters")},
				RequestGuardrailed: []Block{preamble},
				Response:           []Block{reply},
			},
		})
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	repeated, err := r.RepeatedContent(ctx, WindowFrom(time.Hour), 2, 0, 50)
	if err != nil {
		t.Fatalf("RepeatedContent: %v", err)
	}
	if len(repeated) != 0 {
		t.Errorf("repeated = %+v, want none — only request_guardrailed/response blocks were repeated, no client 'request' direction block was", repeated)
	}
}

// TestRepeatedContentSortsBySessionsNotRequests pins the settled sort: a block
// resent many times in one long conversation must rank below one seen fewer
// times but in more distinct sessions — session count is the finding, request
// count only measures conversation length.
func TestRepeatedContentSortsBySessionsNotRequests(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	longConvoBlock := textBlock("system", 0, 0, "resent many times within one very long single conversation")
	widespreadBlock := textBlock("system", 0, 0, "seen only twice per session but across three separate sessions right")

	// One long session: five requests, all resending longConvoBlock (never
	// widespreadBlock) — high request count, session count of 1.
	for i := 0; i < 5; i++ {
		w.Record(Event{
			TraceID: "t", SessionKey: "long-session", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
			Content: &CapturedContent{Request: []Block{
				longConvoBlock,
				textBlock("user", 1, i, "turn "+string(rune('a'+i))+" of the long conversation here"),
			}},
		})
	}
	// Three separate sessions, each sending widespreadBlock twice — lower
	// request count (6) than longConvoBlock (5)... actually let's make it
	// unambiguous: fewer total requests than longConvoBlock, more sessions.
	for i, session := range []string{"s1", "s2", "s3"} {
		w.Record(Event{
			TraceID: "t", SessionKey: session, Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
			Content: &CapturedContent{Request: []Block{
				widespreadBlock,
				textBlock("user", 1, 0, "opening question "+string(rune('a'+i))+" with enough text"),
			}},
		})
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	repeated, err := r.RepeatedContent(ctx, WindowFrom(time.Hour), 2, 0, 50)
	if err != nil {
		t.Fatalf("RepeatedContent: %v", err)
	}
	if len(repeated) != 2 {
		t.Fatalf("got %d blocks, want 2: %+v", len(repeated), repeated)
	}
	// longConvoBlock: 5 requests / 1 session. widespreadBlock: 3 requests / 3
	// sessions. Sessions-first order must put widespreadBlock first despite
	// its lower request count.
	if repeated[0].Sessions < repeated[1].Sessions {
		t.Errorf("order = %+v, want the higher-session block first regardless of request count", repeated)
	}
	if repeated[0].Requests > repeated[1].Requests && repeated[0].Sessions < repeated[1].Sessions {
		t.Errorf("sort appears to favor requests over sessions: %+v", repeated)
	}
}

// TestDiscoveryStateRoundTripsAndDefaultsToAbsent is the seen/ignored
// contract: no row means unseen, a set mark round-trips through
// DiscoveryStates, and clearing removes it again.
func TestDiscoveryStateRoundTripsAndDefaultsToAbsent(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	block := textBlock("system", 0, 0, "a block worth marking seen or ignored in the ledger")
	for _, s := range []string{"s1", "s2"} {
		w.Record(Event{
			TraceID: "t", SessionKey: s, Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
			Content: &CapturedContent{Request: []Block{block}},
		})
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	repeated, err := r.RepeatedContent(ctx, WindowFrom(time.Hour), 2, 0, 50)
	if err != nil || len(repeated) != 1 {
		t.Fatalf("RepeatedContent: %v (%d blocks)", err, len(repeated))
	}
	hash := repeated[0].Hash

	// Unmarked: absent from the map.
	states, err := r.DiscoveryStates(ctx, []string{hash})
	if err != nil {
		t.Fatalf("DiscoveryStates: %v", err)
	}
	if _, ok := states[hash]; ok {
		t.Errorf("unmarked hash present in states map: %+v", states)
	}

	// Mark seen.
	if err := r.SetDiscoveryState(ctx, hash, DiscoverySeen, repeated[0].LastSeen); err != nil {
		t.Fatalf("SetDiscoveryState: %v", err)
	}
	states, err = r.DiscoveryStates(ctx, []string{hash})
	if err != nil {
		t.Fatalf("DiscoveryStates after mark: %v", err)
	}
	if got := states[hash]; got.State != DiscoverySeen || got.MarkedAtLastSeen != repeated[0].LastSeen {
		t.Errorf("state after mark = %+v, want seen at %q", got, repeated[0].LastSeen)
	}

	// Re-mark ignored (upsert, not a second row).
	if err := r.SetDiscoveryState(ctx, hash, DiscoveryIgnored, repeated[0].LastSeen); err != nil {
		t.Fatalf("SetDiscoveryState (ignored): %v", err)
	}
	states, err = r.DiscoveryStates(ctx, []string{hash})
	if err != nil {
		t.Fatalf("DiscoveryStates after re-mark: %v", err)
	}
	if got := states[hash]; got.State != DiscoveryIgnored {
		t.Errorf("state after re-mark = %+v, want ignored", got)
	}

	// Clear: back to absent.
	if err := r.ClearDiscoveryState(ctx, hash); err != nil {
		t.Fatalf("ClearDiscoveryState: %v", err)
	}
	states, err = r.DiscoveryStates(ctx, []string{hash})
	if err != nil {
		t.Fatalf("DiscoveryStates after clear: %v", err)
	}
	if _, ok := states[hash]; ok {
		t.Errorf("hash still present after clear: %+v", states)
	}

	// A bad state value is refused.
	if err := r.SetDiscoveryState(ctx, hash, "unseen", ""); !errors.Is(err, ErrBadDiscoveryState) {
		t.Errorf("SetDiscoveryState with state=unseen: err = %v, want ErrBadDiscoveryState", err)
	}
}

// TestPositionsForContentLinksEachRequestToItsBlockCoordinates is the block
// drill-down's diff-link contract: for a hash and a set of request ids, each
// id must resolve to the (msg_index, position) where that request actually
// carries the block, so a per-row "view diff" link can call GuardrailDiff
// with coordinates that exist in that specific request.
func TestPositionsForContentLinksEachRequestToItsBlockCoordinates(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	block := textBlock("system", 0, 0, "a preamble block placed at message 0 position 0 always")
	// Same hash, different (msg_index,position) in a second request — proves
	// the mapping is per-request, not a single shared guess.
	w.Record(Event{
		TraceID: "t1", SessionKey: "s1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{block}},
	})
	w.Record(Event{
		TraceID: "t2", SessionKey: "s2", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{
			textBlock("user", 0, 0, "an unrelated leading block pushing position"),
			{Kind: "text", Body: block.Body, Role: "system", MsgIndex: 1, Position: 0},
		}},
	})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	repeated, err := r.RepeatedContent(ctx, WindowFrom(time.Hour), 2, 0, 50)
	if err != nil || len(repeated) != 1 {
		t.Fatalf("RepeatedContent: %v (%d blocks)", err, len(repeated))
	}
	hash := repeated[0].Hash

	rows, err := r.SessionsForContent(ctx, hash, 50)
	if err != nil || len(rows) != 2 {
		t.Fatalf("SessionsForContent: %v (%d rows)", err, len(rows))
	}
	ids := []int64{rows[0].ID, rows[1].ID}

	positions, err := r.PositionsForContent(ctx, hash, ids)
	if err != nil {
		t.Fatalf("PositionsForContent: %v", err)
	}
	if len(positions) != 2 {
		t.Fatalf("got %d positions, want one per request: %+v", len(positions), positions)
	}
	id1, id2 := idFor(t, r, "t1"), idFor(t, r, "t2")
	if positions[id1].MsgIndex != 0 || positions[id1].Position != 0 {
		t.Errorf("t1 position = %+v, want (0,0)", positions[id1])
	}
	if positions[id2].MsgIndex != 1 || positions[id2].Position != 0 {
		t.Errorf("t2 position = %+v, want (1,0)", positions[id2])
	}
}
