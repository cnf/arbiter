package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/cnf/arbiter/internal/config"
	arbiterhttp "github.com/cnf/arbiter/internal/http"
	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/pipeline"
	"github.com/cnf/arbiter/internal/store"
	"github.com/cnf/arbiter/internal/ui"
)

// reloadDebounce coalesces the burst of events a single save produces. Most
// editors write the file more than once (or via a temp file + rename), and a
// reload is expensive enough (rebuild every router/classifier/guardrail) that
// reacting to each event individually is wasteful. A short settle window keeps
// one save to one reload.
const reloadDebounce = 150 * time.Millisecond

// watchConfig reloads arbiter.yaml whenever it changes on disk, swapping the
// handler's runtime in place. It watches the *directory* rather than the file
// itself: editors commonly save by writing a temp file and renaming it over the
// target (and some remove-then-recreate), which replaces the inode and would
// silently break a watch held on the file. Watching the directory and filtering
// by basename survives all of those save styles.
//
// A reload only publishes a runtime that was fully loaded, validated, and
// built. Anything short of that — a syntax error mid-save, an invalid router,
// an unset ${ENV_VAR} — logs and leaves the current runtime serving, so a bad
// edit never takes Arbiter down.
func watchConfig(ctx context.Context, path string, handler *arbiterhttp.Handler, logger logging.Logger, writer store.Writer, cooldowns *pipeline.CooldownStore, reader *store.Reader, adminUI *ui.Handler) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create config watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()

	dir := filepath.Dir(path)
	if err := watcher.Add(dir); err != nil {
		return fmt.Errorf("watch config directory %s: %w", dir, err)
	}

	target := filepath.Base(path)
	var timer *time.Timer
	var timerC <-chan time.Time

	// stopTimer prevents a lingering timer from firing after this loop returns.
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil

		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			// Ignore other files in the directory (editors drop swap files,
			// backups, etc. alongside the real config).
			if filepath.Base(event.Name) != target {
				continue
			}
			if timer == nil {
				timer = time.NewTimer(reloadDebounce)
			} else {
				timer.Reset(reloadDebounce)
			}
			timerC = timer.C

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			logger.LogError(ctx, "warn", err, map[string]interface{}{"phase": "watch_config"})

		case <-timerC:
			timerC = nil
			_ = reload(ctx, path, handler, logger, writer, cooldowns, reader, adminUI)
		}
	}
}

// reload loads, validates, and rebuilds the configuration, then publishes it.
// On any failure the previous runtime is left untouched and the error is
// returned so the caller can report it — the file watcher only logs it,
// while POST /admin/reload surfaces it to whoever asked.
func reload(ctx context.Context, path string, handler *arbiterhttp.Handler, logger logging.Logger, writer store.Writer, cooldowns *pipeline.CooldownStore, reader *store.Reader, adminUI *ui.Handler) error {
	cfg, err := config.Load(path)
	if err != nil {
		logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "config_reload"})
		slog.Warn("config reload rejected; keeping previous configuration", "config", path)
		return err
	}

	p, err := buildPipeline(cfg, logger, writer, cooldowns, reader)
	if err != nil {
		logger.LogError(ctx, "error", err, map[string]interface{}{"phase": "config_reload_build"})
		slog.Warn("config reload rejected; keeping previous configuration", "config", path)
		return err
	}

	models := configuredModels(cfg)
	handler.Swap(arbiterhttp.NewRuntime(p, models, cfg.SessionAffinity.Header))
	// adminUI is not rebuilt on reload (it holds no config-derived state
	// besides this), so its copy of capture_content needs the same update
	// buildPipeline just gave the new pipeline's copy — see main.go's
	// matching call at startup. nil is accepted (tests that don't exercise
	// the admin UI pass it that way) rather than forcing every caller to
	// construct one just to satisfy this line.
	if adminUI != nil {
		adminUI.SetCaptureContent(cfg.Storage.CaptureContent)
	}
	slog.Info("config reloaded", "config", path, "providers", len(cfg.Providers), "models", len(models))
	return nil
}
