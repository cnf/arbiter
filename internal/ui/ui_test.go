package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	r.HandleFunc("/admin/ui/requests/{id}/guardrail-diff", h.GuardrailDiffHandler).Methods("GET")
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






func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	if len(s) > 300 {
		return s[:300]
	}
	return s
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
	if !strings.Contains(rec.Body.String(), "--text") {
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


// whichProviderA keeps the session tests' provider name in one place.
const whichProviderA = "alpha"

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
