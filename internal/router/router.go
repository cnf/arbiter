package router

import (
	"context"
	"errors"
	"fmt"

	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

// containsModel reports whether models declares model exactly.
func containsModel(models []string, model string) bool {
	for _, m := range models {
		if m == model {
			return true
		}
	}
	return false
}

// Router determines which upstream provider handles a request.
type Router interface {
	Route(ctx context.Context, req *types.NormalizedRequest, signals types.Signals) (types.Route, error)
}

// ChainedRouter tries multiple routers in sequence, taking the first
// non-error result. This is how composable routing axes stack: e.g. a
// capability router that only produces a Route when the request needs
// vision, chained before a SimpleRouter that always produces one as the
// final fallback.
type ChainedRouter struct {
	name    string
	routers []Router
}

// NewChainedRouter creates a router that chains multiple routers.
func NewChainedRouter(name string, routers []Router) *ChainedRouter {
	return &ChainedRouter{
		name:    name,
		routers: routers,
	}
}

// Route tries each chained router in order, returning the first one that
// succeeds. If all routers fail, the last error is returned.
//
// A StopError is never one of those failures to fall through on: it is a
// deliberate "refuse this request" decision from a matched rule, not "this
// router doesn't apply here" — the chain exists to compose the second kind
// of miss, and treating a stop as one would let a later router quietly
// override an operator's explicit refusal.
func (cr *ChainedRouter) Route(ctx context.Context, req *types.NormalizedRequest, signals types.Signals) (types.Route, error) {
	if len(cr.routers) == 0 {
		return types.Route{}, fmt.Errorf("router %q: no routers configured", cr.name)
	}

	var lastErr error
	for _, r := range cr.routers {
		route, err := r.Route(ctx, req, signals)
		if err == nil {
			return route, nil
		}
		var stopErr *arbitererrors.StopError
		if errors.As(err, &stopErr) {
			return types.Route{}, err
		}
		lastErr = err
	}
	return types.Route{}, fmt.Errorf("router %q: all routers failed: %w", cr.name, lastErr)
}

// SimpleRouter always routes to a default provider, falling back to a
// second provider if the default isn't configured. This is the v1
// workhorse — no cost/latency/capability-aware logic yet, just "does this
// provider exist, use it."
type SimpleRouter struct {
	name             string
	defaultProvider  string
	fallbackProvider string
	providerConfig   map[string]types.ProviderConfig
}

// NewSimpleRouter creates a simple router.
func NewSimpleRouter(name, defaultProvider, fallbackProvider string, providerConfig map[string]types.ProviderConfig) *SimpleRouter {
	return &SimpleRouter{
		name:             name,
		defaultProvider:  defaultProvider,
		fallbackProvider: fallbackProvider,
		providerConfig:   providerConfig,
	}
}

// Route returns the default provider, or the fallback if the default isn't
// in the provider map (e.g. missing API key / not configured for this
// deployment). The requested model, if any, is preserved; otherwise the
// provider's first configured model is used.
func (sr *SimpleRouter) Route(ctx context.Context, req *types.NormalizedRequest, signals types.Signals) (types.Route, error) {
	providerName := sr.defaultProvider
	cfg, ok := sr.providerConfig[providerName]
	if !ok {
		providerName = sr.fallbackProvider
		cfg, ok = sr.providerConfig[providerName]
		if !ok {
			return types.Route{}, fmt.Errorf("router %q: neither default provider %q nor fallback %q are configured", sr.name, sr.defaultProvider, sr.fallbackProvider)
		}
	}

	model := req.Model
	rationale := fmt.Sprintf("simple router %q: default provider", sr.name)
	if providerName == sr.fallbackProvider {
		rationale = fmt.Sprintf("simple router %q: default provider %q unavailable, used fallback", sr.name, sr.defaultProvider)
	}
	// req.Model reaching this router was neither a literal match (that
	// precedence step already ran) nor an alias/pin, so it's an opaque
	// routing signal at best (a force-alias name, a client convention) —
	// never a real model this provider declared. Forwarding it verbatim
	// would send an undeclared model upstream, so it's dropped in favor of
	// the provider's own declared model, same as an empty model.
	if model != "" && len(cfg.Models) > 0 && !containsModel(cfg.Models, model) {
		rationale = fmt.Sprintf("%s (requested model %q not declared by %q, using provider default)", rationale, model, providerName)
		model = ""
	}
	if model == "" && len(cfg.Models) > 0 {
		model = cfg.Models[0]
	}

	route := types.Route{
		Provider:  providerName,
		Model:     model,
		Config:    cfg,
		Rationale: rationale,
	}
	return route, nil
}
