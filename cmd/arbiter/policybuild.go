package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cnf/arbiter/internal/config"
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
			if err := rejectUnknownWhenKeys(i, w); err != nil {
				return nil, err
			}
			if err := checkWhenValueShapes(i, w); err != nil {
				return nil, err
			}
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

			when.Difficulty, _ = w["difficulty"].(string)
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

			// A non-empty `when` that sets no predicate is a catch-all the
			// operator almost certainly did not intend. It parses to the zero
			// PolicyCondition, which Matches treats as a wildcard, so the rule
			// silently swallows every request behind it while its own
			// conditions look like they discriminate.
			//
			// This deliberately does not try to read intent. `when: {}` and an
			// absent `when` are the sanctioned spellings of "match
			// everything"; anything else that constrains nothing — a typo'd
			// key, a value of the wrong shape, or `capabilities: []` on its own
			// — behaves as a catch-all whether or not it was meant to, and is
			// reported here rather than left to route the wrong traffic.
			// (An empty list alongside a real predicate is fine: the predicate
			// constrains the rule, so the empty list is just an unconstrained
			// axis.)
			if len(w) > 0 && !whenSetsAnyPredicate(when) {
				return nil, fmt.Errorf("rule %d: `when` sets no condition, so the rule would match every request; "+
					"write `when: {}` to mean a catch-all deliberately", i)
			}
		}

		rules = append(rules, router.PolicyRule{When: when, Target: target, Provider: provider, Model: model, Stop: stop})
	}
	return rules, nil
}

// whenKeys are every key a `when` block may set, with the deprecated spellings
// each of its two aliased fields accepts. A key outside this set is rejected
// rather than ignored: an ignored key contributes no predicate, and a rule
// whose only key was ignored becomes a catch-all that matches everything —
// silently, since a zero PolicyCondition is a legal wildcard. The cost of
// catching it is that this list has to grow with the parser; the cost of not
// catching it is a rule that routes the wrong traffic and says nothing.
var whenKeys = []string{
	"domain", "intent",
	"difficulty",
	"capabilities",
	"cost_class", "cost_sensitivity",
	"request_kind",
	"requires_input_modalities",
}

// rejectUnknownWhenKeys fails a rule that sets any `when` key the parser does
// not read, naming every offender. Every one is reported rather than the first
// so the operator fixes the rule in one pass, and the order is sorted because
// Go map iteration is random — an error message that varies between runs is
// untestable and unreadable.
func rejectUnknownWhenKeys(ruleIndex int, w map[string]interface{}) error {
	var unknown []string
	for key := range w {
		if !containsString(whenKeys, key) {
			// A key that isn't current may be a REMOVED spelling rather than a
			// typo; naming its replacement turns a dead end into a one-line fix.
			if to, renamed := config.RenamedAxisSpelling(key); renamed {
				return fmt.Errorf("rule %d: `%s` was renamed to `%s`; update the config", ruleIndex, key, to)
			}
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("rule %d: unknown `when` key(s) %s; recognized keys are %s",
		ruleIndex, strings.Join(unknown, ", "), strings.Join(sortedCopy(whenKeys), ", "))
}

// whenSetsAnyPredicate reports whether a condition carries at least one
// predicate. It is deliberately not a zero-value comparison: an all-zero
// PolicyCondition is a wildcard, while `capabilities: []` is an empty-but-real
// condition ("matches requests needing no capabilities") that the operator
// wrote on purpose, and the two must not be treated alike.
func whenSetsAnyPredicate(c router.PolicyCondition) bool {
	return c.Domain != "" || c.Difficulty != "" || c.CostClass != "" || c.RequestKind != "" ||
		c.Capabilities != nil || c.RequiresInputModalities != nil
}

// checkWhenValueShapes rejects a known key carrying a value of the wrong type.
// The parser reads `domain`/`difficulty`/`cost_class`/`request_kind` as strings and
// `capabilities`/`requires_input_modalities` as lists, and skips anything else
// — so `capabilities: "vision"` (a bare string where a list belongs) set no
// predicate at all and turned the rule into a catch-all. Unknown keys are
// caught separately by rejectUnknownWhenKeys.
func checkWhenValueShapes(ruleIndex int, w map[string]interface{}) error {
	for _, key := range []string{"domain", "intent", "difficulty", "cost_class", "cost_sensitivity", "request_kind"} {
		if v, ok := w[key]; ok {
			if _, isString := v.(string); !isString {
				return fmt.Errorf("rule %d: `%s` must be a string", ruleIndex, key)
			}
		}
	}
	for _, key := range []string{"capabilities", "requires_input_modalities"} {
		v, ok := w[key]
		if !ok {
			continue
		}
		list, isList := v.([]interface{})
		if !isList {
			return fmt.Errorf("rule %d: `%s` must be a list of strings (write [`x`], not `x`)", ruleIndex, key)
		}
		for _, item := range list {
			if _, isString := item.(string); !isString {
				return fmt.Errorf("rule %d: `%s` must contain only strings", ruleIndex, key)
			}
		}
	}
	return nil
}

// containsString reports whether want is in list.
func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// sortedCopy returns a sorted copy, leaving the input untouched so a shared
// table cannot be reordered by a caller.
func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
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
