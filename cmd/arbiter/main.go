package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	stdhttp "net/http"
	"os"
	"os/signal"
	"slices"
	"sort"
	"syscall"
	"time"

	"github.com/gorilla/mux"

	"github.com/cnf/arbiter/internal/classifier"
	"github.com/cnf/arbiter/internal/config"
	"github.com/cnf/arbiter/internal/guardrail"
	arbiterhttp "github.com/cnf/arbiter/internal/http"
	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/pipeline"
	"github.com/cnf/arbiter/internal/router"
	"github.com/cnf/arbiter/internal/store"
	"github.com/cnf/arbiter/internal/translator"
	"github.com/cnf/arbiter/internal/ui"
	"github.com/cnf/arbiter/internal/upstream"
	"github.com/cnf/arbiter/pkg/types"
)

func main() {
	configPath := flag.String("config", "arbiter.yaml", "Path to arbiter.yaml config file")
	port := flag.String("port", "8080", "Port to listen on (TCP)")
	bind := flag.String("bind", "127.0.0.1", "Interface to bind for TCP (e.g. 0.0.0.0 to expose)")
	socket := flag.String("socket", "", "Unix socket path to listen on instead of TCP; overrides --bind/--port")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	logger := logging.NewStdoutLogger(cfg.Logging.Level)
	slog.Info("Arbiter starting", "config", *configPath, "port", *port)

	// The event store is opened once, here, and shared across reloads: a
	// reload rebuilds the pipeline but must not reopen (or leak) the database
	// handle. storage.path is therefore fixed at startup — a reload that
	// changes it is ignored for the store, though every other config change
	// still applies.
	writer, err := openStore(cfg, logger)
	if err != nil {
		slog.Error("failed to open event store", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := writer.Close(); err != nil {
			slog.Error("failed to close event store", "error", err)
		}
	}()

	// The stats reader opens its own handle on the same database file the
	// writer feeds, rather than sharing the writer's handle, so a read never
	// contends with the writer's single drain goroutine. A disabled store
	// (storage.path unset) leaves it nil; the stats handlers report 503.
	//
	// Opened BEFORE the pipeline because a rate-limit guardrail seeds its counters
	// from it — otherwise a per-day cap would reset on every config reload.
	var reader *store.Reader
	if cfg.Storage.Path != "" {
		reader, err = store.OpenReader(cfg.Storage.Path)
		if err != nil {
			slog.Error("failed to open event store for reading", "error", err)
			os.Exit(1)
		}
		defer func() {
			if err := reader.Close(); err != nil {
				slog.Error("failed to close stats reader", "error", err)
			}
		}()
	}

	// Created once and shared across reloads, so a provider's 429 backoff is not
	// thrown away every time the config file is saved.
	cooldowns := pipeline.NewCooldownStore()
	p, err := buildPipeline(cfg, logger, writer, cooldowns, reader)
	if err != nil {
		slog.Error("failed to build pipeline", "error", err)
		os.Exit(1)
	}
	handler := arbiterhttp.NewHandler(arbiterhttp.NewRuntime(p, configuredModels(cfg), cfg.SessionAffinity.Header), logger)
	admin := arbiterhttp.NewAdminHandler(func(ctx context.Context) error {
		return reload(ctx, *configPath, handler, logger, writer, cooldowns, reader)
	}, logger)
	// The cooldown reset is bound to the shared store, so it clears the state the
	// live pipeline is actually using.
	admin.SetClearCooldowns(cooldowns.ClearForAdmin)

	stats := arbiterhttp.NewStatsHandler(reader, logger)

	// The admin UI reads the same Reader as the JSON surface. It is a separate
	// package rather than more handlers on stats because it brings its own
	// embedded templates and assets; its dependencies are identical.
	adminUI := ui.New(reader, logger)

	// Content retention runs on its own goroutine and its own connection, so an
	// expiry sweep never blocks a request. With no TTL configured, content never
	// expires and no sweeper is started — an upgrade must not silently begin
	// deleting data.
	sweepCtx, stopSweeper := context.WithCancel(context.Background())
	defer stopSweeper()
	startSweeper(sweepCtx, cfg.Storage.Path, cfg.Storage.ContentTTL, logger)

	r := newRouter(handler, admin, stats, adminUI, cfg.Admin.ForwardAuthHeader)

	srv := &stdhttp.Server{
		Handler: r,
		// Deliberately no WriteTimeout: it caps the entire response write,
		// which on an SSE stream means severing a long generation mid-flight
		// at an arbitrary wall-clock point. A stream ends when the upstream
		// finishes, the client disconnects (the request context), or the
		// provider's idle timeout fires — never on a fixed total duration.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := listen(*socket, *bind, *port)
	if err != nil {
		slog.Error("failed to listen", "error", err)
		os.Exit(1)
	}

	go func() {
		slog.Info("listening", "addr", ln.Addr().String(), "network", ln.Addr().Network())
		if err := srv.Serve(ln); err != nil && err != stdhttp.ErrServerClosed {
			slog.Error("server error", "error", err)
		}
	}()

	// Hot-reload arbiter.yaml for the life of the process; errors are logged and
	// a failed reload leaves the running config in place, but a watcher that
	// can't start at all is loud enough to be worth noticing.
	watchCtx, stopWatch := context.WithCancel(context.Background())
	defer stopWatch()
	go func() {
		if err := watchConfig(watchCtx, *configPath, handler, logger, writer, cooldowns, reader); err != nil {
			slog.Error("config watcher stopped", "error", err)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	stopWatch()

	slog.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("shutdown error", "error", err)
		os.Exit(1)
	}

	slog.Info("shutdown complete")
}

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
		stdhttp.Redirect(w, req, "/admin/ui/requests", stdhttp.StatusFound)
	})).Methods("GET")
	r.HandleFunc("/admin/ui/requests", arbiterhttp.Gate(forwardAuthHeader, adminUI.RequestsHandler)).Methods("GET")
	// The live tail's poll endpoint. Polled by live.js rather than by htmx, for
	// the reasons in that file; it returns JSON carrying a rendered row fragment,
	// so the cursor stays an opaque token and the row markup has one definition.
	//
	// It is registered *before* /requests/{id} because gorilla/mux matches in
	// registration order, and {id} happily matches the literal "tail" — so the
	// other order routes every poll into the detail handler, which then refuses
	// "tail" as a request id and answers 400.
	r.HandleFunc("/admin/ui/requests/tail", arbiterhttp.Gate(forwardAuthHeader, adminUI.TailHandler)).Methods("GET")
	r.HandleFunc("/admin/ui/requests/{id}", arbiterhttp.Gate(forwardAuthHeader, adminUI.RequestHandler)).Methods("GET")
	r.HandleFunc("/admin/ui/requests/{id}/content", arbiterhttp.Gate(forwardAuthHeader, adminUI.RequestContentHandler)).Methods("GET")

	// Conversations. The key is a query parameter, not a path segment: session
	// keys are opaque and may be arbitrary client-supplied header values, so a
	// `/` or a `:` in one would break the route. This mirrors /admin/stats/session.
	r.HandleFunc("/admin/ui/sessions", arbiterhttp.Gate(forwardAuthHeader, adminUI.SessionsHandler)).Methods("GET")
	r.HandleFunc("/admin/ui/session", arbiterhttp.Gate(forwardAuthHeader, adminUI.SessionHandler)).Methods("GET")

	// The pivot explorer. No /series.json yet: 7b-3a is the table, and the chart
	// endpoint arrives with the chart (and with a query that does not exist).
	r.HandleFunc("/admin/ui/overview", arbiterhttp.Gate(forwardAuthHeader, adminUI.OverviewHandler)).Methods("GET")

	// The chart's data. UI-internal and explicitly unstable: the shape can change
	// with the chart, which is why it is not under /admin/stats/* with the
	// documented read surface. Under the same gate as everything else.
	r.HandleFunc("/admin/ui/overview/series.json", arbiterhttp.Gate(forwardAuthHeader, adminUI.SeriesHandler)).Methods("GET")

	// Discovery: the blocks that recur across requests, and the drill-down from
	// one block to the requests containing it. The block page takes ?hash= rather
	// than a path segment because a content hash is hex and long, and a segment
	// would need its own escaping rules for a value that is already opaque.
	r.HandleFunc("/admin/ui/content/repeated", arbiterhttp.Gate(forwardAuthHeader, adminUI.DiscoveryHandler)).Methods("GET")
	r.HandleFunc("/admin/ui/content/block", arbiterhttp.Gate(forwardAuthHeader, adminUI.BlockRequestsHandler)).Methods("GET")
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

