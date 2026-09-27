package main

import (
	"context"
	"fmt"
	"time"

	"github.com/cnf/arbiter/internal/config"
	"github.com/cnf/arbiter/internal/guardrail"
	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/pipeline"
	"github.com/cnf/arbiter/internal/router"
	"github.com/cnf/arbiter/internal/store"
	"github.com/cnf/arbiter/internal/translator"
	"github.com/cnf/arbiter/internal/upstream"
	"github.com/cnf/arbiter/pkg/types"
)

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
