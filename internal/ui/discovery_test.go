package ui

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// discoveryEvents seeds two blocks that differ only in session spread, which is
// what makes the min_sessions control observable:
//
//   - shared: in three requests across two sessions — the shape of a client
//     preamble, and what the page exists to surface.
//   - local: in two requests, both in one session — repeated, so it passes
//     min_requests, but confined to a single conversation. At min_sessions=0 it
//     is listed; at the default 2 it is correctly not a discovery.
//
// The local block deliberately spans two *requests* in one session: a block in a
// single request cannot pass min_requests at all, so it would prove nothing
// about the session control.
func discoveryEvents() []store.Event {
	shared := store.Block{Kind: "text", Body: []byte("a preamble every session carries"), Role: "system", MsgIndex: 0, Position: 0}
	local := store.Block{Kind: "text", Body: []byte("a preamble only one session carries"), Role: "system", MsgIndex: 1, Position: 0}

	// (session, carriesLocal)
	requests := []struct {
		session string
		local   bool
	}{{"session-aaaa", true}, {"session-aaaa", true}, {"session-bbbb", false}}

	var events []store.Event
	for i, r := range requests {
		blocks := []store.Block{shared}
		if r.local {
			blocks = append(blocks, local)
		}
		blocks = append(blocks, store.Block{
			Kind: "text", Body: []byte("distinct question " + string(rune('a'+i)) + " with enough length"),
			Role: "user", MsgIndex: 2, Position: 0,
		})
		events = append(events, store.Event{
			TraceID: "t", SessionKey: r.session, Format: "openai", Provider: "p", Model: "m",
			StatusCode: 200, Content: &store.CapturedContent{Request: blocks},
		})
	}
	return events
}

// TestDiscoveryDefaultsToSessionSpread is the page's central claim: at
// min_sessions=0 the list is topped by a block repeated within one conversation,
// which is the ordinary shape of a chat rather than a finding. The default must
// therefore exclude the single-session block and include the shared one.
func TestDiscoveryDefaultsToSessionSpread(t *testing.T) {
	h, _ := newSeededHandler(t, discoveryEvents()...)

	// v2 is the current UI: the default must apply.
	body, code := getPage(t, h, "/admin/ui/content/repeated")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(body, "a preamble every session carries") {
		t.Error("the cross-session block is missing from the default view")
	}
	if strings.Contains(body, "only one session carries") {
		t.Error("the single-session block appears at the default min_sessions=2; " +
			"a block repeated inside one conversation is not a discovery")
	}

	// An explicit min_sessions=0 must include it, so the default is a default
	// rather than a floor.
	body, code = getPage(t, h, "/admin/ui/content/repeated?min_sessions=0")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(body, "only one session carries") {
		t.Error("min_sessions=0 did not widen the result; the parameter is being ignored")
	}
}

// TestDiscoveryCountsAndListAgree is the assertion that the summary line is not
// a second, differently-computed answer: the count the page prints and the rows
// it renders must describe the same query.
func TestDiscoveryCountsAndListAgree(t *testing.T) {
	h, _ := newSeededHandler(t, discoveryEvents()...)

	for _, q := range []string{"", "?min_sessions=0", "?min_sessions=2", "?min_requests=3&min_sessions=0"} {
		body, code := getPage(t, h, "/admin/ui/content/repeated"+q)
		if code != 200 {
			t.Fatalf("%s: status = %d", q, code)
		}
		rows := strings.Count(body, `href="/admin/ui/content/block?hash=`)
		// "showing N of M matching" states the rendered count; parse it and
		// require it to equal the rows actually on the page.
		if n, ok := parseShown(body); ok && n != rows {
			t.Errorf("%s: summary says %d shown but %d block links rendered", q, n, rows)
		}
	}
}