// listen opens the server's listener: a unix socket when socketPath is set
// (overriding the TCP bind/port), else TCP on bind:port. The default bind is
// loopback — Arbiter is meant to sit behind Caddy/tailscale, and a listener
// reachable from anywhere would let a peer forge the admin gate's
// forward-auth header (and reach the chat endpoints) directly.
func listen(socketPath, bind, port string) (net.Listener, error) {
	if socketPath != "" {
		// Remove a stale socket from a previous run; bind fails if the path
		// exists even when nothing is listening on it.
		if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove stale socket %s: %w", socketPath, err)
		}
		ln, err := net.Listen("unix", socketPath)
		if err != nil {
			return nil, fmt.Errorf("listen unix %s: %w", socketPath, err)
		}
		return ln, nil
	}
	addr := net.JoinHostPort(bind, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen tcp %s: %w", addr, err)
	}
	return ln, nil
}

// storeHandle is a Writer that also owns resources main must release at
// shutdown. Both concrete writers satisfy it, so main treats the enabled and
// disabled cases identically.
type storeHandle interface {
	store.Writer
	Close() error
}

// openStore returns the event-store writer named by config: a SQLiteWriter
// when storage.path is set, else a NoopWriter so nothing is persisted and the
// pipeline needs no nil checks.
func openStore(cfg *config.Config, logger logging.Logger) (storeHandle, error) {
	if cfg.Storage.Path == "" {
		slog.Info("event store disabled (storage.path unset)")
		return store.NoopWriter{}, nil
	}
	w, err := store.NewSQLiteWriter(cfg.Storage.Path, logger)
	if err != nil {
		return nil, err
	}
	slog.Info("event store enabled", "path", cfg.Storage.Path)
	if cfg.Storage.CaptureContent {
		slog.Info("content capture enabled",
			"content_ttl", cfg.Storage.ContentTTL,
			"note", "prompt and response bodies are stored; empty content_ttl means never expire")
	}
	return w, nil
}

