package router

import (
	"context"
	"fmt"
	"time"

	"github.com/cnf/arbiter/pkg/types"
)

// PolicyCondition is the match criteria for a PolicyRule. A zero-value field
// is a wildcard (not checked); a zero-value PolicyCondition matches any
// signals, which is useful as a catch-all rule ahead of a SimpleRouter
// fallback in a ChainedRouter.
type PolicyCondition struct {
	Intent          string
	Capabilities    []string // every entry must appear in signals.RequiredCapabilities
	CostSensitivity string
}

// Matches reports whether signals satisfy this condition.
func (c PolicyCondition) Matches(sig types.Signals) bool {
	if c.Intent != "" && c.Intent != sig.Intent {
		return false
	}
	if c.CostSensitivity != "" && c.CostSensitivity != sig.CostSensitivity {
		return false
	}
	for _, want := range c.Capabilities {
		found := false
		for _, have := range sig.RequiredCapabilities {
			if want == have {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// PolicyRule maps a condition to a provider/model choice. Model is optional:
// if unset, the request's own model (if any) is preserved, otherwise the
// provider's first configured model is used — same fallback SimpleRouter
// uses.
type PolicyRule struct {
	When     PolicyCondition
	Provider string
	Model    string
}

// PolicyRouter picks a provider by matching classifier signals against an
// ordered list of rules — the first match wins. Unlike SimpleRouter, it does
// not fall back to a default: a request that matches no rule is an error,
// which is deliberate — it lets ChainedRouter fall through to a
// SimpleRouter (or another PolicyRouter) configured after it.
type PolicyRouter struct {
	name           string
	rules          []PolicyRule
	providerConfig map[string]types.ProviderConfig
}

// NewPolicyRouter creates a signals-driven router.
func NewPolicyRouter(name string, rules []PolicyRule, providerConfig map[string]types.ProviderConfig) *PolicyRouter {
	return &PolicyRouter{
		name:           name,
		rules:          rules,
		providerConfig: providerConfig,
	}
}

// Route returns the provider/model from the first matching rule.
func (pr *PolicyRouter) Route(ctx context.Context, req *types.NormalizedRequest, signals types.Signals) (types.Route, types.Metadata, error) {
	for _, rule := range pr.rules {
		if !rule.When.Matches(signals) {
			continue
		}

		cfg, ok := pr.providerConfig[rule.Provider]
		if !ok {
			return types.Route{}, types.Metadata{}, fmt.Errorf("router %q: rule matched but provider %q is not configured", pr.name, rule.Provider)
		}

		model := rule.Model
		if model == "" {
			model = req.Model
		}
		if model == "" && len(cfg.Models) > 0 {
			model = cfg.Models[0]
		}

		route := types.Route{
			Provider:  rule.Provider,
			Model:     model,
			Config:    cfg,
			Rationale: fmt.Sprintf("policy router %q: intent=%q capabilities=%v cost_sensitivity=%q -> provider %q", pr.name, signals.Intent, signals.RequiredCapabilities, signals.CostSensitivity, rule.Provider),
		}
		meta := types.Metadata{
			LatencyTarget: "normal",
			TraceID:       req.TraceID,
			RoutedAt:      time.Now(),
		}
		return route, meta, nil
	}

	return types.Route{}, types.Metadata{}, fmt.Errorf("router %q: no rule matched signals (intent=%q capabilities=%v cost_sensitivity=%q)", pr.name, signals.Intent, signals.RequiredCapabilities, signals.CostSensitivity)
}
