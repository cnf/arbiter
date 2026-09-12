package router

import (
	"context"

	"github.com/cnf/arbiter/pkg/types"
)

// Router determines which upstream provider handles a request.
type Router interface {
	Route(ctx context.Context, req *types.NormalizedRequest, signals types.Signals) (types.Route, types.Metadata, error)
}

// Factory creates a Router from config.
type Factory func(name string, config map[string]interface{}, providers map[string]types.ProviderConfig) (Router, error)

// Registry holds all registered router factories.
var Registry = make(map[string]Factory)

// Register registers a router factory by type name.
func Register(typeName string, factory Factory) {
	Registry[typeName] = factory
}

// ChainedRouter tries multiple routers in sequence, stops on first success.
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

// Route tries each chained router in order.
func (cr *ChainedRouter) Route(ctx context.Context, req *types.NormalizedRequest, signals types.Signals) (types.Route, types.Metadata, error) {
	// TODO: implement chaining logic
	return types.Route{}, types.Metadata{}, nil
}

// SimpleRouter always routes to a default provider.
type SimpleRouter struct {
	name              string
	defaultProvider   string
	fallbackProvider  string
	providerConfig    map[string]types.ProviderConfig
}

// NewSimpleRouter creates a simple router.
func NewSimpleRouter(name string, defaultProvider string, providerConfig map[string]types.ProviderConfig) *SimpleRouter {
	return &SimpleRouter{
		name:            name,
		defaultProvider: defaultProvider,
		providerConfig:  providerConfig,
	}
}

// Route returns the default provider.
func (sr *SimpleRouter) Route(ctx context.Context, req *types.NormalizedRequest, signals types.Signals) (types.Route, types.Metadata, error) {
	// TODO: implement default routing logic
	return types.Route{}, types.Metadata{}, nil
}