// sweepInterval is how often expired captured content is reclaimed. Hourly is
// far more often than a TTL measured in days needs, and cheap: the sweep is two
// indexed deletes.
const sweepInterval = time.Hour

// startSweeper runs the background reclamation sweep in the background until ctx
// is done: expired content (only when a content TTL is configured) and expired
// affinity pins, which have their own deadlines and so are swept regardless.
// It runs once immediately so a long-idle store is reclaimed at startup rather
// than after the first interval.
//
// Only a non-zero content TTL sweeps content; with TTL unset ("never expire") a
// content sweep could only ever delete nothing. Pins changed the shape of this:
// they expire unconditionally, so the goroutine now always starts when a store
// is configured, and it is the content half that is conditional.
// Pins are swept unconditionally because their expiry is not opt-in — every pin
// carries an absolute deadline, so a row that outlives it is dead weight whether
// or not content retention is configured. Correctness does not depend on this
// running (LoadPin rejects an expired pin on its own), but without it the table
// grows forever.
//
// One goroutine, not two: both are indexed deletes on the same database, and a
// second ticker would add contention for no benefit.
func startSweeper(ctx context.Context, path, ttlSpec string, logger logging.Logger) {
	if path == "" {
		return
	}

	// A separate reader connection from the writer's: the sweep must not
	// contend with the drain goroutine, and it is exactly the read-while-writing
	// case the shared DSN's WAL + busy_timeout exist for.
	reader, err := store.OpenReader(path)
	if err != nil {
		slog.Error("cannot start reclamation sweeper", "error", err)
		return
	}

	// A content TTL is optional; an invalid one is reported and then ignored
	// (content simply never expires) rather than preventing the pin sweep.
	var contentTTL time.Duration
	if ttlSpec != "" {
		d, err := time.ParseDuration(ttlSpec)
		switch {
		case err != nil:
			slog.Error("content_ttl is not a valid duration; content will not expire",
				"content_ttl", ttlSpec, "error", err)
		case d <= 0:
			// Zero/negative means "never expire", which is the documented way to
			// keep content forever.
		default:
			contentTTL = d
		}
	}

	go func() {
		defer func() { _ = reader.Close() }()
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			if contentTTL > 0 {
				bodies, refs, err := reader.SweepContent(ctx, contentTTL)
				if err != nil {
					if ctx.Err() == nil {
						logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "content_sweep"})
					}
				} else if refs > 0 || bodies > 0 {
					slog.Info("expired captured content", "refs", refs, "bodies", bodies)
				}
			}

			pins, err := reader.SweepPins(ctx)
			if err != nil {
				if ctx.Err() == nil {
					logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "pin_sweep"})
				}
			} else if pins > 0 {
				slog.Info("expired session pins", "pins", pins)
			}

			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// buildPipeline turns config into a fully wired Pipeline: provider table,