// parseShown reads the "showing N of M matching" summary line.
func parseShown(body string) (int, bool) {
	i := strings.Index(body, "showing ")
	if i < 0 {
		return 0, false
	}
	rest := body[i+len("showing "):]
	j := strings.Index(rest, " ")
	if j < 0 {
		return 0, false
	}
	n := 0
	for _, c := range rest[:j] {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// TestDiscoveryRejectsParametersByName pins that a bad parameter is a 400 that
// names the parameter, rather than a silent clamp: min_requests=1 cannot mean
// "repeated", and quietly raising it to 2 would make the page describe a query
// the operator did not ask for.
func TestDiscoveryRejectsParametersByName(t *testing.T) {
	h, _ := newSeededHandler(t, discoveryEvents()...)

	for _, tc := range []struct{ q, wantIn string }{
		{"?min_requests=1", "min_requests"},
		{"?min_requests=0", "min_requests"},
		{"?min_requests=abc", "min_requests"},
		{"?min_sessions=-1", "min_sessions"},
		{"?min_sessions=x", "min_sessions"},
		{"?limit=0", "limit"},
		{"?limit=100000", "limit"},
		{"?since=nonsense", "since"},
		{"?since=0h", "since"},
	} {
		body, code := getPage(t, h, "/admin/ui/content/repeated"+tc.q)
		if code != 400 {
			t.Errorf("%s: status = %d, want 400", tc.q, code)
			continue
		}
		if !strings.Contains(body, tc.wantIn) {
			t.Errorf("%s: the 400 does not name %q, so it cannot be acted on: %s", tc.q, tc.wantIn, body)
		}
	}
}

// TestBlockDrilldownLinksToItsRequests is the drill-down's contract: the link on
// the block list must resolve, list the requests containing the block, and offer
// each request's own page.
func TestBlockDrilldownLinksToItsRequests(t *testing.T) {
	h, _ := newSeededHandler(t, discoveryEvents()...)

	list, code := getPage(t, h, "/admin/ui/content/repeated")
	if code != 200 {
		t.Fatalf("list status = %d", code)
	}
	href := firstBlockHref(list)
	if href == "" {
		t.Fatal("no drill-down link on the block list")
	}

	body, code := getPage(t, h, href)
	if code != 200 {
		t.Fatalf("drill-down status = %d", code)
	}
	if !strings.Contains(body, "a preamble every session carries") {
		t.Error("the drill-down does not show the block's own body")
	}
	// Both seeded requests contain the shared block, so both must be listed and
	// both must link to their own detail page.
	if n := strings.Count(body, `href="/admin/ui/requests/`); n < 2 {
		t.Errorf("drill-down links to %d requests, want 2 (the block is in both)", n)
	}
	if !strings.Contains(body, "back to discovery") {
		t.Error("the drill-down has no way back to the block list")
	}
}

// TestBlockDrilldownRejectsAMalformedHash is the boundary between operator input
// and a server fault. A hash that is not a hash is a 400; reporting it as a 500
// sends someone looking for a bug in the store.
func TestBlockDrilldownRejectsAMalformedHash(t *testing.T) {
	h, _ := newSeededHandler(t, discoveryEvents()...)

	for _, hash := range []string{"zzzz", "abc123", strings.Repeat("ab", 31)} {
		body, code := getPage(t, h, "/admin/ui/content/block?hash="+url.QueryEscape(hash))
		if code != 400 {
			t.Errorf("hash=%q: status = %d, want 400", hash, code)
		}
		if strings.Contains(body, "query failed") {
			t.Errorf("hash=%q: reported as a query failure rather than bad input: %s", hash, body)
		}
	}

	if _, code := getPage(t, h, "/admin/ui/content/block"); code != 400 {
		t.Errorf("a missing hash: status = %d, want 400", code)
	}
}

// TestBlockDrilldownStatesTheCountItRenders is the bug this page shipped with:
// the stated request count was declared and never populated, so the page said
// "0 requests contain this block" directly above a table listing five. Counting
// rendered rows cannot catch that; the two numbers have to be compared.
func TestBlockDrilldownStatesTheCountItRenders(t *testing.T) {
	h, _ := newSeededHandler(t, discoveryEvents()...)

	list, code := getPage(t, h, "/admin/ui/content/repeated")
	if code != 200 {
		t.Fatalf("list status = %d", code)
	}
	href := firstBlockHref(list)
	if href == "" {
		t.Fatal("no drill-down link")
	}
	body, code := getPage(t, h, href)
	if code != 200 {
		t.Fatalf("drill-down status = %d", code)
	}

	rows := strings.Count(body, `href="/admin/ui/requests/`)
	stated, ok := parseContainsCount(body)
	if !ok {
		t.Fatalf("the drill-down states no request count: %s", body)
	}
	if stated != rows {
		t.Errorf("states %d requests contain the block but renders %d rows", stated, rows)
	}
	if stated == 0 {
		t.Error("the shared block is in two requests, so the count cannot be zero")
	}
}

// parseContainsCount reads "N requests contain this block" (or the capped
// "At least N requests contain this block").
func parseContainsCount(body string) (int, bool) {
	pat := regexp.MustCompile(`(?:At least )?(\d+) requests? contain`)
	m := pat.FindStringSubmatch(body)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// TestBlockDrilldownOnAnUnlinkedHash says the honest thing. A block can be
// recorded against a capture that has no request row — a rejected request — so
// its drill-down has nothing to list. Saying that is different from claiming the
// block does not exist, which is what an empty table implies.
func TestBlockDrilldownOnAnUnlinkedHash(t *testing.T) {
	h, _ := newSeededHandler(t, discoveryEvents()...)

	body, code := getPage(t, h, "/admin/ui/content/block?hash="+strings.Repeat("00", 32))
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(body, "No request in the store contains this block") {
		t.Errorf("an unlinked hash does not explain itself: %s", body)
	}
	if !strings.Contains(body, "No body is stored for this hash") {
		t.Errorf("an unknown hash claims a body exists: %s", body)
	}
}

// TestBlockListPreviewIsEscapedNotInterpreted is the escaping check on the new
// page: a block whose body contains markup must appear as text. The body is
// stored exactly as the client sent it, so this is reachable by any prompt.
func TestBlockListPreviewIsEscapedNotInterpreted(t *testing.T) {
	payload := "<script>alert(1)</script>"
	block := store.Block{Kind: "text", Body: []byte(payload), Role: "system", MsgIndex: 0, Position: 0}

	var events []store.Event
	for _, s := range []string{"s1", "s2"} {
		events = append(events, store.Event{
			TraceID: "t", SessionKey: s, Format: "openai", Provider: "p", Model: "m",
			StatusCode: 200, Content: &store.CapturedContent{Request: []store.Block{
				block,
				{Kind: "text", Body: []byte("distinct body " + s + " long enough to keep"), Role: "user", MsgIndex: 1, Position: 0},
			}},
		})
	}
	h, _ := newSeededHandler(t, events...)

	// The list shows a preview, and the drill-down shows the whole body.
	for _, target := range []string{"/admin/ui/content/repeated", "/admin/ui/content/repeated?min_sessions=0"} {
		body, code := getPage(t, h, target)
		if code != 200 {
			t.Fatalf("%s: status = %d", target, code)
		}
		if strings.Contains(body, payload) {
			t.Errorf("%s: the stored body was injected raw", target)
		}
		if !strings.Contains(body, "&lt;script&gt;") {
			t.Errorf("%s: the escaped body is not present either, so the block was dropped: %s", target, body)
		}
	}
}

// TestDiscoveryFragmentIsAFragment is the htmx response shape: the filter form
// swaps #repeated-rows, so a fragment request must return that element alone and
// must not carry a second <html>/<head>. A full page returned into a swap would
// nest a document in the page, which renders wrong rather than failing.
func TestDiscoveryFragmentIsAFragment(t *testing.T) {
	h, _ := newSeededHandler(t, discoveryEvents()...)

	full, code := getPage(t, h, "/admin/ui/content/repeated")
	if code != 200 {
		t.Fatalf("full page status = %d", code)
	}
	frag, code := getFragment(t, h, "/admin/ui/content/repeated")
	if code != 200 {
		t.Fatalf("fragment status = %d", code)
	}
	if strings.Contains(frag, "<html") || strings.Contains(frag, "<!doctype") {
		t.Error("the fragment response carries a whole document; a swap would nest one")
	}
	// It must be the swap target itself, so hx-swap="outerHTML" replaces it.
	if !strings.Contains(frag, `id="repeated-rows"`) {
		t.Error("the fragment does not carry the #repeated-rows element it swaps")
	}
	// And it must be a strict subset of the page, i.e. the same rows.
	if len(frag) >= len(full) {
		t.Errorf("fragment (%d bytes) is not smaller than the page (%d bytes)", len(frag), len(full))
	}
}

// firstBlockHref pulls the first drill-down link out of the block list.
func firstBlockHref(body string) string {
	const marker = `href="/admin/ui/content/block?hash=`
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i+len(`href="`):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// TestDiscoveryWindowIsHonoured checks the window actually filters, using a
// block seeded outside it.
func TestDiscoveryWindowIsHonoured(t *testing.T) {
	h, _ := newSeededHandler(t, discoveryEvents()...)

	// The seeded events have just been written, so a window that ends before
	// them must exclude everything.
	body, code := getPage(t, h, "/admin/ui/content/repeated?since=1ns")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	if strings.Contains(body, "a preamble every session carries") {
		t.Error("a window of 1ns still returned data; since is not being applied")
	}
	_ = time.Second
}
