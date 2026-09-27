package main

import (
	"fmt"

	"github.com/cnf/arbiter/internal/router"
)

// configuredModels lists every model a client can name in a request:
// concrete provider models, plus every configured alias — force-aliases
// included, since REQUIREMENTS.md §1 makes aliases client-facing regardless
// of shape. An alias is advertised with Provider "alias" rather than a
// resolved target, since group/force aliases don't resolve to one fixed
// provider.
func policyRules(cfg map[string]interface{}) ([]router.PolicyRule, error) {
	raw, _ := cfg["rules"].([]interface{})
	rules := make([]router.PolicyRule, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("rule %d: expected a map", i)
		}

		provider, _ := m["provider"].(string)
		model, _ := m["model"].(string)

		// "target" is either a plain string (an alias name — the existing
		// shape) or a map naming a terminal target ({stop: {...}} — the only
		// one that exists today, but a map shape rather than a dedicated
		// "stop" key leaves room for a second terminal target later without
		// another top-level rule field).
		var target string
		var stop *router.StopTarget
		switch t := m["target"].(type) {
		case string:
			target = t
		case map[string]interface{}:
			stopRaw, ok := t["stop"].(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("rule %d: target map must set \"stop\"", i)
			}
			st, err := parseStopTarget(stopRaw)
			if err != nil {
				return nil, fmt.Errorf("rule %d: %w", i, err)
			}
			stop = st
		case nil:
			// no target set; provider/model or an error below decides.
		default:
			return nil, fmt.Errorf("rule %d: target must be a string (alias name) or a map ({stop: ...})", i)
		}

		if stop != nil {
			if target != "" || provider != "" {
				return nil, fmt.Errorf("rule %d: sets stop together with target/provider; use exactly one", i)
			}
		} else {
			if target != "" && provider != "" {
				return nil, fmt.Errorf("rule %d: sets both target and provider; use exactly one", i)
			}
			if target == "" && provider == "" {
				return nil, fmt.Errorf("rule %d: missing target or provider", i)
			}
		}

		var when router.PolicyCondition
		if w, ok := m["when"].(map[string]interface{}); ok {
			domain, err := stringOneOf(w, "domain", "intent")
			if err != nil {
				return nil, fmt.Errorf("rule %d: %w", i, err)
			}
			when.Domain = domain

			costClass, err := stringOneOf(w, "cost_class", "cost_sensitivity")
			if err != nil {
				return nil, fmt.Errorf("rule %d: %w", i, err)
			}
			when.CostClass = costClass

			when.Effort, _ = w["effort"].(string)
			when.RequestKind, _ = w["request_kind"].(string)
			if caps, ok := w["capabilities"].([]interface{}); ok {
				for _, c := range caps {
					if s, ok := c.(string); ok {
						when.Capabilities = append(when.Capabilities, s)
					}
				}
			}
			// Guards the rule's target rather than matching the request — see
			// PolicyCondition.RequiresInputModalities. Named distinctly from
			// `capabilities` because the two vocabularies differ: signals say
			// what the request needs, modalities say what a model accepts.
			if mods, ok := w["requires_input_modalities"].([]interface{}); ok {
				for _, c := range mods {
					if s, ok := c.(string); ok {
						when.RequiresInputModalities = append(when.RequiresInputModalities, s)
					}
				}
			}
		}

		rules = append(rules, router.PolicyRule{When: when, Target: target, Provider: provider, Model: model, Stop: stop})
	}
	return rules, nil
}

// parseStopTarget parses a rule's `target: {stop: {error, message}}` block.
// error must be a valid HTTP status code and message must be non-empty — an
// operator-configured refusal with no explanation is as unhelpful to debug
// as the empty-axis bug this whole ticket exists to fix.
func parseStopTarget(m map[string]interface{}) (*router.StopTarget, error) {
	status := intFromConfig(m, "error")
	if status < 100 || status > 599 {
		return nil, fmt.Errorf("stop: \"error\" must be a valid HTTP status code, got %d", status)
	}
	message, _ := m["message"].(string)
	if message == "" {
		return nil, fmt.Errorf("stop: missing \"message\"")
	}
	return &router.StopTarget{StatusCode: status, Message: message}, nil
}

// stringOneOf reads a string value from exactly one of the given keys,
// erroring if more than one is set on the same map — used to accept a
// deprecated YAML key spelling alongside its replacement without silently
// preferring one when a config mistakenly sets both.
func stringOneOf(m map[string]interface{}, keys ...string) (string, error) {
	var value, foundKey string
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			if foundKey != "" {
				return "", fmt.Errorf("both %q and %q are set; use only %q", foundKey, k, keys[0])
			}
			value, foundKey = v, k
		}
	}
	return value, nil
}