// translator, classifiers, router, guardrails, upstream client. This is the
// one place that knows how config type-names map to concrete constructors —
// adding a new classifier/router/guardrail type means adding a case here
// (or, once there's a reason to, registering it into router.Registry /
// classifier.Registry / guardrail.Registry instead of switching on it).
func buildPipeline(cfg *config.Config, logger logging.Logger, writer store.Writer, cooldowns *pipeline.CooldownStore, reader *store.Reader) (*pipeline.Pipeline, error) {
	providers := make(map[string]types.ProviderConfig, len(cfg.Providers))
	for name, pc := range cfg.Providers {
		timeout := 60 * time.Second
		if pc.Timeout != "" {
			if d, err := time.ParseDuration(pc.Timeout); err == nil {
				timeout = d
			}
		}
		var cacheTTL time.Duration
		if pc.CacheTTL != "" {
			if d, err := time.ParseDuration(pc.CacheTTL); err == nil {
				cacheTTL = d
			}
		}
		providers[name] = types.ProviderConfig{
			Name:     name,
			Type:     pc.Type,
			Endpoint: pc.Endpoint,
			APIKey:   pc.Key,
			Models:   pc.Models,
			Headers:  pc.Headers,
			Timeout:  timeout,
			RetryMax: pc.RetryMax,
			CacheTTL: cacheTTL,
		}
	}

	catalog := modelCostEntries(cfg.ModelCatalog)
	resolver := buildAliasResolver(cfg.Aliases, providers, catalog)

	// The catalog also answers the pipeline's cost computation: when an
	// upstream reports no cost (plain Anthropic/OpenAI), the pipeline prices
	// the request from these same rows. nil when no catalog is configured.
	var costLookup router.CostLatencyLookup
	if len(catalog) > 0 {
		costLookup = router.NewStaticCatalog(catalog)
	}

	// Built here rather than lower down: a model-backed classifier ("llm",
	// "decisions") needs a real client to route its own classification calls
	// through, and resolver/providers to resolve the alias it's configured
	// against — the same three things every other classifier type doesn't need
	// at all. One HTTPClient serves both roles: a decisions call is a third
	// method on it, not a second client.
	t := translator.NewDefaultTranslator()
	u := upstream.NewHTTPClient(t)

	classifiers, err := buildClassifiers(cfg.Classifiers, resolver, providers, u, u)
	if err != nil {
		return nil, err
	}

	routers := make([]router.Router, 0, len(cfg.Routers))
	for _, rc := range cfg.Routers {
		r, err := buildRouter(rc, providers, resolver, costLookup)
		if err != nil {
			return nil, fmt.Errorf("router %q: %w", rc.Name, err)
		}
		routers = append(routers, r)
	}
	if len(routers) == 0 {
		return nil, fmt.Errorf("no routers configured")
	}
	mainRouter := combineRouters(routers)

	preGuardrails := make([]guardrail.Guardrail, 0, len(cfg.Guardrails.Pre))
	for _, gc := range cfg.Guardrails.Pre {
		g, err := buildGuardrail(gc, countSource(reader))
		if err != nil {
			return nil, fmt.Errorf("guardrail %q: %w", gc.Name, err)
		}
		preGuardrails = append(preGuardrails, g)
	}
	postGuardrails := make([]guardrail.Guardrail, 0, len(cfg.Guardrails.Post))
	for _, gc := range cfg.Guardrails.Post {
		g, err := buildGuardrail(gc, countSource(reader))
		if err != nil {
			return nil, fmt.Errorf("guardrail %q: %w", gc.Name, err)
		}
		postGuardrails = append(postGuardrails, g)
	}

	var defaultCacheTTL time.Duration
	if cfg.SessionAffinity.DefaultTTL != "" {
		if d, err := time.ParseDuration(cfg.SessionAffinity.DefaultTTL); err == nil {
			defaultCacheTTL = d
		}
	}

	// Affinity pins are persisted so they survive a reload (which rebuilds this
	// whole pipeline) and a restart. With no store configured there is nothing
	// to persist to, and pins stay in memory — today's behaviour, and the
	// correct degradation.
	var pinner pipeline.Pinner
	if sp, ok := writer.(store.Pinner); ok {
		pinner = affinityPinner{sp}
	}

	p := pipeline.NewPipeline(t, t, t, classifiers, mainRouter, u, providers, cfg.Routing.FallbackProviders, preGuardrails, postGuardrails, logger, defaultCacheTTL, resolver, writer, costLookup, pinner, cooldowns)
	// Every event this pipeline records is stamped with the hash of the config
	// that built it, so spend can be compared across config changes.
	p.SetConfigEpoch(cfg.Epoch())
	// Body capture is wiring-time policy, so it rides the same path as the
	// epoch rather than joining the constructor's positional arguments.
	p.SetCaptureContent(cfg.Storage.CaptureContent)
	return p, nil
}

// affinityPinner adapts the store's pin API onto the pipeline's narrow Pinner
// interface.
//
// The two use different record types on purpose — the store must not import the
// pipeline (the pipeline already imports the store), and a shared type would
// make the persisted format and the pipeline's internal struct change together
// by accident. The adapter is the one place that knows both, so a change to
// either surface is a compile error here rather than silent data drift.
type affinityPinner struct{ s store.Pinner }

func (a affinityPinner) SavePin(ctx context.Context, p pipeline.AffinityPinRecord) error {
	return a.s.SavePin(ctx, store.AffinityPin{
		SessionKey:     p.SessionKey,
		RequestedModel: p.RequestedModel,
		Provider:       p.Provider,
		Model:          p.Model,
		ExpiresAt:      p.ExpiresAt,
	})
}

func (a affinityPinner) LoadPin(ctx context.Context, sessionKey string) (pipeline.AffinityPinRecord, bool, error) {
	rec, ok, err := a.s.LoadPin(ctx, sessionKey)
	if err != nil || !ok {
		return pipeline.AffinityPinRecord{}, ok, err
	}
	return pipeline.AffinityPinRecord{
		SessionKey:     rec.SessionKey,
		RequestedModel: rec.RequestedModel,
		Provider:       rec.Provider,
		Model:          rec.Model,
		ExpiresAt:      rec.ExpiresAt,
	}, true, nil
}

func (a affinityPinner) DeletePin(ctx context.Context, sessionKey string) error {
	return a.s.DeletePin(ctx, sessionKey)
}

