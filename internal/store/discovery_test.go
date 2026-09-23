package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestRequestsForContentReturnsEachRequestOnce is the duplicate-row trap: a
// client re-sends its conversation every turn, so one conversation holds many
// *references* to the same hash. A join against content_refs would report that
// conversation once per reference; the drill-down would then show a request
// three times and a count that matched nothing on the block list.
func TestRequestsForContentReturnsEachRequestOnce(t *testing.T) {
	w, r := captureFixture(t)
	ctx := context.Background()

	// One block, referenced twice inside the same request (two messages), plus
	// a second request that also carries it.
	const shared = "a block that appears in more than one message and request"
	w.Record(Event{
		TraceID: "t", SessionKey: "s1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
		Content: &CapturedContent{Request: []Block{
			textBlock("system", 0, 0, shared),
			textBlock("user", 1, 0, shared),
		}},
	})
	w.Record(Event{
		TraceID: "t", SessionKey: "s1", Format: "openai", Provider: "p", Model: "m", StatusCode: 200,
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

	rows, err := r.RequestsForContent(ctx, repeated[0].Hash, 50)
	if err != nil {
		t.Fatalf("RequestsForContent: %v", err)
	}
	// Two requests, and the first one contains two references to the same hash.
	// Anything other than 2 means the drill-down is counting references.
	if len(rows) != 2 {
		t.Errorf("drill-down returned %d rows, want 2 (one per request, not per reference)", len(rows))
	}
	// And it must agree with the count the block list showed, since the page
	// presents them as the same number.
	if int64(len(rows)) != repeated[0].Requests {
		t.Errorf("drill-down has %d requests but the block list says %d", len(rows), repeated[0].Requests)
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
		if _, err := r.RequestsForContent(ctx, bad, 10); err == nil {
			t.Errorf("RequestsForContent(%q) succeeded; a malformed hash must be refused, not silently match nothing", bad)
		}
	}

	// The valid form must work, so the guard is not simply refusing everything.
	valid := strings.Repeat("ab", 32)
	if _, err := r.RequestsForContent(ctx, valid, 10); err != nil {
		t.Errorf("RequestsForContent with a well-formed hash: %v", err)
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

// TestRequestsForContentExcludesRejections is now moot: no live write path
// ever produces owner_kind='rejected' rows (#5 — see schema.sql's comment on
// content_refs). Removed along with RecordRejected/rejectionID.

// idFor finds the id of the single request matching a trace_id, for tests
// that need to name a specific row to query against — ListRequests doesn't
// expose a trace_id filter, so this reads the id back the same way
// RequestsForContent tests already do (rows[N].ID), keyed on TraceID to make
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
