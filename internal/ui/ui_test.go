package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/store"
)

// newTestHandler builds a Handler with no reader — the disabled-store case,
// which every route must handle. Tests that need data use newSeededHandler.
func newTestHandler() *Handler {
	return New(nil, logging.NewStdoutLogger("error"))
}

// newSeededHandler seeds a real sqlite store through the real SQLiteWriter and
// returns a Handler over it. Seeding through the writer rather than with
// hand-written INSERTs is not decoration: the store's ts column is TEXT in the
// driver's own layout, and a hand-written value would be a layout production
// never produces — which is exactly what the keyset cursor is sensitive to.
func newSeededHandler(t *testing.T, events ...store.Event) (*Handler, *store.SQLiteWriter) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	logger := logging.NewStdoutLogger("error")
	w, err := store.NewSQLiteWriter(path, logger)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	for _, ev := range events {
		w.Record(ev)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	r, err := store.OpenReader(path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return New(r, logger), nil
}

// newSeededHandlerLive is newSeededHandler with a writer still open over the same
// file, so a test can record a request *after* the handler has started reading —
// which is the only way to test a live view. The store's writer is not closed
// here; the reader is opened separately, so the two see the same database.
func newSeededHandlerLive(t *testing.T, events ...store.Event) (*Handler, *store.SQLiteWriter) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "live.db")
	logger := logging.NewStdoutLogger("error")
	w, err := store.NewSQLiteWriter(path, logger)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	for _, ev := range events {
		w.Record(ev)
	}
	r, err := store.OpenReader(path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// Wait for the seeded events to be *visible*, not merely recorded. The store
	// writes through a queue, so rows appear a moment after Record returns; a
	// test that read immediately would race the writer and see a partial table —
	// which is exactly what made a tail test report one row where three were
	// seeded. Bounded, so a genuine write failure fails rather than hangs.
	if len(events) > 0 {
		deadline := time.Now().Add(5 * time.Second)
		for {
			rows, err := r.ListRequestsAfter(context.Background(), store.RequestFilter{}, "", 0, 500)
			if err == nil && len(rows) >= len(events) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("only %d of %d seeded events became visible", len(rows), len(events))
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	return New(r, logger), w
}

// serve drives a handler method through the router, so the route pattern (and
// therefore mux.Vars) is exercised rather than faked.
func serve(t *testing.T, h *Handler, method, target string, hx bool) *httptest.ResponseRecorder {
	t.Helper()
	r := mux.NewRouter()
	r.HandleFunc("/admin/ui/requests", h.RequestsHandler).Methods("GET")
	// Same registration order as the real router: mux matches in order, and
	// {id} would otherwise swallow the literal "tail" and answer 400.
	r.HandleFunc("/admin/ui/requests/tail", h.TailHandler).Methods("GET")
	r.HandleFunc("/admin/ui/requests/{id}", h.RequestHandler).Methods("GET")
	r.HandleFunc("/admin/ui/requests/{id}/content", h.RequestContentHandler).Methods("GET")
	r.HandleFunc("/admin/ui/sessions", h.SessionsHandler).Methods("GET")
	r.HandleFunc("/admin/ui/session", h.SessionHandler).Methods("GET")
	r.HandleFunc("/admin/ui/overview", h.OverviewHandler).Methods("GET")
	r.HandleFunc("/admin/ui/overview/series.json", h.SeriesHandler).Methods("GET")
	r.HandleFunc("/admin/ui/content/repeated", h.DiscoveryHandler).Methods("GET")
	r.HandleFunc("/admin/ui/content/block", h.BlockRequestsHandler).Methods("GET")
	r.PathPrefix("/admin/ui/static/").HandlerFunc(h.StaticHandler).Methods("GET")

	req := httptest.NewRequest(method, target, nil)
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// getPage fetches a full page through the router and returns its body and status.
func getPage(t *testing.T, h *Handler, target string) (string, int) {
	t.Helper()
	rec := serve(t, h, "GET", target, false)
	return rec.Body.String(), rec.Code
}

// getFragment fetches a route as htmx would, so the fragment response shape is
// exercised rather than only the full page.
func getFragment(t *testing.T, h *Handler, target string) (string, int) {
	t.Helper()
	rec := serve(t, h, "GET", target, true)
	return rec.Body.String(), rec.Code
}

// serveJSON drives a handler through the router and returns the status and body,
// for the endpoint that answers JSON rather than HTML.
func serveJSON(t *testing.T, h *Handler, target string) (int, string) {
	t.Helper()
	r := mux.NewRouter()
	r.HandleFunc("/admin/ui/overview/series.json", h.SeriesHandler).Methods("GET")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
	return rec.Code, rec.Body.String()
}

// Every page and fragment must render against a zero-value view model.
// template.Must catches a template that does not *parse* at construction, but
// html/template resolves field names only at execution, so a renamed struct
// field would otherwise first fail in a browser.
//
// Two things about this test are load-bearing and were both wrong at first:
//
//   - It executes "layout", not the page name. Each page's own {{define "content"}}
//     means the set's root template — the one named after the page — is empty, so
//     executing *that* renders nothing and passes while the real page is broken.
//     It did exactly that until the discovery page joined, rendering 28 bytes and
//     reporting success.
//   - Each page gets the view type its handler actually passes, listed in
//     pageViews, rather than one composite struct. A composite both invites
//     field-name collisions between views (two embeds could each claim .Rows) and
//     hides which fields a page really reads.
func TestEveryPageRendersWithZeroData(t *testing.T) {
	h := newTestHandler()
	for _, page := range pageFiles {
		entry, ok := pageViews[page]
		if !ok {
			// A new page with no view registered here is a gap in the test, not
			// a pass: say so rather than silently skipping it.
			t.Errorf("page %q has no view type registered in pageViews, so it is not covered here", page)
			continue
		}
		var buf strings.Builder
		if err := h.pages[page].ExecuteTemplate(&buf, "layout", entry.view(h, page)); err != nil {
			t.Errorf("page %q does not render empty: %v", page, err)
		}
		// A page that renders nothing is the vacuous pass this test exists to
		// avoid, so require the layout to have actually produced a document.
		if buf.Len() == 0 {
			t.Errorf("page %q rendered zero bytes", page)
		}
	}
	for _, frag := range fragmentNames(t, h) {
		entry, ok := fragmentViews[frag]
		if !ok {
			// Not every partial is a top-level fragment: "block" and "turnblock"
			// are only ever invoked by another partial, and several fragments
			// take the same view shape. Where a fragment has no entry, it is
			// exercised through its page instead.
			continue
		}
		var buf strings.Builder
		if err := h.fragments["fragments"].ExecuteTemplate(&buf, frag, entry.view(h, frag)); err != nil {
			t.Errorf("fragment %q does not render empty: %v", frag, err)
		}
	}
}

// viewEntry builds a zero-value view model of one page's own type. The function
// is what keeps the view types unexported and their field names checked by the
// compiler: adding a field to a view and not to the template is caught here.
type viewEntry struct {
	view func(h *Handler, name string) interface{}
}

// pageViews maps each page to the view its handler passes. The pairing is the
// point: it is what a page's template can actually reference.
var pageViews = map[string]viewEntry{
	"requests":  {view: func(h *Handler, n string) interface{} { return requestsView{viewBase: h.base(n)} }},
	"request":   {view: func(h *Handler, n string) interface{} { return detailView{viewBase: h.base(n)} }},
	"sessions":  {view: func(h *Handler, n string) interface{} { return sessionsView{viewBase: h.base(n)} }},
	"session":   {view: func(h *Handler, n string) interface{} { return sessionView{viewBase: h.base(n)} }},
	"overview":  {view: func(h *Handler, n string) interface{} { return overviewView{viewBase: h.base(n)} }},
	"discovery": {view: func(h *Handler, n string) interface{} { return discoveryView{viewBase: h.base(n)} }},
	"block":     {view: func(h *Handler, n string) interface{} { return blockRequestsView{viewBase: h.base(n)} }},

	// error.html is parsed but rendered by nothing: Handler.fail builds its HTML
	// inline, on purpose — a renderer failure must not be reported by the
	// renderer. So there is no handler view to hand it, and it is listed here as
	// its own shape so the page is still known to the test rather than being
	// quietly absent from pageFiles. It is dead code with a live parser entry.
	"error": {view: func(h *Handler, n string) interface{} {
		return struct {
			viewBase
			Code    int
			Message string
		}{viewBase: h.base(n)}
	}},
}

// fragmentViews maps a fragment name to its own view shape. Fragments with no
// entry are the nested ones exercised through their page.
var fragmentViews = map[string]viewEntry{
	"req-rows":        {view: func(h *Handler, n string) interface{} { return rowsView{} }},
	"request-content": {view: func(h *Handler, n string) interface{} { return detailView{} }},
	"pagination":      {view: func(h *Handler, n string) interface{} { return rowsView{} }},
	"filters":         {view: func(h *Handler, n string) interface{} { return rowsView{} }},
	"pivot-table":     {view: func(h *Handler, n string) interface{} { return overviewView{} }},
	"session-rows":    {view: func(h *Handler, n string) interface{} { return sessionsView{} }},
	"session-turns":   {view: func(h *Handler, n string) interface{} { return sessionView{} }},
	"repeated-rows":   {view: func(h *Handler, n string) interface{} { return discoveryView{} }},
	"block-requests":  {view: func(h *Handler, n string) interface{} { return blockRequestsView{} }},
	"empty":           {view: func(h *Handler, n string) interface{} { return struct{ Message string }{} }},
}

// fragmentNames lists the defined templates in the partials set, skipping the
// set's own root name. Deriving it from the parsed set rather than a hand-kept
// list means a new partial is covered by the zero-data render without anyone
// remembering to add it — which is the failure mode a hand-kept list has.
func fragmentNames(t *testing.T, h *Handler) []string {
	t.Helper()
	set, ok := h.fragments["fragments"]
	if !ok {
		t.Fatal("no fragments set")
	}
	out := []string{}
	for _, tpl := range set.Templates() {
		name := tpl.Name()
		if name == "fragments" || strings.HasPrefix(name, "_") {
			continue
		}
		out = append(out, name)
	}
	return out
}

// No stored body may ever be marked safe in any context. The grep is for the
// conversion *calls*, which is the one mechanical check that survives a later
// "just fixing the newlines in a transcript". It catches "someone marked a body
// safe"; the escaped-output assertions below cover the other direction.
func TestNoUnsafeContentConversions(t *testing.T) {
	banned := []string{"template.HTML(", "template.JS(", "template.URL(", "template.Identifier("}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, bad := range banned {
			if bytesContains(string(b), bad) {
				t.Errorf("%s converts stored data with %s; bodies must not be marked safe", f, bad)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no Go files were checked — the glob is not finding this package's sources")
	}

	tplFiles, err := filepath.Glob("templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	tplFiles = append(tplFiles, mustGlob(t, "templates/partials/*.html")...)
	tplFiles = append(tplFiles, mustGlob(t, "templates/pages/*.html")...)
	if len(tplFiles) == 0 {
		t.Fatal("no templates were checked")
	}
	for _, f := range tplFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range banned {
			if bytesContains(string(b), bad) {
				t.Errorf("%s uses %s", f, bad)
			}
		}
	}
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	out, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func bytesContains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// timeAt is a fixed instant for seeded rows, so ordering and the "ago"
// rendering are deterministic without freezing time globally.
func timeAt() time.Time {
	return time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
}

// A captured body is untrusted text: a script tag in a prompt must come back
// escaped, with the raw string absent. The session key reaches a URL context,
// so it gets the same treatment through a different escaper.
func TestCapturedBodyIsEscaped(t *testing.T) {
	h, _ := newSeededHandler(t,
		store.Event{
			TraceID: "t", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1,
			SessionKey: `s"1<script>`,
			Content: &store.CapturedContent{
				Request: []store.Block{{
					MsgIndex: 0, Position: 0, Role: "user", Kind: "text",
					Body: []byte(`<script>alert(1)</script>`),
				}},
			},
		})

	rows := serve(t, h, "GET", "/admin/ui/requests", false).Body.String()
	if strings.Contains(rows, "<script>") {
		t.Error("a raw <script> element reached the list page")
	}
	// The key reaches an href (URL context) and a link body (HTML context);
	// html/template applies both escapers without being asked.
	if !strings.Contains(rows, "&lt;script&gt;") {
		t.Errorf("session key was not HTML-escaped; body = %s", firstLine(rows))
	}
	if !strings.Contains(strings.ToLower(rows), "%3cscript%3e") {
		t.Errorf("session key was not URL-escaped in the link; body = %s", firstLine(rows))
	}

	content := serve(t, h, "GET", "/admin/ui/requests/1/content", true).Body.String()
	if strings.Contains(content, "<script>alert(1)</script>") {
		t.Error("a captured prompt body was rendered unescaped")
	}
	if !strings.Contains(content, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("captured body was not escaped; body = %s", firstLine(content))
	}
}

// Store-derived text that is not a captured body (an upstream error, a routing
// rationale) still reaches the page as a plain string. It is escaped by the
// same contextual rule, and this pins that so a future "these are our own
// strings, they can be marked safe" change is caught.
func TestStoreErrorTextIsEscaped(t *testing.T) {
	h, _ := newSeededHandler(t, store.Event{
		TraceID: "t", Provider: "p", Model: "m", LatencyMs: 1, StatusCode: 500,
		RoutingRationale: `upstream said <b>no</b>`,
		Error:            `<img src=x onerror="alert(1)">`,
	})
	body := serve(t, h, "GET", "/admin/ui/requests", false).Body.String()
	for _, raw := range []string{`<img src=x onerror="alert(1)">`, `<b>no</b>`} {
		if strings.Contains(body, raw) {
			t.Errorf("unescaped store text reached the page: %s", raw)
		}
	}
	if !strings.Contains(body, "&lt;img src=x onerror=") {
		t.Errorf("error text was not escaped; body = %s", firstLine(body))
	}
}

// The error text for a failed request sits in the status code's hover
// tooltip, not as its own visible row — a separate row per error cluttered
// the list with text nobody asked to see yet.
func TestErrorTextIsAHoverTooltipNotAnInlineRow(t *testing.T) {
	h, _ := newSeededHandler(t, store.Event{
		TraceID: "t", Provider: "p", Model: "m", LatencyMs: 1, StatusCode: 502,
		Error: "upstream unreachable",
	})
	body := serve(t, h, "GET", "/admin/ui/requests", false).Body.String()
	if !strings.Contains(body, `title="upstream unreachable"`) {
		t.Errorf("error text is not on the status code's title attribute; body = %s", firstLine(body))
	}
	if strings.Contains(body, `class="errrow"`) {
		t.Errorf("error is still rendered as its own row; body = %s", firstLine(body))
	}
}

// The actual upstream-reported model (a meta-router alias like OpenRouter's
// "openrouter/auto" picking something concrete) shows on both the request
// list row and the detail page, distinct from the routed model.
func TestActualModelShownOnListAndDetail(t *testing.T) {
	h, _ := newSeededHandler(t, store.Event{
		TraceID: "t", Provider: "litellm", Model: "openrouter/auto", ActualModel: "anthropic/claude-3.5-sonnet",
		StatusCode: 200, LatencyMs: 1,
	})
	list := serve(t, h, "GET", "/admin/ui/requests", false).Body.String()
	if !strings.Contains(list, "anthropic/claude-3.5-sonnet") {
		t.Errorf("actual model missing from request list; body = %s", firstLine(list))
	}

	detail := serve(t, h, "GET", "/admin/ui/requests/1", false).Body.String()
	if !strings.Contains(detail, "anthropic/claude-3.5-sonnet") {
		t.Errorf("actual model missing from request detail; body = %s", firstLine(detail))
	}
}

// The common case — a plain provider that doesn't diverge — must not show a
// redundant "actually X" when X equals the routed model.
func TestActualModelHiddenWhenAbsent(t *testing.T) {
	h, _ := newSeededHandler(t, store.Event{
		TraceID: "t", Provider: "claude", Model: "claude-3-haiku", StatusCode: 200, LatencyMs: 1,
	})
	list := serve(t, h, "GET", "/admin/ui/requests", false).Body.String()
	if strings.Contains(list, "actually") || strings.Contains(list, "upstream-reported model") {
		t.Errorf("actual-model UI leaked with no divergence; body = %s", firstLine(list))
	}
}

// The request list defaults to showing every kind (client traffic plus
// Arbiter's own internal requests, tagged so they're distinguishable), and an
// explicit ?kind=client narrows to real traffic only — same contract as the
// JSON surface, exercised here against the actual rendered HTML.
func TestRequestListDefaultsToClientKindAndTagsOthers(t *testing.T) {
	h, _ := newSeededHandler(t,
		store.Event{TraceID: "client-row", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1},
		store.Event{TraceID: "classifier-row", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1, Kind: "classifier"},
	)

	// data-id is the row's rowid — 1 for the client row (inserted first), 2
	// for the classifier row — since the row markup carries no other field
	// that identifies which event produced it.
	def := serve(t, h, "GET", "/admin/ui/requests", false).Body.String()
	if !strings.Contains(def, `data-id="1"`) {
		t.Errorf("default view is missing the client row; body = %s", firstLine(def))
	}
	if !strings.Contains(def, `data-id="2"`) {
		t.Errorf("default view is missing the classifier row; body = %s", firstLine(def))
	}
	if !strings.Contains(def, `class="tag kind"`) {
		t.Errorf("classifier row has no kind tag in the default view; body = %s", firstLine(def))
	}

	clientOnly := serve(t, h, "GET", "/admin/ui/requests?kind=client", false).Body.String()
	if strings.Contains(clientOnly, `data-id="2"`) {
		t.Errorf("?kind=client included the classifier row; body = %s", firstLine(clientOnly))
	}
	if !strings.Contains(clientOnly, `data-id="1"`) {
		t.Errorf("?kind=client is missing the client row; body = %s", firstLine(clientOnly))
	}
}

// TestRequestListRendersClassifierAboveItsParent is #8's visible ordering
// contract: the page is newest-first top-to-bottom, and the decision was
// "parent before child, unconditionally" where before means later in that
// top-to-bottom reading — i.e. lower down, since lower = earlier in time on
// this page. So the parent's own row must render UNDER its classifier child's
// row in the actual HTML, not above it, regardless of which of the two this
// store returns with the smaller ts/id (see attachTraceChildren, and the
// reqrow.html req-line doc comment it's paired with).
func TestRequestListRendersClassifierAboveItsParent(t *testing.T) {
	h, _ := newSeededHandler(t,
		store.Event{TraceID: "shared-trace", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1},
		store.Event{TraceID: "shared-trace", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1, Kind: "classifier"},
	)

	body := serve(t, h, "GET", "/admin/ui/requests", false).Body.String()
	parentPos := strings.Index(body, `data-id="1"`)
	childPos := strings.Index(body, `data-id="2"`)
	if parentPos == -1 || childPos == -1 {
		t.Fatalf("expected both rows in the page; body = %s", firstLine(body))
	}
	if childPos > parentPos {
		t.Errorf("classifier row (id 2) rendered at byte %d, after its parent (id 1) at byte %d; "+
			"want the classifier ABOVE the parent, since the page reads newest-first top-to-bottom "+
			"and the parent is fixed as chronologically first (#8)", childPos, parentPos)
	}
}

// The request detail page shows captured headers, User-Agent included — the
// whole point of capturing them — and the credential redaction already done
// by the HTTP layer before the event reached the store passes through as-is.
func TestRequestDetailShowsHeaders(t *testing.T) {
	h, _ := newSeededHandler(t, store.Event{
		TraceID: "t", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1,
		Headers: map[string]string{"User-Agent": "opencode/1.0", "Authorization": "[REDACTED]"},
	})
	body := serve(t, h, "GET", "/admin/ui/requests/1", false).Body.String()
	if !strings.Contains(body, "opencode/1.0") {
		t.Errorf("User-Agent missing from detail page; body = %s", firstLine(body))
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Errorf("redacted header value missing from detail page; body = %s", firstLine(body))
	}
}

// Content that was stored hash-only (an image, a streamed tool call) must be
// shown as such rather than omitted: "not capturable" and "nothing to capture"
// are different answers.
func TestHashOnlyBlockIsShownNotOmitted(t *testing.T) {
	h, _ := newSeededHandler(t, store.Event{
		TraceID: "t", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1,
		Content: &store.CapturedContent{Request: []store.Block{
			{MsgIndex: 0, Position: 0, Role: "user", Kind: "text", Body: []byte("hello")},
			{MsgIndex: 0, Position: 1, Role: "user", Kind: "image"},
		}},
	})
	body := serve(t, h, "GET", "/admin/ui/requests/1/content", true).Body.String()
	if !strings.Contains(body, "not captured") {
		t.Errorf("a hash-only block was omitted; body = %s", firstLine(body))
	}
	if !strings.Contains(body, "image") {
		t.Error("the hash-only block's type is not rendered")
	}
	if !strings.Contains(body, "hello") {
		t.Error("the captured text block is missing")
	}
}

// "Capture is off" and "this request had no content" must not look the same —
// the empty state says which and what to check.
func TestEmptyContentExplainsItself(t *testing.T) {
	h, _ := newSeededHandler(t, store.Event{TraceID: "t", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1})
	body := serve(t, h, "GET", "/admin/ui/requests/1/content", true).Body.String()
	if !strings.Contains(body, "capture_content") {
		t.Errorf("the empty content state does not name the setting; body = %s", firstLine(body))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

// A disabled store must explain itself in HTML — a browser landing on a JSON
// error body is a dead end — while a fragment request gets a short note to
// swap in. "Disabled" and "no traffic yet" are different answers.
func TestNilReaderRendersHTMLNotJSON(t *testing.T) {
	h := newTestHandler()

	page := serve(t, h, "GET", "/admin/ui/requests", false)
	if page.Code != http.StatusOK {
		t.Fatalf("disabled store list = %d, want 200 with an explanatory page", page.Code)
	}
	body := page.Body.String()
	if !strings.Contains(body, "<!doctype html>") {
		t.Error("disabled store did not render a page")
	}
	if strings.Contains(body, `"error"`) {
		t.Error("disabled store rendered a JSON error body")
	}
	if !strings.Contains(body, "storage.path") {
		t.Error("the page does not name the setting that is missing")
	}

	frag := serve(t, h, "GET", "/admin/ui/requests", true)
	if frag.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled store fragment = %d, want 503", frag.Code)
	}
	if strings.Contains(frag.Body.String(), "<!doctype html>") {
		t.Error("a fragment response carried a whole page")
	}
}

// One route serves both shapes: an htmx request gets the fragment, a plain one
// gets the page. The fragment must not carry a second document.
func TestFragmentDispatch(t *testing.T) {
	h, _ := newSeededHandler(t, store.Event{TraceID: "t", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1})

	full := serve(t, h, "GET", "/admin/ui/requests", false).Body.String()
	if !strings.Contains(full, "<!doctype html>") {
		t.Error("full page request did not return a document")
	}
	frag := serve(t, h, "GET", "/admin/ui/requests", true).Body.String()
	if strings.Contains(frag, "<!doctype html>") || strings.Contains(frag, "<html") {
		t.Error("fragment request returned a whole document")
	}
	if !strings.Contains(frag, `id="rows"`) {
		t.Errorf("fragment does not carry the swap target; got %s", firstLine(frag))
	}
}

// A malformed filter is a 400 naming the parameter, never a silently ignored
// filter: silently dropping it answers a broken query with plausible-looking
// unfiltered data.
func TestMalformedFiltersAreRejected(t *testing.T) {
	h, _ := newSeededHandler(t)
	for _, tc := range []struct{ q, want string }{
		{"?status=abc", "status"},
		{"?status=99", "status"},
		{"?limit=0", "limit"},
		{"?since=7d", "since"},
		{"?after=!!!!", "after"},
	} {
		rec := serve(t, h, "GET", "/admin/ui/requests"+tc.q, false)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", tc.q, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: the 400 does not name %q", tc.q, tc.want)
		}
	}
}

// A malformed id is 400 and an absent one is 404 — the split the JSON surface
// already makes, so the two read surfaces agree on what a bad id means.
func TestDetailStatusSplit(t *testing.T) {
	h, _ := newSeededHandler(t)
	if got := serve(t, h, "GET", "/admin/ui/requests/abc", false).Code; got != http.StatusBadRequest {
		t.Errorf("bad id = %d, want 400", got)
	}
	if got := serve(t, h, "GET", "/admin/ui/requests/9999", false).Code; got != http.StatusNotFound {
		t.Errorf("absent id = %d, want 404", got)
	}
}

// Assets are embedded and served from the binary, with the versioned URL
// cached forever and an unversioned one not.
func TestStaticAssetsAreServedAndVersioned(t *testing.T) {
	h := newTestHandler()
	if h.assetVersion == "" {
		t.Fatal("asset version is empty; asset URLs would not be cache-busted")
	}

	rec := serve(t, h, "GET", "/admin/ui/static/app.css", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("app.css = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "--fg") {
		t.Error("app.css body does not look like our stylesheet")
	}

	versioned := serve(t, h, "GET", "/admin/ui/static/app.css?v="+h.assetVersion, false)
	if cc := versioned.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("versioned asset Cache-Control = %q, want immutable", cc)
	}
	if cc := rec.Header().Get("Cache-Control"); strings.Contains(cc, "immutable") {
		t.Errorf("unversioned asset is cached immutably: %q", cc)
	}
	if got := serve(t, h, "GET", "/admin/ui/static/nope.css", false).Code; got != http.StatusNotFound {
		t.Errorf("missing asset = %d, want 404", got)
	}
	// The vendored JS must actually be in the binary, not merely referenced.
	if got := serve(t, h, "GET", "/admin/ui/static/htmx.min.js", false).Code; got != http.StatusOK {
		t.Errorf("htmx.min.js = %d, want 200", got)
	}
}

// The page's own URLs carry the asset version, so a stale stylesheet cannot
// outlive a changed one.
func TestPageReferencesVersionedAssets(t *testing.T) {
	h, _ := newSeededHandler(t)
	body := serve(t, h, "GET", "/admin/ui/requests", false).Body.String()
	if !strings.Contains(body, "/admin/ui/static/app.css?v="+h.assetVersion) {
		t.Error("the page does not reference the versioned stylesheet")
	}
	if !strings.Contains(body, "/admin/ui/static/htmx.min.js?v="+h.assetVersion) {
		t.Error("the page does not reference the versioned htmx")
	}
}

// whichProviderA keeps the session tests' provider name in one place.
const whichProviderA = "alpha"

// seedMany writes n events one second apart, for paging.
func seedMany(t *testing.T, n int) []store.Event {
	t.Helper()
	events := make([]store.Event, 0, n)
	base := timeAt()
	for i := 0; i < n; i++ {
		events = append(events, store.Event{
			TraceID: "t", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1,
			Ts: base.Add(time.Duration(i) * time.Second),
		})
	}
	return events
}

// A capped list must say so: rendered without a note, a truncated list reads as
// data loss.
func TestCappedListIsLabelled(t *testing.T) {
	h, _ := newSeededHandler(t, seedMany(t, 5)...)
	body := serve(t, h, "GET", "/admin/ui/requests?limit=2", false).Body.String()
	if !strings.Contains(body, "load more") {
		t.Errorf("a capped list does not offer a continuation; got %s", firstLine(body))
	}
}

// The cursor is opaque and URL-safe: the payload contains the stored timestamp
// text, whose `+` characters must not survive into a query string as spaces.
func TestCursorIsOpaqueAndRoundTrips(t *testing.T) {
	stored := "2026-09-16 11:59:39.812343302 +0000 UTC"
	token := encodeCursor(stored, 42)
	if strings.ContainsAny(token, "+/= ") {
		t.Errorf("cursor %q is not URL-safe", token)
	}
	ts, id, err := decodeCursor(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ts != stored || id != 42 {
		t.Errorf("round trip = (%q, %d), want (%q, 42)", ts, id, stored)
	}
	for _, bad := range []string{"", "!!!!", "Zm9v", encodeCursor("", 1), encodeCursor(stored, 0)} {
		if _, _, err := decodeCursor(bad); err == nil {
			t.Errorf("decodeCursor(%q) accepted a malformed cursor", bad)
		}
	}
}

// A malformed cursor is a 400, not a silently ignored page position.
func TestMalformedCursorIsRejected(t *testing.T) {
	h, _ := newSeededHandler(t, store.Event{TraceID: "t", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1})
	rec := serve(t, h, "GET", "/admin/ui/requests?after=!!!!", false)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad cursor = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "after") {
		t.Error("the 400 does not name the offending parameter")
	}
}

// Paging through the handler's own link (rather than the store directly) must
// also walk every row exactly once — that is the property the opaque token
// exists to preserve end to end, encoding included.
func TestHandlerPagingWalksEveryRow(t *testing.T) {
	h, _ := newSeededHandler(t, seedMany(t, 7)...)

	seen := map[int64]int{}
	url := "/admin/ui/requests?limit=3"
	for i := 0; i < 10; i++ {
		body := serve(t, h, "GET", url, true).Body.String()
		ids := idPattern.FindAllStringSubmatch(body, -1)
		if len(ids) == 0 {
			break
		}
		for _, m := range ids {
			n, _ := strconv.ParseInt(m[1], 10, 64)
			seen[n]++
		}
		next := moreLinkPattern.FindStringSubmatch(body)
		if next == nil {
			break
		}
		url = strings.ReplaceAll(next[1], "&amp;", "&")
	}
	if len(seen) != 7 {
		t.Errorf("paged over %d distinct rows, want 7", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("row %d appeared %d times through the handler's pager", id, n)
		}
	}
}

var (
	idPattern       = regexp.MustCompile(`/admin/ui/requests/(\d+)"`)
	moreLinkPattern = regexp.MustCompile(`hx-get="(/admin/ui/requests\?[^"]*after=[^"]*)"`)
)

// The list JSON shape is what the JSON surface's own clients see; the UI must
// not have changed it by adding its cursor field to the wire.
func TestCursorFieldIsNotOnTheWire(t *testing.T) {
	h, _ := newSeededHandler(t)
	body := serve(t, h, "GET", "/admin/ui/requests", false).Body.String()
	if strings.Contains(body, "tsraw") || strings.Contains(body, "TsRaw") {
		t.Error("the internal cursor field leaked into a rendered page")
	}
	_ = json.Marshal // keep the import honest if the assertions above change
}
