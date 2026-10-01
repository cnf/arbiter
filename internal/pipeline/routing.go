package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cnf/arbiter/internal/classifier"
	"github.com/cnf/arbiter/internal/router"
	arbitererrors "github.com/cnf/arbiter/pkg/errors"
	"github.com/cnf/arbiter/pkg/types"
)

func (p *Pipeline) resolveRoute(ctx context.Context, req *types.NormalizedRequest, hasKey bool, promptHash string, arrivalTs time.Time) (types.Route, types.Signals, error) {
	if route, ok := p.literalModelRoute(req.Model); ok {
		sig := p.classifyLiteral(ctx, req, hasKey, promptHash, arrivalTs)
		p.logger.LogRouting(ctx, route, sig, 0)
		return route, sig, nil
	}

	if hasKey {
		if provider, model, ok := p.affinity.get(ctx, req.SessionKey, promptHash, req.Model); ok {
			if _, cooling := p.onCooldown(provider); !cooling {
				if cfg, ok := p.providers[provider]; ok {
					route := types.Route{
						Provider:  provider,
						Model:     model,
						Config:    cfg,
						Rationale: "session affinity pin",
					}
					var sig types.Signals
					if kind, stamped := p.stampAliasKind(req.Model); stamped {
						sig.RequestKind = kind
					}
					p.logger.LogRouting(ctx, route, sig, 0)
					return route, sig, nil
				}
			}
		}
	}

	// A client-named pinned/group alias selects a target directly, the same
	// way a rule target would. Force aliases resolve to nothing by design —
	// they only shape axes — so they fall through to classify+rules below.
	if route, ok := p.aliasRoute(req.Model); ok {
		var sig types.Signals
		if kind, stamped := p.stampAliasKind(req.Model); stamped {
			sig.RequestKind = kind
		}
		p.logger.LogRouting(ctx, route, sig, 0)
		return route, sig, nil
	}

	// REQUIREMENTS.md §1: the model field is either a real model name or a
	// configured alias — the two categories /models lists. req.Model already
	// failed the literal-model check above; a force alias is the only other
	// legitimate reason to reach here with req.Model still set (it resolves
	// to no route by design, see above). Anything else is a typo or a stale
	// name the operator never declared, and must not be classified and routed
	// as if it were meaningful — reject rather than silently routing it
	// somewhere on the operator's dime.
	if req.Model != "" && (p.aliasResolver == nil || !p.aliasResolver.Has(req.Model)) {
		return types.Route{}, types.Signals{}, arbitererrors.NewUnknownModelError(
			fmt.Sprintf("model %q is not a configured provider model or alias", req.Model))
	}

	sig, err := p.classify(ctx, req)
	if err != nil {
		return types.Route{}, types.Signals{}, arbitererrors.NewClassificationError("classify request", err)
	}
	p.recordClassifierCalls(req, sig, arrivalTs)
	sig = p.applyForceAlias(req, sig)

	routeStart := time.Now()
	route, err := p.router.Route(ctx, req, sig)
	if err != nil {
		// A StopError is a deliberate refusal from a matched rule, not a
		// routing failure — wrapping it in RoutingError would erase its
		// status/message and the client would see a generic 500 for what
		// the operator configured as a clean, specific refusal. Passed
		// through unwrapped so writeArbiterError (internal/http) and the
		// event-store status mapping below can both see the real type.
		var stopErr *arbitererrors.StopError
		if errors.As(err, &stopErr) {
			return types.Route{}, sig, err
		}
		return types.Route{}, sig, arbitererrors.NewRoutingError("route request", err)
	}
	p.logger.LogRouting(ctx, route, sig, time.Since(routeStart))
	return route, sig, nil
}

// classifyLiteral produces signals for a request whose route is already
// decided by its own model name (resolveRoute rule 1).
//
// The signals are for the RECORD, never for the route: the client named a
// concrete model and goes exactly where it asked. What they buy is
// identification — without this, a title-generation request is
// indistinguishable on its row from ordinary traffic, because it shares the
// main model's name, fills no axis, and carries the generic
// `explicit model "..." -> provider "..."` rationale. The operator's own
// words: "if it happened, it should be shown."
//
// It runs only when the request is NOT YET part of a session (hasKey false, or
// no pin recorded yet). That is the rule from #1 — "every request that is not
// yet part of a session gets classified" — and it is also what keeps the cost
// bounded: the pin recorded after a successful call means turn 2 onward of a
// normal conversation skips this entirely. A title-gen request is the case
// that keeps classifying, and correctly so: its session key is derived from
// conversation text that changes on every call, so it is never part of a
// session and every one of its requests is a first request.
//
// A decisive signature (a title-gen matcher, say) ends the merge, so the
// model-backed classifiers behind it never run: an identified request costs no
// upstream call at all. Anything else pays for classification once per
// session, which is the accepted cost of #1.
//
// A classification failure is not fatal here, unlike on the classify+rules
// path. There the signals decide the route, so failing to produce them must
// fail the request; here the route is already known and only the row's
// annotations are lost, and failing a request the client would otherwise have
// been served is the worse outcome.
func (p *Pipeline) classifyLiteral(ctx context.Context, req *types.NormalizedRequest, hasKey bool, promptHash string, arrivalTs time.Time) types.Signals {
	if hasKey {
		// A pin is the proof that this session/family already exists,
		// whatever model it was recorded under. Deliberately not
		// affinity.get: that returns a hit only when the client is still
		// requesting the model the pin was recorded under, so a client that
		// switched models would look like a brand-new session and be
		// re-classified on every turn.
		if _, ok := p.affinity.pinned(ctx, req.SessionKey, promptHash); ok {
			return types.Signals{}
		}
	}

	sig, err := p.classify(ctx, req)
	if err != nil {
		p.logger.LogError(ctx, "warn", err,
			map[string]interface{}{"stage": "classify_literal_model", "model": req.Model})
		return types.Signals{}
	}
	p.recordClassifierCalls(req, sig, arrivalTs)
	// applyForceAlias is deliberately NOT called: a force alias exists to
	// shape what the router matches on, and nothing is matched here.
	return sig
}

