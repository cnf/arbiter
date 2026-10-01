package main

import (
	"fmt"

	"github.com/cnf/arbiter/internal/config"
	"github.com/cnf/arbiter/internal/router"
	"github.com/cnf/arbiter/pkg/types"
)

// configuredModels lists every model a client can name in a request:
// concrete provider models, plus every configured alias — force-aliases
// included, since REQUIREMENTS.md §1 makes aliases client-facing regardless
// of shape. An alias is advertised with Provider "alias" rather than a
// resolved target, since group/force aliases don't resolve to one fixed
// provider.
func buildRouter(rc config.RouterConfig, providers map[string]types.ProviderConfig, resolver *router.AliasResolver, catalog router.CostLatencyLookup) (router.Router, error) {
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
		return router.NewPolicyRouter(rc.Name, rules, providers, resolver, catalog), nil
	default:
		return nil, fmt.Errorf("unknown router type %q", rc.Type)
	}
}

// policyRules parses the "rules" list out of a policy router's config block.
// Each rule's "when" clause is optional per-field (a missing field is a
// wildcard); see router.PolicyCondition. A rule's target is exactly one of:
// a named alias ("target" as a string), a literal provider/model
// ("provider"/"model"), or a terminal refusal ("target: {stop: {error,
// message}}") — see parseStopTarget. Both the current ("domain"/"cost_class")
// and deprecated ("intent"/"cost_sensitivity") when-clause key spellings are
// accepted during the deprecation window; setting both spellings of the same
// axis on one rule is an error rather than silently picking one.
// "request_kind" matches types.Signals.RequestKind exactly ("title", later
// "subagent") — not an axis, but still a legitimate rule condition.
//
// A `when` key the parser does not read is an ERROR, not ignored. An ignored
// key contributes no predicate, and a rule whose only key was ignored parses to
// the zero PolicyCondition — which Matches treats as a wildcard, so the rule
// silently becomes a catch-all and swallows every request behind it while its
// own conditions appear to discriminate. The same applies to a recognised key
// carrying a value of the wrong shape (`capabilities: "vision"` where a list
// belongs), and to a `when` that sets nothing on purpose-by-accident.
//
// A rule with no `when` at all, or `when: {}`, is still the deliberate way to
// match everything — deliberate in the spelling, so the easy mistake and the
// intended catch-all are distinguishable at load time.