func combineRouters(routers []router.Router) router.Router {
	if len(routers) == 1 {
		return routers[0]
	}
	return router.NewChainedRouter("chained", routers)
}

// configuredModels lists every model a client can name in a request:
// concrete provider models, plus every configured alias — force-aliases
// included, since REQUIREMENTS.md §1 makes aliases client-facing regardless
// of shape. An alias is advertised with Provider "alias" rather than a
// resolved target, since group/force aliases don't resolve to one fixed
// provider.
func configuredModels(cfg *config.Config) []arbiterhttp.Model {
	// Catalog rows carry what each model can do, keyed provider+model exactly
	// as the provider's declared model list is. Built into a lookup first so
	// the model list stays a single pass and an unmatched model simply has no
	// capability data (unknown, not none).
	type capInfo struct {
		modalities []string
		maxIn      *int
		maxOut     *int
		metadata   map[string]interface{}
	}
	byModel := make(map[string]capInfo, len(cfg.ModelCatalog))
	for _, e := range cfg.ModelCatalog {
		byModel[e.Provider+"\x00"+e.Model] = capInfo{
			modalities: e.InputModalities,
			maxIn:      e.MaxInputTokens,
			maxOut:     e.MaxOutputTokens,
			metadata:   e.Metadata,
		}
	}

	models := make([]arbiterhttp.Model, 0)
	for provider, providerConfig := range cfg.Providers {
		for _, model := range providerConfig.Models {
			m := arbiterhttp.Model{ID: model, Provider: provider}
			if c, ok := byModel[provider+"\x00"+model]; ok {
				m.InputModalities = c.modalities
				m.MaxInputTokens = c.maxIn
				m.MaxOutputTokens = c.maxOut
				m.Metadata = c.metadata
			}
			models = append(models, m)
		}
	}
	// Aliases carry no capability data of their own: an alias resolves to a
	// target at request time, so what it can accept depends on where it lands.
	// Left unknown rather than guessed at from the members.
	for name := range cfg.Aliases {
		models = append(models, arbiterhttp.Model{ID: name, Provider: "alias"})
	}
	slices.SortFunc(models, func(a, b arbiterhttp.Model) int {
		if a.ID != b.ID {
			return cmp.Compare(a.ID, b.ID)
		}
		return cmp.Compare(a.Provider, b.Provider)
	})
	return models
}

// buildClassifiers builds every configured classifier in two passes: every
// non-model-backed type first (indexed by name), then every type that makes its
// own upstream call ("llm", "decisions"), resolving its named `fallback` from
// that index. Two passes rather than one so a model-backed classifier's
// fallback is guaranteed to exist regardless of which one is declared first in
// config — the final list is still assembled in declared order, only the
// dependency resolution is two-phase. A fallback naming another model-backed
// classifier is rejected for "llm"; a "decisions" classifier may fall back to
// an "llm" one (see config's validateDecisionsClassifiers).
func buildClassifiers(ccs []config.ClassifierConfig, resolver *router.AliasResolver, providers map[string]types.ProviderConfig, u upstream.Client, decisions upstream.DecisionClient) ([]classifier.Classifier, error) {
	byName := make(map[string]classifier.Classifier, len(ccs))
	for _, cc := range ccs {
		if isModelBackedClassifier(cc.Type) {
			continue
		}
		c, err := buildClassifier(cc)
		if err != nil {
			return nil, fmt.Errorf("classifier %q: %w", cc.Name, err)
		}
		byName[cc.Name] = c
	}

	ordered := make([]classifier.Classifier, 0, len(ccs))
	for _, cc := range ccs {
		if !isModelBackedClassifier(cc.Type) {
			ordered = append(ordered, byName[cc.Name])
			continue
		}
		var (
			c   classifier.Classifier
			err error
		)
		switch cc.Type {
		case "decisions":
			c, err = buildDecisionsClassifier(cc, resolver, providers, decisions, byName)
		default:
			c, err = buildLLMClassifier(cc, resolver, providers, u, byName)
		}
		if err != nil {
			return nil, fmt.Errorf("classifier %q: %w", cc.Name, err)
		}
		ordered = append(ordered, c)
	}
	return ordered, nil
}

// isModelBackedClassifier reports whether a type makes its own upstream call
// and therefore needs the second construction pass (and a non-model fallback).
func isModelBackedClassifier(typeName string) bool {
	return typeName == "llm" || typeName == "decisions"
}

// buildClassifier's axis defaulting preserves pre-axis behavior: a plain
// "heuristic" classifier with no declared axis fills Domain, and the legacy
// "capability_detector" type always fills Capabilities regardless of what's
// declared (it never meant anything else). Does not handle the model-backed
// types ("llm", "decisions") — those need the resolver/providers/upstream
// client buildClassifiers threads in, which is why each has its own builder and
// its own pass.
func buildClassifier(cc config.ClassifierConfig) (classifier.Classifier, error) {
	switch cc.Type {
	case "heuristic":
		keywords, err := stringListMap(cc.Config, "keywords", "detectors")
		if err != nil {
			return nil, err
		}
		return classifier.NewHeuristicClassifier(cc.Name, cc.Axis, keywords), nil
	case "capability_detector":
		keywords, err := stringListMap(cc.Config, "keywords", "detectors")
		if err != nil {
			return nil, err
		}
		return classifier.NewHeuristicClassifier(cc.Name, classifier.AxisCapabilities, keywords), nil
	default:
		return nil, fmt.Errorf("unknown classifier type %q", cc.Type)
	}
}