// literalModelRoute returns a direct route when req.Model is a model actually
// declared by a configured provider.
func (p *Pipeline) literalModelRoute(model string) (types.Route, bool) {
	if model == "" {
		return types.Route{}, false
	}
	name, ok := p.literalModels[model]
	if !ok {
		return types.Route{}, false
	}
	cfg, ok := p.providers[name]
	if !ok {
		return types.Route{}, false
	}
	return types.Route{
		Provider:  name,
		Model:     model,
		Config:    cfg,
		Rationale: fmt.Sprintf("explicit model %q -> provider %q", model, name),
		// The one place this is set: the client named a concrete model, so a
		// rate-limited provider must surface rather than be substituted.
		ExplicitModel: true,
	}, true
}

// aliasRoute resolves a client-named pinned/group alias into a concrete
// route. Force aliases and non-alias names yield ok=false so the caller
// proceeds to classification and rule matching.
func (p *Pipeline) aliasRoute(name string) (types.Route, bool) {
	if p.aliasResolver == nil || name == "" {
		return types.Route{}, false
	}
	if _, isForce := p.aliasResolver.Force(name); isForce {
		return types.Route{}, false
	}
	provider, model, ok, err := p.aliasResolver.Resolve(name)
	if err != nil || !ok {
		// A malformed alias is a config error surfaced at load time; treat a
		// runtime resolution failure as "not an alias" rather than failing the
		// request here.
		return types.Route{}, false
	}
	cfg, ok := p.providers[provider]
	if !ok {
		return types.Route{}, false
	}
	if model == "" && len(cfg.Models) > 0 {
		model = cfg.Models[0]
	}
	route := types.Route{
		Provider:  provider,
		Model:     model,
		Config:    cfg,
		Rationale: fmt.Sprintf("client requested alias %q -> %s/%s", name, provider, model),
	}
	route.Fallbacks = p.aliasResolver.GroupFallbacks(name, router.AliasMember{Provider: provider, Model: model})
	return route, true
}

// applyForceAlias overrides the axes named by a force-alias, when req.Model
// names one. Only the axes the alias declares are overridden — an unforced
// axis keeps whatever the classifiers produced (e.g. "coding" forces domain
// but leaves difficulty to be classified normally). ok=false (req.Model isn't a
// force-alias, or no aliases are configured at all) returns sig unchanged.
func (p *Pipeline) applyForceAlias(req *types.NormalizedRequest, sig types.Signals) types.Signals {
	if p.aliasResolver == nil || req.Model == "" {
		return sig
	}
	force, ok := p.aliasResolver.Force(req.Model)
	if !ok {
		return sig
	}
	for axis, values := range force {
		if len(values) == 0 {
			continue
		}
		switch axis {
		case classifier.AxisDomain, "intent":
			sig.Domain = values[0]
		case classifier.AxisDifficulty:
			sig.Difficulty = values[0]
		case classifier.AxisCostClass, "cost_sensitivity":
			sig.CostClass = values[0]
		case classifier.AxisCapabilities:
			sig.RequiredCapabilities = append([]string{}, values...)
		}
	}
	// An alias-declared request kind OVERRIDES what classification produced:
	// the operator named this alias for this traffic, so the alias's word
	// about what the request IS is the final one (unlike the axes, which the
	// alias merely nudges). Override rather than fill-if-empty keeps the two
	// sources from disagreeing on the row.
	if kind, stamped := p.stampAliasKind(req.Model); stamped {
		sig.RequestKind = kind
	}
	return sig
}

// stampAliasKind returns the request kind req.Model's alias declares, for the
// record. ok is false when the model is not an alias or the alias declares no
// kind — callers then keep whatever signals they already have (usually none:
// the paths that call this are exactly the ones where classification never
// ran).
//
// This is the recorded kind, never a routing decision: the route is already
// decided at the two no-classification call sites (the pinned/group
// short-circuit and the affinity pin), and on the force path the router
// matches on whatever the merged signals now carry.
//
// The affinity pin is the subtle call site: turn 2+ of an alias-routed
// conversation resolves through the pin, which records the client's model
// string — for alias traffic that IS the alias name — so looking up the alias
// by req.Model here keeps the kind on every row of the session, not just the
// first request. Missing this site is how a stamp "works" once and silently
// vanishes from every row after.
func (p *Pipeline) stampAliasKind(model string) (string, bool) {
	if p.aliasResolver == nil || model == "" {
		return "", false
	}
	kind, ok := p.aliasResolver.RequestKind(model)
	if kind == "" {
		return "", false
	}
	return kind, ok
}

// cacheTTLFor returns the session affinity idle TTL to use for a pin served
// by provider: the provider's own CacheTTL override if set, else the
// pipeline's default.
func (p *Pipeline) cacheTTLFor(provider string) time.Duration {
	if cfg, ok := p.providers[provider]; ok && cfg.CacheTTL > 0 {
		return cfg.CacheTTL
	}
	return p.defaultCacheTTL
}
