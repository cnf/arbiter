package router

import (
	"context"
	"fmt"
	"time"

	arbitererrors "github.com/cnf/arbiter/pkg/errors"
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

	// RequiresInputModalities guards the rule's *target* rather than matching
	// the request: every entry must be something the target model accepts
	// ("text", "image", "file"). A rule whose target cannot satisfy it is
	// SKIPPED and matching continues, which is what lets a chain of rules read
	// as "use the vision model when there is an image, otherwise this one".
	//
	// Deliberately a separate field from Capabilities, which matches the
	// request's own required capabilities. The two vocabularies are different
	// things — signals say what the request NEEDS, modalities say what a model
	// ACCEPTS — and folding them together would silently change what every
	// existing `when: {capabilities: ...}` rule means.
	RequiresInputModalities []string
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
// (an alias name, resolved via AliasResolver), Provider/Model (a literal
// provider reference), or Stop (a terminal refusal) must be set. Model is
// optional within the literal form: if unset, the request's own model (if
// any) is preserved, otherwise the provider's first configured model is
// used — same fallback SimpleRouter uses.
type PolicyRule struct {
	When     PolicyCondition
	Target   string // alias name; mutually exclusive with Provider/Model/Stop
	Provider string
	Model    string

	// Stop makes a matched rule terminal: rather than resolving to a route,
	// it refuses the request outright with its own status and message. Set
	// only via the "stop" target shape — mutually exclusive with
	// Target/Provider/Model. See arbitererrors.StopError for why this
	// short-circuits ChainedRouter instead of falling through it like an
	// ordinary "no rule matched" miss.
	Stop *StopTarget
}

// StopTarget is a rule's terminal-refusal target: `target: {stop: {error,
// message}}`. StatusCode is the HTTP status returned to the client (any
// value the operator chooses — 406 in the ticket's example, but nothing
// requires it to be a 4xx); Message is returned verbatim as the error body.
type StopTarget struct {
	StatusCode int
	Message    string
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

	// catalog answers "what does this model accept", for rules that guard on
	// their target's input modalities. nil when no catalog is configured, in
	// which case a guarded rule cannot be verified and is skipped rather than
	// assumed to pass — an unverifiable claim must not route a request to a
	// model that may reject it.
	catalog CostLatencyLookup
}

// NewPolicyRouter creates a signals-driven router. resolver may be nil when
// the config declares no aliases, in which case rules must use the literal
// provider/model form. catalog may be nil when no cost/latency catalog is
// configured; it is only consulted by rules that set
// RequiresInputModalities.
func NewPolicyRouter(name string, rules []PolicyRule, providerConfig map[string]types.ProviderConfig, resolver *AliasResolver, catalog CostLatencyLookup) *PolicyRouter {
	return &PolicyRouter{
		name:           name,
		rules:          rules,
		providerConfig: providerConfig,
		resolver:       resolver,
		catalog:        catalog,
	}
}

// Route returns the provider/model from the first matching rule. A matched
// rule with a Stop target returns an *arbitererrors.StopError instead of a
// route — a deliberate refusal, not a routing failure — so the caller
// (ChainedRouter, the pipeline) can tell the two apart. See StopError's own
// doc for why that distinction matters.
func (pr *PolicyRouter) Route(ctx context.Context, req *types.NormalizedRequest, signals types.Signals) (types.Route, types.Metadata, error) {
	for i, rule := range pr.rules {
		if !rule.When.Matches(signals) {
			continue
		}

		if rule.Stop != nil {
			return types.Route{}, types.Metadata{}, arbitererrors.NewStopError(rule.Stop.StatusCode, rule.Stop.Message)
		}

		route, err := pr.routeFor(rule, req, signals)
		if err != nil {
			return types.Route{}, types.Metadata{}, fmt.Errorf("router %q: rule %d: %w", pr.name, i, err)
		}

		// A rule whose target cannot accept what the rule demands is skipped,
		// and matching continues to the next rule. This is what makes a chain
		// read as "send image traffic here, everything else there" without the
		// operator having to express the negative case.
		if unmet := pr.unmetModalities(rule.When, route); len(unmet) > 0 {
			continue
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

// unmetModalities returns the required modalities the route's target model does
// not advertise. An empty result means the rule may be used.
//
// Three cases are deliberately distinguished:
//
//   - No requirement, or no catalog to consult: nothing to check. A catalog-less
//     router cannot verify a guarded rule, and an unverifiable guard must not
//     pass silently, so the caller treats a nil catalog as "skip the rule".
//   - The model has no capability data (unknown): also unverifiable. Unknown is
//     not permission — routing image traffic to a model whose support is simply
//     unstated is the guess this whole feature exists to stop.
//   - The model states its modalities: check them directly.
func (pr *PolicyRouter) unmetModalities(when PolicyCondition, route types.Route) []string {
	if len(when.RequiresInputModalities) == 0 {
		return nil
	}
	if pr.catalog == nil {
		return when.RequiresInputModalities
	}
	cost, ok := pr.catalog.Lookup(route.Provider, route.Model)
	if !ok || len(cost.InputModalities) == 0 {
		// No catalog row, or a row that states nothing about modalities.
		return when.RequiresInputModalities
	}
	var unmet []string
	for _, want := range when.RequiresInputModalities {
		found := false
		for _, have := range cost.InputModalities {
			if want == have {
				found = true
				break
			}
		}
		if !found {
			unmet = append(unmet, want)
		}
	}
	return unmet
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