// buildLLMClassifier builds an "llm" classifier: alias (required) names the
// configured alias its classification calls route through, labels (required,
// non-empty) is the set of values it may return — bare names, or name ->
// rubric-description pairs — escape (optional) names the label meaning "no
// category fits", instructions (optional) replaces the default framing
// sentence, fallback (required) names another classifier already built in
// buildClassifiers' first pass, and timeout is an optional Go duration
// (defaults inside NewLLMClassifier).
func buildLLMClassifier(cc config.ClassifierConfig, resolver *router.AliasResolver, providers map[string]types.ProviderConfig, u upstream.Client, byName map[string]classifier.Classifier) (classifier.Classifier, error) {
	alias, _ := cc.Config["alias"].(string)
	if alias == "" {
		return nil, fmt.Errorf(`"llm" classifier requires "alias"`)
	}
	// The same parser config validation uses, so the two cannot disagree about
	// what a labels block means (see types.ParseLabels).
	labels, err := types.ParseLabels(cc.Config["labels"])
	if err != nil {
		return nil, fmt.Errorf("invalid labels: %w", err)
	}
	if len(labels) == 0 {
		return nil, fmt.Errorf(`"llm" classifier requires a non-empty "labels" list`)
	}
	escape, _ := cc.Config["escape"].(string)
	instructions, _ := cc.Config["instructions"].(string)
	fallbackName, _ := cc.Config["fallback"].(string)
	if fallbackName == "" {
		return nil, fmt.Errorf(`"llm" classifier requires "fallback"`)
	}
	fallback, ok := byName[fallbackName]
	if !ok {
		return nil, fmt.Errorf("fallback %q is not a configured non-llm classifier (must be declared, and must not itself be type \"llm\")", fallbackName)
	}
	var timeout time.Duration
	if raw, _ := cc.Config["timeout"].(string); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid timeout %q: %w", raw, err)
		}
		timeout = d
	}
	return classifier.NewLLMClassifier(cc.Name, cc.Axis, resolver, alias, u, providers, labels, escape, instructions, fallback, timeout), nil
}

// buildDecisionsClassifier builds a "decisions" classifier: alias (required)
// names the configured alias its decision calls route through — which must
// resolve to a provider of type "decisions" (config validation enforces that),
// questions (required) are the typed questions asked in ONE call, each with its
// own `axis` choosing the Signals field it fills, escape (optional) names the
// label meaning "no option fits", instructions (optional) is the question's
// framing sentence, fallback (required) names another classifier already built
// in buildClassifiers' first pass, and timeout is an optional Go duration
// (defaults inside NewDecisionsClassifier).
//
// Question order is sorted by name rather than taken from map iteration, so the
// recorded prompt and the rationale are stable across calls — the same reason
// the LLM classifier sorts its labels.
func buildDecisionsClassifier(cc config.ClassifierConfig, resolver *router.AliasResolver, providers map[string]types.ProviderConfig, decisions upstream.DecisionClient, byName map[string]classifier.Classifier) (classifier.Classifier, error) {
	alias, _ := cc.Config["alias"].(string)
	if alias == "" {
		return nil, fmt.Errorf(`"decisions" classifier requires "alias"`)
	}
	rawQuestions, ok := cc.Config["questions"].(map[string]interface{})
	if !ok || len(rawQuestions) == 0 {
		return nil, fmt.Errorf(`"decisions" classifier requires "questions"`)
	}

	names := make([]string, 0, len(rawQuestions))
	for name := range rawQuestions {
		names = append(names, name)
	}
	sort.Strings(names)

	questions := make([]classifier.DecisionQuestionConfig, 0, len(names))
	for _, qname := range names {
		q, ok := rawQuestions[qname].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("question %q must be a map", qname)
		}
		axis, _ := q["axis"].(string)
		if axis == "" {
			return nil, fmt.Errorf("question %q requires \"axis\"", qname)
		}
		// The same parser config validation uses, so the two cannot disagree
		// about what a labels block means (see types.ParseLabels).
		labels, err := types.ParseLabels(q["labels"])
		if err != nil {
			return nil, fmt.Errorf("question %q: invalid labels: %w", qname, err)
		}
		if len(labels) == 0 {
			return nil, fmt.Errorf("question %q requires a non-empty \"labels\" list", qname)
		}
		qtype, _ := q["type"].(string)
		if qtype == "" {
			qtype = types.DecisionChoice
		}
		escape, _ := q["escape"].(string)
		instructions, _ := q["instructions"].(string)
		questions = append(questions, classifier.DecisionQuestionConfig{
			Name: qname, Axis: axis, Type: qtype,
			Labels: labels, Escape: escape, Instructions: instructions,
		})
	}

	fallbackName, _ := cc.Config["fallback"].(string)
	if fallbackName == "" {
		return nil, fmt.Errorf(`"decisions" classifier requires "fallback"`)
	}
	fallback, ok := byName[fallbackName]
	if !ok {
		return nil, fmt.Errorf("fallback %q is not a configured non-model classifier (must be declared, and must not itself be a model-backed type)", fallbackName)
	}
	var timeout time.Duration
	if raw, _ := cc.Config["timeout"].(string); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid timeout %q: %w", raw, err)
		}
		timeout = d
	}
	return classifier.NewDecisionsClassifier(cc.Name, resolver, alias, decisions, providers, questions, fallback, timeout), nil
}

