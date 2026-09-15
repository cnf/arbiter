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
	"github.com/cnf/arbiter/internal/upstream"
	"github.com/cnf/arbiter/pkg/types"
)

func main() {
	configPath := flag.String("config", "lanes.yaml", "Path to lanes.yaml config file")
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

	p, err := buildPipeline(cfg, logger, writer)
	if err != nil {
		slog.Error("failed to build pipeline", "error", err)
		os.Exit(1)
	}
	handler := arbiterhttp.NewHandler(arbiterhttp.NewRuntime(p, configuredModels(cfg), cfg.SessionAffinity.Header), logger)
	admin := arbiterhttp.NewAdminHandler(func(ctx context.Context) error {
		return reload(ctx, *configPath, handler, logger, writer)
	}, logger)

	// The stats reader opens its own handle on the same database file the
	// writer feeds, rather than sharing the writer's handle, so a read never
	// contends with the writer's single drain goroutine. A disabled store
	// (storage.path unset) leaves it nil; the stats handlers report 503.
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
	stats := arbiterhttp.NewStatsHandler(reader, logger)

	// Content retention runs on its own goroutine and its own connection, so an
	// expiry sweep never blocks a request. With no TTL configured, content never
	// expires and no sweeper is started — an upgrade must not silently begin
	// deleting data.
	sweepCtx, stopSweeper := context.WithCancel(context.Background())
	defer stopSweeper()
	startContentSweeper(sweepCtx, cfg.Storage.Path, cfg.Storage.ContentTTL, logger)

	r := newRouter(handler, admin, stats, cfg.Admin.ForwardAuthHeader)

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

	// Hot-reload lanes.yaml for the life of the process; errors are logged and
	// a failed reload leaves the running config in place, but a watcher that
	// can't start at all is loud enough to be worth noticing.
	watchCtx, stopWatch := context.WithCancel(context.Background())
	defer stopWatch()
	go func() {
		if err := watchConfig(watchCtx, *configPath, handler, logger, writer); err != nil {
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
func newRouter(handler *arbiterhttp.Handler, admin *arbiterhttp.AdminHandler, stats *arbiterhttp.StatsHandler, forwardAuthHeader string) *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/v1/messages", handler.MessagesHandler).Methods("POST")
	r.HandleFunc("/chat/completions", handler.CompletionsHandler).Methods("POST")
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
	return r
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

// startContentSweeper runs the retention sweep in the background until ctx is
// done. It runs once immediately so a long-idle store is reclaimed at startup
// rather than after the first interval.
//
// Only a non-zero TTL sweeps; with TTL unset ("never expire") starting a
// goroutine that can only ever delete nothing would be pointless, so it isn't
// started at all.
func startContentSweeper(ctx context.Context, path, ttlSpec string, logger logging.Logger) {
	if path == "" || ttlSpec == "" {
		return
	}
	ttl, err := time.ParseDuration(ttlSpec)
	if err != nil || ttl <= 0 {
		if err != nil {
			slog.Error("content_ttl is not a valid duration; content will not expire", "content_ttl", ttlSpec, "error", err)
		}
		return
	}

	// A separate reader connection from the writer's: the sweep must not
	// contend with the drain goroutine, and it is exactly the read-while-writing
	// case the shared DSN's WAL + busy_timeout exist for.
	reader, err := store.OpenReader(path)
	if err != nil {
		slog.Error("cannot start content sweeper", "error", err)
		return
	}

	go func() {
		defer func() { _ = reader.Close() }()
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			bodies, refs, err := reader.SweepContent(ctx, ttl)
			if err != nil {
				if ctx.Err() == nil {
					logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "content_sweep"})
				}
			} else if refs > 0 || bodies > 0 {
				slog.Info("expired captured content", "refs", refs, "bodies", bodies)
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
func buildPipeline(cfg *config.Config, logger logging.Logger, writer store.Writer) (*pipeline.Pipeline, error) {
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

	classifiers := make([]classifier.Classifier, 0, len(cfg.Classifiers))
	for _, cc := range cfg.Classifiers {
		c, err := buildClassifier(cc)
		if err != nil {
			return nil, fmt.Errorf("classifier %q: %w", cc.Name, err)
		}
		classifiers = append(classifiers, c)
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

	routers := make([]router.Router, 0, len(cfg.Routers))
	for _, rc := range cfg.Routers {
		r, err := buildRouter(rc, providers, resolver)
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
		g, err := buildGuardrail(gc)
		if err != nil {
			return nil, fmt.Errorf("guardrail %q: %w", gc.Name, err)
		}
		preGuardrails = append(preGuardrails, g)
	}
	postGuardrails := make([]guardrail.Guardrail, 0, len(cfg.Guardrails.Post))
	for _, gc := range cfg.Guardrails.Post {
		g, err := buildGuardrail(gc)
		if err != nil {
			return nil, fmt.Errorf("guardrail %q: %w", gc.Name, err)
		}
		postGuardrails = append(postGuardrails, g)
	}

	t := translator.NewDefaultTranslator()
	u := upstream.NewHTTPClient(t)

	var defaultCacheTTL time.Duration
	if cfg.SessionAffinity.DefaultTTL != "" {
		if d, err := time.ParseDuration(cfg.SessionAffinity.DefaultTTL); err == nil {
			defaultCacheTTL = d
		}
	}

	p := pipeline.NewPipeline(t, t, t, classifiers, mainRouter, u, providers, cfg.Routing.FallbackProviders, preGuardrails, postGuardrails, logger, defaultCacheTTL, resolver, writer, costLookup)
	// Every event this pipeline records is stamped with the hash of the config
	// that built it, so spend can be compared across config changes.
	p.SetConfigEpoch(cfg.Epoch())
	// Body capture is wiring-time policy, so it rides the same path as the
	// epoch rather than joining the constructor's positional arguments.
	p.SetCaptureContent(cfg.Storage.CaptureContent)
	return p, nil
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
	models := make([]arbiterhttp.Model, 0)
	for provider, providerConfig := range cfg.Providers {
		for _, model := range providerConfig.Models {
			models = append(models, arbiterhttp.Model{ID: model, Provider: provider})
		}
	}
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

// buildClassifier's axis defaulting preserves pre-axis behavior: a plain
// "heuristic" classifier with no declared axis fills Domain, and the legacy
// "capability_detector" type always fills Capabilities regardless of what's
// declared (it never meant anything else).
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
		})
	}
	return out
}

func buildRouter(rc config.RouterConfig, providers map[string]types.ProviderConfig, resolver *router.AliasResolver) (router.Router, error) {
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
		return router.NewPolicyRouter(rc.Name, rules, providers, resolver), nil
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

func buildGuardrail(gc config.GuardrailConfig) (guardrail.Guardrail, error) {
	switch gc.Type {
	case "system_prompt":
		prompt, _ := gc.Config["prompt"].(string)
		override, _ := gc.Config["override"].(bool)
		return guardrail.NewSystemPromptGuardrail(gc.Name, prompt, override), nil
	case "rate_limit":
		perMinute := intFromConfig(gc.Config, "per_minute")
		perDay := intFromConfig(gc.Config, "per_day")
		return guardrail.NewRateLimitGuardrail(gc.Name, perMinute, perDay), nil
	default:
		return nil, fmt.Errorf("unknown guardrail type %q", gc.Type)
	}
}

// stringListMap pulls a map[string][]string out of a classifier config
// block, trying each of the given keys in turn (lanes.yaml uses "keywords"
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
