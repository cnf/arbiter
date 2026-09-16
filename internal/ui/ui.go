// Package ui serves Arbiter's admin web UI: a read-only browser over the same
// sqlite event store the JSON /admin/* surface exposes.
//
// It is deliberately not part of internal/http. That package is proxy ingress —
// wire formats, SSE flushing, the request hot path — and the UI shares none of
// it; the UI brings its own embedded asset tree and its own rendering, and its
// dependencies are exactly StatsHandler's: a *store.Reader and a logger.
package ui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	stdhtml "html/template"
	"io/fs"
	"net/http"
	"time"

	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/store"
)

// assets holds the templates and the static tree. They are embedded rather
// than read from disk so a deployed binary cannot be missing its own UI, and
// so a template that does not parse fails at process start (the template.Must
// in parseTemplates) rather than on the first request in production.
//
//go:embed templates static
var assets embed.FS

// blockPreviewBytes caps how much of one captured block a page renders before
// offering the full text. Captured bodies are client prompts and model output,
// and a single one can be megabytes: rendering it whole makes the page
// unusable rather than merely slow, and the transcript can hold dozens.
const blockPreviewBytes = 8 * 1024

// Handler serves the UI. A nil reader means the event store is disabled
// (storage.path unset); every page then explains that in HTML rather than
// serving the JSON 503 the admin API uses — a browser landing on {"error":…}
// is a dead end, while a fragment request gets a short HTML note because htmx
// is about to swap it into the page.
type Handler struct {
	reader *store.Reader
	logger logging.Logger

	// pages and fragments are built once, in New. Pages need one template set
	// each (see parseTemplates); fragments are rendered from the partials
	// alone.
	pages     map[string]*stdhtml.Template
	fragments map[string]*stdhtml.Template

	// assetsFS is the embedded tree rooted at static/, served by staticHandler.
	assetsFS fs.FS

	// assetVersion changes whenever any embedded asset byte changes, so asset
	// URLs are self-invalidating. An embed.FS reports a zero ModTime, which
	// makes Last-Modified and ETag useless; a content-derived query parameter
	// is the only mechanism available.
	assetVersion string
}

// New builds the UI around an already-open Reader. A nil reader is the
// disabled-store case, not an error.
func New(reader *store.Reader, l logging.Logger) *Handler {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic("ui: embedded static tree: " + err.Error())
	}
	return &Handler{
		reader:       reader,
		logger:       l,
		pages:        parseTemplates(),
		fragments:    parsePartials(),
		assetsFS:     sub,
		assetVersion: versionOf(assets),
	}
}

// versionOf hashes the whole embedded tree. Hashing everything rather than per
// file means adding or editing any asset invalidates every asset URL at once —
// intended: these are a handful of files served immutably, not a CDN.
func versionOf(fsys fs.FS) string {
	sum := sha256.New()
	// WalkDir reports entries in lexical order, so the digest is stable.
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		_, _ = sum.Write([]byte(path))
		_, _ = sum.Write(b)
		return nil
	})
	if err != nil {
		panic("ui: hash embedded assets: " + err.Error())
	}
	return hex.EncodeToString(sum.Sum(nil))[:12]
}

// pageFiles are the page templates, each rendered only inside its own set.
var pageFiles = []string{"requests", "request", "sessions", "session", "overview", "error"}

// parseTemplates builds one template set per page, each from the layout, every
// partial, and that one page. Go's html/template cannot redefine a block name
// within a single set, so a shared {{define "content"}} per page is only
// reachable with a set per page. Parsing happens here, at construction, so a
// broken template fails at process start and in every test that builds a
// Handler.
func parseTemplates() map[string]*stdhtml.Template {
	shared := globFiles("templates/partials/*.html")
	layout := readFile("templates/layout.html")

	out := make(map[string]*stdhtml.Template, len(pageFiles))
	for _, page := range pageFiles {
		src := layout + readFile("templates/pages/"+page+".html")
		for _, s := range shared {
			src += s
		}
		out[page] = stdhtml.Must(stdhtml.New(page).Funcs(funcs).Parse(src))
	}
	return out
}

// parsePartials builds the one set the partials are rendered from, for the
// htmx fragment responses. The layout is deliberately absent: a fragment is
// swapped into an existing page and must not carry a second <html> element.
func parsePartials() map[string]*stdhtml.Template {
	var src string
	for _, s := range globFiles("templates/partials/*.html") {
		src += s
	}
	return map[string]*stdhtml.Template{
		"fragments": stdhtml.Must(stdhtml.New("fragments").Funcs(funcs).Parse(src)),
	}
}

