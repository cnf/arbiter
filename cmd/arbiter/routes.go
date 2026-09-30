package main

import (
	"fmt"
	"log/slog"
	stdhttp "net/http"

	arbiterhttp "github.com/cnf/arbiter/internal/http"
	"github.com/cnf/arbiter/internal/ui"
	"github.com/gorilla/mux"
)

// newRouter registers every HTTP route. Routes are grouped here rather than
// inline in main so the /admin/* gate is exercised by a real request in tests
// rather than asserted by reading the wiring. forwardAuthHeader is the
// configured admin gate header ("" = ungated).
func newRouter(handler *arbiterhttp.Handler, admin *arbiterhttp.AdminHandler, stats *arbiterhttp.StatsHandler, adminUI *ui.Handler, forwardAuthHeader string) *mux.Router {
	r := mux.NewRouter()
	r.NotFoundHandler = stdhttp.HandlerFunc(notFoundJSON)
	r.HandleFunc("/", func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		stdhttp.Redirect(w, req, "/admin/ui/", stdhttp.StatusFound)
	}).Methods("GET")
	// Both chat endpoints are registered plain and /v1-prefixed, matching
	// /models below: a client's base_url convention (whether it already
	// includes /v1) shouldn't decide whether Arbiter has a route.
	r.HandleFunc("/v1/messages", handler.MessagesHandler).Methods("POST")
	r.HandleFunc("/messages", handler.MessagesHandler).Methods("POST")
	r.HandleFunc("/chat/completions", handler.CompletionsHandler).Methods("POST")
	r.HandleFunc("/v1/chat/completions", handler.CompletionsHandler).Methods("POST")
	r.HandleFunc("/models", handler.ModelsHandler).Methods("GET")
	r.HandleFunc("/v1/models", handler.ModelsHandler).Methods("GET")
	r.HandleFunc("/health", func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprint(w, `{"status":"ok"}`); err != nil {
			slog.Error("health response failed", "error", err)
		}
	}).Methods("GET")

	// Admin surface. Path-and-verb registered (not just path) so a reverse
	// proxy in front can match on either independently; Arbiter's own control
	// is the presence-only forward-auth gate.
	r.HandleFunc("/admin/reload", arbiterhttp.Gate(forwardAuthHeader, admin.ReloadHandler)).Methods("POST")
	// Clearing 429 backoff is a deliberate action, never a side effect of a
	// reload — see AdminHandler.ClearCooldownsHandler. POST-only, like reload, so
	// a fronting proxy can allow reads while denying this.
	r.HandleFunc("/admin/cooldowns/clear", arbiterhttp.Gate(forwardAuthHeader, admin.ClearCooldownsHandler)).Methods("POST")
	r.HandleFunc("/admin/stats", arbiterhttp.Gate(forwardAuthHeader, stats.OverallHandler)).Methods("GET")
	r.HandleFunc("/admin/stats/providers", arbiterhttp.Gate(forwardAuthHeader, stats.ProvidersHandler)).Methods("GET")
	r.HandleFunc("/admin/stats/epochs", arbiterhttp.Gate(forwardAuthHeader, stats.EpochsHandler)).Methods("GET")
	r.HandleFunc("/admin/stats/tools", arbiterhttp.Gate(forwardAuthHeader, stats.ToolsHandler)).Methods("GET")
	r.HandleFunc("/admin/stats/session", arbiterhttp.Gate(forwardAuthHeader, stats.SessionHandler)).Methods("GET")

	// Request list and detail. Same /admin/* gate and the same path-and-verb
	// registration, so a proxy can allow reads (GET) and the reload (POST)
	// independently. The detail route takes the store's rowid, which the list
	// returns as `id`.
	r.HandleFunc("/admin/requests", arbiterhttp.Gate(forwardAuthHeader, stats.RequestsHandler)).Methods("GET")
	r.HandleFunc("/admin/requests/{id}", arbiterhttp.Gate(forwardAuthHeader, stats.RequestHandler)).Methods("GET")

	// Content surface. Only useful with storage.capture_content on; without it
	// these return empty results rather than an error, because "no content
	// stored" is a legitimate answer and the endpoints are harmless.
	r.HandleFunc("/admin/content/repeated", arbiterhttp.Gate(forwardAuthHeader, stats.RepeatedContentHandler)).Methods("GET")

	// The admin UI. Same gate, same path-and-verb registration, so the whole
	// page can be allowed or denied by a fronting proxy alongside the JSON
	// reads it renders. Order matters: the concrete routes are registered
	// before the static PathPrefix, which would otherwise shadow them.
	//
	// The asset tree is behind the gate too, deliberately: an unauthenticated
	// peer should not be able to enumerate it any more than the data. The
	// consequence is that a direct-to-loopback browser with forward_auth_header
	// set gets an unstyled 401 on everything including the CSS — see the
	// README's admin section.
	r.HandleFunc("/admin/ui/", arbiterhttp.Gate(forwardAuthHeader, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		stdhttp.Redirect(w, req, "/admin/ui/overview", stdhttp.StatusFound)
	})).Methods("GET")
	r.HandleFunc("/admin/ui/requests/{id}/guardrail-diff", arbiterhttp.Gate(forwardAuthHeader, adminUI.GuardrailDiffHandler)).Methods("GET")

	// Conversations. The key is a query parameter, not a path segment: session
	// keys are opaque and may be arbitrary client-supplied header values, so a
	// `/` or a `:` in one would break the route. This mirrors /admin/stats/session.
	r.HandleFunc("/admin/ui/sessions", arbiterhttp.Gate(forwardAuthHeader, adminUI.SessionsHandler)).Methods("GET")
	r.HandleFunc("/admin/ui/sessions/tail", arbiterhttp.Gate(forwardAuthHeader, adminUI.SessionsTailHandler)).Methods("GET")
	r.HandleFunc("/admin/ui/session", arbiterhttp.Gate(forwardAuthHeader, adminUI.SessionHandler)).Methods("GET")

	// Overview (#54): the routing-flow page. The old pivot explorer and its
	// series.json chart endpoint were deleted with it — the rebuild is
	// greenfield, so nothing of that page survives to route to.
	r.HandleFunc("/admin/ui/overview", arbiterhttp.Gate(forwardAuthHeader, adminUI.OverviewHandler)).Methods("GET")
	// The per-node drawer, fetched on click. Registered before nothing in
	// particular, but kept adjacent to its page: it is a fragment-only endpoint
	// and has no full-page form.
	r.HandleFunc("/admin/ui/overview/node", arbiterhttp.Gate(forwardAuthHeader, adminUI.OverviewNodeHandler)).Methods("GET")
	r.HandleFunc("/admin/ui/overview/node/close", arbiterhttp.Gate(forwardAuthHeader, adminUI.OverviewNodeCloseHandler)).Methods("GET")

	// Config (#77 phase 1): a read-only YAML dump of the live, resolved
	// config. Does not depend on the event store, unlike every other page
	// here, so it is reachable even with storage.path unset.
	r.HandleFunc("/admin/ui/config", arbiterhttp.Gate(forwardAuthHeader, adminUI.ConfigHandler)).Methods("GET")

	// Discovery: the blocks that recur across requests, and the drill-down from
	// one block to the requests containing it. The block page takes ?hash= rather
	// than a path segment because a content hash is hex and long, and a segment
	// would need its own escaping rules for a value that is already opaque.
	r.HandleFunc("/admin/ui/content/repeated", arbiterhttp.Gate(forwardAuthHeader, adminUI.DiscoveryHandler)).Methods("GET")
	r.HandleFunc("/admin/ui/content/block", arbiterhttp.Gate(forwardAuthHeader, adminUI.BlockRequestsHandler)).Methods("GET")
	// The workspace pane's on-demand full-body fetch (#50 follow-up):
	// fragment-only, no full-page form, since the row's own htmx entry into
	// workspace mode is its only caller.
	r.HandleFunc("/admin/ui/content/block/body", arbiterhttp.Gate(forwardAuthHeader, adminUI.DiscoveryBlockBodyHandler)).Methods("GET")
	// The state-cycle endpoint: the first POST under /admin/ui/ — every other
	// route here is a read. Registered with its own Methods("POST") the same
	// way /admin/reload is, so a fronting proxy's forward_auth gate can allow
	// every GET under /admin/ui/ while still denying this one specifically, if
	// it chooses to draw that line.
	r.HandleFunc("/admin/ui/content/repeated/state", arbiterhttp.Gate(forwardAuthHeader, adminUI.DiscoverySetStateHandler)).Methods("POST")
	// PathPrefix, not HandleFunc: gorilla/mux's HandleFunc matches the exact
	// path, so a static route registered that way serves only "/static/" and
	// 404s every asset under it — which is exactly what a first version did.
	// Gate takes and returns an http.HandlerFunc, so the handler is passed as a
	// method value rather than as an http.Handler.
	r.PathPrefix("/admin/ui/static/").HandlerFunc(arbiterhttp.Gate(forwardAuthHeader, adminUI.StaticHandler)).Methods("GET")
	return r
}

// notFoundJSON answers an unmatched route with a JSON body instead of Go's
// default plain-text "404 page not found", so a client parsing every
// response as JSON (the common case for an API proxy) doesn't choke on one
// that isn't.
func notFoundJSON(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(stdhttp.StatusNotFound)
	_, _ = w.Write([]byte(`{"code":404,"detail":"Not Found"}`))
}
