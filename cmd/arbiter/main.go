// Command arbiter is the proxy's entry point: it loads config, opens the event
// store, builds the pipeline and its supporting components, and serves.
//
// The work is split across files by "when in startup does this run":
//
//	main.go       flag parsing, the startup sequence, signal/shutdown handling
//	routes.go     newRouter — every HTTP route on the main mux
//	wiring.go     buildPipeline and the store-backed Pinner adapter
//	builders.go   configuredModels and the classifier builders
//	routerbuild.go  buildRouter — config.RouterConfig to a router.Router
//	policybuild.go  policyRules and the config-map parsing behind it
//	guardrail.go  buildGuardrail and the config-map helpers it shares
//	store.go      listen, openStore, and the retention sweeper
//	version.go    buildVersion, read from the VCS stamp
package main

import (
	"context"
	"flag"
	"log/slog"
	stdhttp "net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cnf/arbiter/internal/config"
	arbiterhttp "github.com/cnf/arbiter/internal/http"
	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/pipeline"
	"github.com/cnf/arbiter/internal/store"
	"github.com/cnf/arbiter/internal/ui"
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
	revision, modified, buildTime := buildVersion()
	slog.Info("Arbiter version", "revision", revision, "modified", modified, "build_time", buildTime)
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

	// The admin UI reads the same Reader as the JSON surface. It is a separate
	// package rather than more handlers on stats because it brings its own
	// embedded templates and assets; its dependencies are identical. Built
	// here, ahead of admin's reload closure, because that closure captures it.
	adminUI := ui.New(reader, logger)
	// See buildPipeline's matching SetCaptureContent call: the UI needs its
	// own copy of the same wiring-time policy, not a read through the
	// pipeline, because piece 3 of #11 uses it to explain a title-gen line
	// with no resolvable parent (tier 2 of ParentSessionForTitle cannot run
	// with capture off, and that is a different situation from tier 2
	// running and finding nothing).
	adminUI.SetCaptureContent(cfg.Storage.CaptureContent)

	admin := arbiterhttp.NewAdminHandler(func(ctx context.Context) error {
		return reload(ctx, *configPath, handler, logger, writer, cooldowns, reader, adminUI)
	}, logger)
	// The cooldown reset is bound to the shared store, so it clears the state the
	// live pipeline is actually using.
	admin.SetClearCooldowns(cooldowns.ClearForAdmin)

	stats := arbiterhttp.NewStatsHandler(reader, logger)

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
		if err := watchConfig(watchCtx, *configPath, handler, logger, writer, cooldowns, reader, adminUI); err != nil {
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