// buildAliasResolver builds the resolver used by policy routers to resolve
// rule targets. Aliases are optional — an empty/nil map still yields a
// resolver (Has/Resolve simply report "not an alias" for everything), so
// policy routers that only use literal provider/model targets don't need one
// at all; the resolver is nonetheless always built and passed through so a
// nil isn't threaded separately.
func buildAliasResolver(aliasesCfg map[string]config.AliasConfig, providers map[string]types.ProviderConfig, catalog []types.ModelCost) *router.AliasResolver {
	aliases := make(map[string]router.Alias, len(aliasesCfg))
	for name, a := range aliasesCfg {
		alias := router.Alias{
			Name:     name,
			Force:    a.Force,
			Type:     a.Type,
			Provider: a.Provider,
			Model:    a.Model,
			Select:   a.Select,
		}
		for _, m := range a.Members {
			alias.Members = append(alias.Members, router.AliasMember{Provider: m.Provider, Model: m.Model})
		}
		aliases[name] = alias
	}
	var lookup router.CostLatencyLookup
	if len(catalog) > 0 {
		lookup = router.NewStaticCatalog(catalog)
	}
	return router.NewAliasResolver(aliases, providers, nil, lookup)
}

// modelCostEntries converts the config's catalog rows into the type the
// router's lookup consumes.
func modelCostEntries(entries []config.ModelCatalogEntry) []types.ModelCost {
	out := make([]types.ModelCost, 0, len(entries))
	for _, e := range entries {
		out = append(out, types.ModelCost{
			Provider:          e.Provider,
			Model:             e.Model,
			InputCostPerMTok:  e.InputCostPerMTok,
			OutputCostPerMTok: e.OutputCostPerMTok,
			LatencyMsP50:      e.LatencyMsP50,
			InputModalities:   e.InputModalities,
			MaxInputTokens:    e.MaxInputTokens,
			MaxOutputTokens:   e.MaxOutputTokens,
			Metadata:          e.Metadata,
		})
	}
	return out
}

func buildRouter(rc config.RouterConfig, providers map[string]types.ProviderConfig, resolver *router.AliasResolver, catalog router.CostLatencyLookup) (router.Router, error) {
	switch rc.Type {
	case "simple":
		defaultProvider, _ := rc.Config["default_provider"].(string)
		fallbackProvider, _ := rc.Config["fallback_provider"].(string)
		if defaultProvider == "" {
			return nil, fmt.Errorf("missing default_provider")
		}
		return router.NewSimpleRouter(rc.Name, defaultProvider, fallbackProvider, providers), nil
	case "policy":
		rules, err := policyRules(rc.Config)
		if err != nil {
			return nil, err
		}
		return router.NewPolicyRouter(rc.Name, rules, providers, resolver, catalog), nil
	default:
		return nil, fmt.Errorf("unknown router type %q", rc.Type)
	}
}

// policyRules parses the "rules" list out of a policy router's config block.
// Each rule's "when" clause is optional per-field (a missing field is a
// wildcard); see router.PolicyCondition. A rule's target is either a named
// alias ("target") or a literal provider/model ("provider"/"model") — not
// both. Both the current ("domain"/"cost_class") and deprecated
// ("intent"/"cost_sensitivity") when-clause key spellings are accepted
// during the deprecation window; setting both spellings of the same axis on
// one rule is an error rather than silently picking one.
func policyRules(cfg map[string]interface{}) ([]router.PolicyRule, error) {
	raw, _ := cfg["rules"].([]interface{})
	rules := make([]router.PolicyRule, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("rule %d: expected a map", i)
		}

		target, _ := m["target"].(string)
		provider, _ := m["provider"].(string)
		model, _ := m["model"].(string)
		if target != "" && provider != "" {
			return nil, fmt.Errorf("rule %d: sets both target and provider; use exactly one", i)
		}
		if target == "" && provider == "" {
			return nil, fmt.Errorf("rule %d: missing target or provider", i)
		}

		var when router.PolicyCondition
		if w, ok := m["when"].(map[string]interface{}); ok {
			domain, err := stringOneOf(w, "domain", "intent")
			if err != nil {
				return nil, fmt.Errorf("rule %d: %w", i, err)
			}
			when.Domain = domain

			costClass, err := stringOneOf(w, "cost_class", "cost_sensitivity")
			if err != nil {
				return nil, fmt.Errorf("rule %d: %w", i, err)
			}
			when.CostClass = costClass

			when.Effort, _ = w["effort"].(string)
			if caps, ok := w["capabilities"].([]interface{}); ok {
				for _, c := range caps {
					if s, ok := c.(string); ok {
						when.Capabilities = append(when.Capabilities, s)
					}
				}
			}
			// Guards the rule's target rather than matching the request — see
			// PolicyCondition.RequiresInputModalities. Named distinctly from
			// `capabilities` because the two vocabularies differ: signals say
			// what the request needs, modalities say what a model accepts.
			if mods, ok := w["requires_input_modalities"].([]interface{}); ok {
				for _, c := range mods {
					if s, ok := c.(string); ok {
						when.RequiresInputModalities = append(when.RequiresInputModalities, s)
					}
				}
			}
		}

		rules = append(rules, router.PolicyRule{When: when, Target: target, Provider: provider, Model: model})
	}
	return rules, nil
}

