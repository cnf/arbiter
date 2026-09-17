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
	Domain       string
	Effort       string
	Capabilities []string // every entry must appear in signals.RequiredCapabilities
	CostClass    string
}

// Matches reports whether signals satisfy this condition.
func (c PolicyCondition) Matches(sig types.Signals) bool {
	if c.Domain != "" && c.Domain != sig.Domain {
		return false
	}
	if c.Effort != "" && c.Effort != sig.Effort {
		return false
	}
	if c.CostClass != "" && c.CostClass != sig.CostClass {
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

// String renders the condition for log lines and error messages.
func (c PolicyCondition) String() string {
	return fmt.Sprintf("domain=%q effort=%q capabilities=%v cost_class=%q", c.Domain, c.Effort, c.Capabilities, c.CostClass)
}

// PolicyRule maps a condition to a routing target. Exactly one of Target
// (an alias name, resolved via AliasResolver) or Provider/Model (a literal
// provider reference) must be set. Model is optional within the literal
// form: if unset, the request's own model (if any) is preserved, otherwise
// the provider's first configured model is used — same fallback SimpleRouter
// uses.
type PolicyRule struct {
	When     PolicyCondition
	Target   string // alias name; mutually exclusive with Provider/Model
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
	resolver       *AliasResolver // nil when no aliases are configured
}

// NewPolicyRouter creates a signals-driven router. resolver may be nil when
// the config declares no aliases, in which case rules must use the literal
// provider/model form.
func NewPolicyRouter(name string, rules []PolicyRule, providerConfig map[string]types.ProviderConfig, resolver *AliasResolver) *PolicyRouter {
	return &PolicyRouter{
		name:           name,
		rules:          rules,
		providerConfig: providerConfig,
		resolver:       resolver,
	}
}

// Route returns the provider/model from the first matching rule.
func (pr *PolicyRouter) Route(ctx context.Context, req *types.NormalizedRequest, signals types.Signals) (types.Route, types.Metadata, error) {
	for i, rule := range pr.rules {
		if !rule.When.Matches(signals) {
			continue
		}

		route, err := pr.routeFor(rule, req, signals)
		if err != nil {
			return types.Route{}, types.Metadata{}, fmt.Errorf("router %q: rule %d: %w", pr.name, i, err)
		}

		meta := types.Metadata{
			LatencyTarget: "normal",
			TraceID:       req.TraceID,
			RoutedAt:      time.Now(),
		}
		return route, meta, nil
	}

	return types.Route{}, types.Metadata{}, fmt.Errorf("router %q: no rule matched signals (%s)", pr.name, signalsDescription(signals))
}

// routeFor turns a matched rule into a concrete Route, resolving a Target
// alias when the rule names one and otherwise using the literal
// provider/model.
func (pr *PolicyRouter) routeFor(rule PolicyRule, req *types.NormalizedRequest, signals types.Signals) (types.Route, error) {
	if rule.Target != "" {
		return pr.routeViaAlias(rule, req, signals)
	}

	cfg, ok := pr.providerConfig[rule.Provider]
	if !ok {
		return types.Route{}, fmt.Errorf("rule matched but provider %q is not configured", rule.Provider)
	}

	model := rule.Model
	if model == "" {
		model = req.Model
		// Unlike rule.Model (the operator's own config, trusted as-is), this
		// came from the client and was neither a literal-model match nor an
		// alias/pin — an opaque routing signal, not necessarily a model this
		// provider declares. Forwarding it verbatim would send an undeclared
		// model upstream, so drop it and fall through to the provider default
		// below, same as an empty model.
		if model != "" && len(cfg.Models) > 0 && !containsModel(cfg.Models, model) {
			model = ""
		}
	}
	if model == "" && len(cfg.Models) > 0 {
		model = cfg.Models[0]
	}

	return types.Route{
		Provider:  rule.Provider,
		Model:     model,
		Config:    cfg,
		Rationale: fmt.Sprintf("policy router %q: %s -> provider %q", pr.name, rule.When, rule.Provider),
	}, nil
}

// routeViaAlias resolves a rule's Target alias into provider+model, carrying
// the alias's unselected group members as this route's fallback chain.
func (pr *PolicyRouter) routeViaAlias(rule PolicyRule, req *types.NormalizedRequest, signals types.Signals) (types.Route, error) {
	if pr.resolver == nil {
		return types.Route{}, fmt.Errorf("target %q names an alias, but no aliases are configured", rule.Target)
	}

	provider, model, ok, err := pr.resolver.Resolve(rule.Target)
	if err != nil {
		return types.Route{}, err
	}
	if !ok {
		return types.Route{}, fmt.Errorf("target %q is not a configured alias", rule.Target)
	}

	cfg, ok := pr.providerConfig[provider]
	if !ok {
		return types.Route{}, fmt.Errorf("alias %q resolved to provider %q, which is not configured", rule.Target, provider)
	}
	if model == "" && len(cfg.Models) > 0 {
		model = cfg.Models[0]
	}

	route := types.Route{
		Provider:  provider,
		Model:     model,
		Config:    cfg,
		Rationale: fmt.Sprintf("policy router %q: %s -> alias %q -> %s/%s", pr.name, rule.When, rule.Target, provider, model),
	}
	route.Fallbacks = pr.resolver.GroupFallbacks(rule.Target, AliasMember{Provider: provider, Model: model})
	return route, nil
}

// signalsDescription renders the signal axes routing matches on, for log
// lines and error messages.
func signalsDescription(sig types.Signals) string {
	return fmt.Sprintf("domain=%q effort=%q capabilities=%v cost_class=%q", sig.Domain, sig.Effort, sig.RequiredCapabilities, sig.CostClass)
}