func globFiles(pattern string) []string {
	names, err := fs.Glob(assets, pattern)
	if err != nil {
		panic("ui: glob " + pattern + ": " + err.Error())
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, readFile(n))
	}
	return out
}

func readFile(name string) string {
	b, err := fs.ReadFile(assets, name)
	if err != nil {
		panic("ui: read " + name + ": " + err.Error())
	}
	return string(b)
}

// fragmentsRequested reports whether htmx made this request.
func fragmentsRequested(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// render writes a view. An htmx request gets just the named fragment; anything
// else gets the whole page, so a fragment's URL is still shareable, a reload
// still works, and the back button needs no bookkeeping. This is the only
// place the two response shapes are distinguished — there is no parallel
// /fragment/… route table.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, page, fragment string, data interface{}) {
	if fragmentsRequested(r) {
		if fragment == "" {
			h.fail(w, r, http.StatusBadRequest, "this URL has no fragment form")
			return
		}
		h.exec(w, r, h.fragments, "fragments", fragment, data)
		return
	}
	h.exec(w, r, h.pages, page, "layout", data)
}

// exec renders one template into a buffer and then writes it, so a failure
// part-way through cannot emit half a page under an already-committed 200.
func (h *Handler) exec(w http.ResponseWriter, r *http.Request, set map[string]*stdhtml.Template, setKey, name string, data interface{}) {
	tpl, ok := set[setKey]
	if !ok {
		h.fail(w, r, http.StatusInternalServerError, "no template set named "+setKey)
		return
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, name, data); err != nil {
		h.logger.LogError(r.Context(), "error", err,
			map[string]interface{}{"phase": "admin_ui_render", "template": name})
		h.fail(w, r, http.StatusInternalServerError, "rendering failed: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// A UI response is never cacheable: it is a live view of a store that a
	// running request may have just changed.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		h.logger.LogError(r.Context(), "warn", err,
			map[string]interface{}{"phase": "admin_ui_write", "template": name})
	}
}

// fail writes an error response as HTML. It does not go through the template
// sets: a renderer failure must not be reported by the renderer.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, code int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !fragmentsRequested(r) {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(code)
	body := `<p class="error">` + stdhtml.HTMLEscapeString(msg) + `</p>`
	if !fragmentsRequested(r) {
		body = `<!doctype html><meta charset="utf-8"><title>Arbiter</title>` +
			`<link rel="stylesheet" href="/admin/ui/static/app.css"><body class="fail"><h1>` +
			fmt.Sprint(code) + `</h1>` + body + `</body>`
	}
	if _, err := w.Write([]byte(body)); err != nil {
		h.logger.LogError(r.Context(), "warn", err, map[string]interface{}{"phase": "admin_ui_write_error"})
	}
}

// storeDisabled reports the disabled-store case, having already written the
// response for it: a 503 carrying a short HTML note for a fragment request,
// otherwise nothing (the caller renders its page with Base.StoreDisabled set,
// which the layout turns into a banner). Returns true when it handled the
// request.
func (h *Handler) storeDisabled(w http.ResponseWriter, r *http.Request) bool {
	if h.reader == nil && fragmentsRequested(r) {
		h.fail(w, r, http.StatusServiceUnavailable, storeDisabledNote)
		return true
	}
	return h.reader == nil
}

// storeDisabledNote is the one wording for "there is nothing to read", used by
// both the banner and the fragment note so they cannot drift.
const storeDisabledNote = "the event store is disabled: set storage.path in the config to record and browse requests"

// disabledReason returns the banner text only in the disabled case, so an
// enabled store renders no banner at all.
func disabledReason(disabled bool) string {
	if !disabled {
		return ""
	}
	return storeDisabledNote
}

// viewBase is carried by every page's view: what the layout needs for its
// chrome, plus the store-disabled state so any page can explain itself.
type viewBase struct {
	Title               string
	Nav                 []navItem
	Active              string
	AssetVersion        string
	StoreDisabled       bool
	StoreDisabledReason string
	Now                 time.Time
}

// navItem is one entry in the header navigation.
type navItem struct {
	Name string
	Href string
}

// base builds the common view state for a page.
func (h *Handler) base(active string) viewBase {
	return viewBase{
		Nav: []navItem{
			{Name: "Overview", Href: "/admin/ui/overview"},
			{Name: "Sessions", Href: "/admin/ui/sessions"},
			{Name: "Requests", Href: "/admin/ui/requests"},
		},
		Active:        active,
		AssetVersion:  h.assetVersion,
		Now:           time.Now().UTC(),
		StoreDisabled: h.reader == nil,
		// The reason string is set here rather than in the template so the
		// banner and the fragment note cannot drift apart.
		StoreDisabledReason: disabledReason(h.reader == nil),
	}
}