// stringOneOf reads a string value from exactly one of the given keys,
// erroring if more than one is set on the same map — used to accept a
// deprecated YAML key spelling alongside its replacement without silently
// preferring one when a config mistakenly sets both.
func stringOneOf(m map[string]interface{}, keys ...string) (string, error) {
	var value, foundKey string
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			if foundKey != "" {
				return "", fmt.Errorf("both %q and %q are set; use only %q", foundKey, k, keys[0])
			}
			value, foundKey = v, k
		}
	}
	return value, nil
}

// countSource converts a possibly-nil *store.Reader into a guardrail.CountSource.
//
// The conversion is explicit because a nil *store.Reader assigned to an interface
// produces a NON-nil interface holding a nil pointer — so the guardrail's own
// `counts == nil` check would pass and then panic on first use. Returning a true
// nil interface when there is no reader keeps the "no store configured" case
// behaving as the guardrail documents.
func countSource(r *store.Reader) guardrail.CountSource {
	if r == nil {
		return nil
	}
	return r
}

func buildGuardrail(gc config.GuardrailConfig, counts guardrail.CountSource) (guardrail.Guardrail, error) {
	switch gc.Type {
	case "system_prompt":
		prompt, _ := gc.Config["prompt"].(string)
		override, _ := gc.Config["override"].(bool)
		return guardrail.NewSystemPromptGuardrail(gc.Name, prompt, override), nil
	case "rate_limit":
		perMinute := intFromConfig(gc.Config, "per_minute")
		perDay := intFromConfig(gc.Config, "per_day")
		return guardrail.NewRateLimitGuardrail(gc.Name, perMinute, perDay, counts), nil
	case "prompt_rewrite":
		// Every value is read here and validated in the constructor, so a typo'd
		// mode or action is a config-load error rather than a guardrail that
		// silently rewrites nothing while the operator believes it strips.
		match, _ := gc.Config["match"].(string)
		mode, _ := gc.Config["mode"].(string)
		action, _ := gc.Config["action"].(string)
		replacement, _ := gc.Config["replacement"].(string)
		paragraphBoundary, _ := gc.Config["paragraph_boundary"].(string)
		blockStatus := intFromConfig(gc.Config, "block_status")
		where, err := stringList(gc.Config, "where")
		if err != nil {
			return nil, err
		}
		return guardrail.NewPromptRewriteGuardrail(
			gc.Name, match, mode, action, replacement, paragraphBoundary, blockStatus, where)
	default:
		return nil, fmt.Errorf("unknown guardrail type %q", gc.Type)
	}
}

// stringList pulls an optional list-of-strings out of a guardrail config block.
func stringList(cfg map[string]interface{}, key string) ([]string, error) {
	raw, ok := cfg[key]
	if !ok {
		return nil, nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("%s must be a list of strings", key)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be a list of strings", key)
		}
		out = append(out, s)
	}
	return out, nil
}

// stringListMap pulls a map[string][]string out of a classifier config
// block, trying each of the given keys in turn (arbiter.yaml uses "keywords"
// for the domain classifier and "detectors" for the capability classifier —
// same shape, different name).
func stringListMap(cfg map[string]interface{}, keys ...string) (map[string][]string, error) {
	var raw map[string]interface{}
	for _, k := range keys {
		if v, ok := cfg[k]; ok {
			raw, _ = v.(map[string]interface{})
			break
		}
	}
	if raw == nil {
		return nil, fmt.Errorf("missing one of %v in config", keys)
	}

	out := make(map[string][]string, len(raw))
	for intent, v := range raw {
		list, ok := v.([]interface{})
		if !ok {
			continue
		}
		words := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				words = append(words, s)
			}
		}
		out[intent] = words
	}
	return out, nil
}

func intFromConfig(cfg map[string]interface{}, key string) int {
	v, ok := cfg[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}
