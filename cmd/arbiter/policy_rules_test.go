package main

import (
	"strings"
	"testing"
)

func TestPolicyRulesRejectsBothTargetAndProvider(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"target": "coding", "provider": "claude"},
		},
	}
	if _, err := policyRules(cfg); err == nil {
		t.Fatal("expected an error: rule sets both target and provider")
	}
}

func TestPolicyRulesRejectsNeitherTargetNorProvider(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"model": "claude-3-opus-20250219"},
		},
	}
	if _, err := policyRules(cfg); err == nil {
		t.Fatal("expected an error: rule sets neither target nor provider")
	}
}

func TestPolicyRulesAcceptsTargetAlone(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"target": "coding"},
		},
	}
	rules, err := policyRules(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 1 || rules[0].Target != "coding" {
		t.Fatalf("rules = %+v, want one rule with Target=coding", rules)
	}
}

func TestPolicyRulesAcceptsCurrentDomainSpelling(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"provider": "claude",
				"when":     map[string]interface{}{"domain": "code_generation"},
			},
		},
	}
	rules, err := policyRules(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rules[0].When.Domain != "code_generation" {
		t.Fatalf("When.Domain = %q, want code_generation", rules[0].When.Domain)
	}
}

func TestPolicyRulesAcceptsDeprecatedIntentSpelling(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"provider": "claude",
				"when":     map[string]interface{}{"intent": "code_generation"},
			},
		},
	}
	rules, err := policyRules(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rules[0].When.Domain != "code_generation" {
		t.Fatalf("When.Domain = %q, want code_generation (from deprecated 'intent' key)", rules[0].When.Domain)
	}
}

func TestPolicyRulesRejectsBothDomainSpellingsSet(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"provider": "claude",
				"when":     map[string]interface{}{"domain": "code_generation", "intent": "chat"},
			},
		},
	}
	if _, err := policyRules(cfg); err == nil {
		t.Fatal("expected an error: both 'domain' and 'intent' set on the same rule")
	}
}

func TestPolicyRulesAcceptsDeprecatedCostSensitivitySpelling(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"provider": "claude",
				"when":     map[string]interface{}{"cost_sensitivity": "budget"},
			},
		},
	}
	rules, err := policyRules(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rules[0].When.CostClass != "budget" {
		t.Fatalf("When.CostClass = %q, want budget (from deprecated 'cost_sensitivity' key)", rules[0].When.CostClass)
	}
}

func TestPolicyRulesRejectsBothCostClassSpellingsSet(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"provider": "claude",
				"when":     map[string]interface{}{"cost_class": "budget", "cost_sensitivity": "quality_first"},
			},
		},
	}
	if _, err := policyRules(cfg); err == nil {
		t.Fatal("expected an error: both 'cost_class' and 'cost_sensitivity' set on the same rule")
	}
}

func TestPolicyRulesParsesRequiresInputModalities(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"provider": "claude",
				"when":     map[string]interface{}{"requires_input_modalities": []interface{}{"image", "file"}},
			},
		},
	}
	rules, err := policyRules(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := rules[0].When.RequiresInputModalities
	if len(got) != 2 || got[0] != "image" || got[1] != "file" {
		t.Fatalf("RequiresInputModalities = %v, want [image file]", got)
	}
}

// The new key must not be confused with the existing `capabilities`, which
// matches the request rather than the target. Parsing one into the other would
// silently change what every existing capabilities rule means.
func TestPolicyRulesKeepsModalitiesAndCapabilitiesSeparate(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"provider": "claude",
				"when": map[string]interface{}{
					"capabilities":              []interface{}{"vision"},
					"requires_input_modalities": []interface{}{"image"},
				},
			},
		},
	}
	rules, err := policyRules(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	when := rules[0].When
	if len(when.Capabilities) != 1 || when.Capabilities[0] != "vision" {
		t.Errorf("Capabilities = %v, want [vision] (request-side)", when.Capabilities)
	}
	if len(when.RequiresInputModalities) != 1 || when.RequiresInputModalities[0] != "image" {
		t.Errorf("RequiresInputModalities = %v, want [image] (target-side)", when.RequiresInputModalities)
	}
}

// Tags parse like capabilities into their own PolicyCondition field — and
// must NOT be confused with capabilities, since the two are matched against
// separate signals (see types.Signals.Tags).
func TestPolicyRulesParsesTags(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"provider": "claude",
				"when": map[string]interface{}{
					"tags":         []interface{}{"python", "french"},
					"capabilities": []interface{}{"vision"},
				},
			},
		},
	}
	rules, err := policyRules(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	when := rules[0].When
	if len(when.Tags) != 2 || when.Tags[0] != "python" || when.Tags[1] != "french" {
		t.Errorf("Tags = %v, want [python french]", when.Tags)
	}
	if len(when.Capabilities) != 1 || when.Capabilities[0] != "vision" {
		t.Errorf("Capabilities = %v, want [vision] (must stay separate from tags)", when.Capabilities)
	}
}

// A tag rule with a wrong-shaped value must be rejected, not silently parsed
// to an empty predicate (which would turn the rule into a catch-all).
func TestPolicyRulesRejectsWrongShapedTags(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"provider": "claude",
				"when":     map[string]interface{}{"tags": "python"},
			},
		},
	}
	if _, err := policyRules(cfg); err == nil {
		t.Fatal("expected an error for `tags: python` (a bare string where a list belongs), got none")
	}
}

// TestPolicyRulesParsesStopTarget is #43 part 2's core parsing case:
// `target: {stop: {error, message}}` must produce a rule with Stop set and
// no Target/Provider — the terminal shape, not an alias/literal one.
func TestPolicyRulesParsesStopTarget(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"when": map[string]interface{}{"domain": "unmatched"},
				"target": map[string]interface{}{
					"stop": map[string]interface{}{
						"error":   406,
						"message": "not like that poopoohead",
					},
				},
			},
		},
	}
	rules, err := policyRules(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("rules = %+v, want 1", rules)
	}
	r := rules[0]
	if r.Stop == nil {
		t.Fatal("Stop = nil, want a parsed StopTarget")
	}
	if r.Stop.StatusCode != 406 || r.Stop.Message != "not like that poopoohead" {
		t.Errorf("Stop = %+v, want {406, \"not like that poopoohead\"}", r.Stop)
	}
	if r.Target != "" || r.Provider != "" {
		t.Errorf("Target=%q Provider=%q, want both empty on a stop rule", r.Target, r.Provider)
	}
	if r.When.Domain != "unmatched" {
		t.Errorf("When.Domain = %q, want unmatched", r.When.Domain)
	}
}

// A YAML config's numbers decode as float64, not int — the same reason
// intFromConfig exists for long_context_tokens. The stop target's "error"
// key must accept that shape too, or every real config using it fails.
func TestPolicyRulesParsesStopTargetErrorAsFloat64(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"target": map[string]interface{}{
					"stop": map[string]interface{}{
						"error":   float64(406),
						"message": "refused",
					},
				},
			},
		},
	}
	rules, err := policyRules(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rules[0].Stop == nil || rules[0].Stop.StatusCode != 406 {
		t.Fatalf("Stop = %+v, want StatusCode 406", rules[0].Stop)
	}
}

// A stop target with no message is rejected: an operator refusal with no
// explanation is as unhelpful to debug as the unmatchable-escape bug this
// ticket exists to fix.
func TestPolicyRulesRejectsStopWithoutMessage(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"target": map[string]interface{}{
					"stop": map[string]interface{}{"error": 406},
				},
			},
		},
	}
	if _, err := policyRules(cfg); err == nil {
		t.Fatal("expected an error: stop target with no message")
	}
}

// An invalid HTTP status code must be rejected at config load, not surface
// as a confusing runtime failure the first time the rule matches.
func TestPolicyRulesRejectsStopWithInvalidStatusCode(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"target": map[string]interface{}{
					"stop": map[string]interface{}{"error": 6000, "message": "nope"},
				},
			},
		},
	}
	if _, err := policyRules(cfg); err == nil {
		t.Fatal("expected an error: stop target with an out-of-range status code")
	}
}

// Stop is mutually exclusive with target/provider — combining them is a
// config mistake worth catching at load rather than silently picking one.
func TestPolicyRulesRejectsStopCombinedWithProvider(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"provider": "claude",
				"target": map[string]interface{}{
					"stop": map[string]interface{}{"error": 406, "message": "nope"},
				},
			},
		},
	}
	if _, err := policyRules(cfg); err == nil {
		t.Fatal("expected an error: rule sets both stop and provider")
	}
}

// The catch-all/default fallback position (`when: {}`) is the documented
// second place a stop target is valid — it must parse identically to a
// stop on a specific rule.
func TestPolicyRulesParsesStopInCatchAllPosition(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"provider": "claude", "when": map[string]interface{}{"domain": "code_generation"}},
			map[string]interface{}{
				"target": map[string]interface{}{
					"stop": map[string]interface{}{"error": 400, "message": "no policy for this request"},
				},
			},
		},
	}
	rules, err := policyRules(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 2 || rules[1].Stop == nil {
		t.Fatalf("rules = %+v, want rule 1 to carry a Stop target", rules)
	}
}

// #11: a rule's "when" clause can match on request_kind ("title", later
// "subagent") — not an axis, but still a legitimate condition, so routing
// can send title-generation traffic to a cheap/fast alias.
func TestPolicyRulesParsesRequestKind(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"target": "cheap-claude",
				"when":   map[string]interface{}{"request_kind": "title"},
			},
		},
	}
	rules, err := policyRules(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rules[0].When.RequestKind != "title" {
		t.Fatalf("When.RequestKind = %q, want title", rules[0].When.RequestKind)
	}
}

// The bug: the singular `capability` is not a key the parser reads, so a rule
// whose only condition it was parsed to an all-zero PolicyCondition, which
// Matches treats as a wildcard. The rule silently became a catch-all and
// swallowed every request behind it. An unrecognised key must be a load error
// instead, named so the typo is obvious.
func TestPolicyRulesRejectsSingularCapabilityKey(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"target": "imageparse",
				"when":   map[string]interface{}{"capability": "vision"},
			},
		},
	}
	_, err := policyRules(cfg)
	if err == nil {
		t.Fatal("expected an error: `capability` is not a recognised `when` key")
	}
	if !strings.Contains(err.Error(), "capability") {
		t.Errorf("error = %q, want it to name the offending key", err)
	}
}

// Every unrecognised key is reported, not just the first one met: map
// iteration order is random, so a single-key error would be non-deterministic
// whenever a rule carries several typos.
func TestPolicyRulesRejectsEveryUnknownWhenKey(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"target": "imageparse",
				"when": map[string]interface{}{
					"domain":     "chat",
					"capability": "vision",
					"efort":      "hard",
				},
			},
		},
	}
	_, err := policyRules(cfg)
	if err == nil {
		t.Fatal("expected an error for two unknown keys")
	}
	for _, want := range []string{"capability", "efort"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), `"domain"`) {
		t.Errorf("error = %q, names the valid key `domain` as unknown", err)
	}
}

// `effort` is a valid `when` key as of phase 2 — it matches the reasoning
// effort the CLIENT requested (types.Signals.ClientEffort), now that phase 1
// freed the word `effort` by renaming the classifier axis to `difficulty`.
// This supersedes the phase-1 stopgap that rejected the `effort` spelling here
// (the key is now current, not removed — the hard error still stands on the
// force-map path, where `effort` never became a valid key).
func TestPolicyRulesAcceptsEffortKeyForClientKnob(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"target": "coding-hard",
				"when":   map[string]interface{}{"effort": "high"},
			},
		},
	}
	rules, err := policyRules(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 1 || rules[0].When.Effort != "high" {
		t.Fatalf("rules = %+v, want one rule with Effort=high", rules)
	}
}

// A near-miss spelling of the client-effort key is still an unknown key — the
// rename hint applies to the classifier axis, not to a typo of the new key.
func TestPolicyRulesRejectsMisspelledEffortKey(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"target": "coding-hard",
				"when":   map[string]interface{}{"efort": "high"},
			},
		},
	}
	_, err := policyRules(cfg)
	if err == nil {
		t.Fatal("expected an error for the misspelled key `efort`")
	}
	if !strings.Contains(err.Error(), "efort") {
		t.Errorf("error = %q, want it to name the offending key `efort`", err)
	}
}

// A `when` that parses to no predicates is a catch-all by accident unless the
// operator wrote it that way. An explicitly empty map is the legal spelling of
// "match everything"; a non-empty map that yields nothing (a typo'd key, or a
// value of the wrong shape) is not.
func TestPolicyRulesRejectsWhenThatYieldsNoPredicates(t *testing.T) {
	for name, when := range map[string]interface{}{
		// A scalar where a list belongs: the parser only accepts a list for
		// `capabilities`, so this silently produced an empty condition.
		"capabilities as a scalar": map[string]interface{}{"capabilities": "vision"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := map[string]interface{}{
				"rules": []interface{}{
					map[string]interface{}{"target": "imageparse", "when": when},
				},
			}
			if _, err := policyRules(cfg); err == nil {
				t.Fatal("expected an error: `when` yields no predicates, so the rule would match everything")
			}
		})
	}
}

// The deliberate catch-all must keep working — it is a documented shape, both
// as `when: {}` and as a rule with no `when` at all.
func TestPolicyRulesStillAcceptsDeliberateCatchAll(t *testing.T) {
	for name, rule := range map[string]map[string]interface{}{
		"explicitly empty when": {"target": "free-fast", "when": map[string]interface{}{}},
		"no when key at all":    {"target": "free-fast"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := map[string]interface{}{"rules": []interface{}{rule}}
			rules, err := policyRules(cfg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(rules) != 1 || rules[0].Target != "free-fast" {
				t.Fatalf("rules = %+v, want one rule targeting free-fast", rules)
			}
		})
	}
}

// An empty list on its own constrains nothing, so the rule would still match
// every request — the same silent catch-all in a different costume. It is an
// error like any other non-empty `when` that sets no predicate. Written
// alongside a real predicate it is fine; see the test below.
func TestPolicyRulesRejectsLoneEmptyCapabilityList(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"target": "free-fast",
				"when":   map[string]interface{}{"capabilities": []interface{}{}},
			},
		},
	}
	if _, err := policyRules(cfg); err == nil {
		t.Fatal("expected an error: a lone `capabilities: []` constrains nothing, so the rule matches everything")
	}
}

// …but the same empty list next to a real predicate is harmless: the predicate
// is what the rule matches on, and the empty list is just an axis left
// unconstrained.
func TestPolicyRulesAcceptsEmptyCapabilityListAlongsidePredicate(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"target": "free-fast",
				"when":   map[string]interface{}{"domain": "chat", "capabilities": []interface{}{}},
			},
		},
	}
	rules, err := policyRules(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rules[0].When.Domain != "chat" {
		t.Fatalf("When.Domain = %q, want chat", rules[0].When.Domain)
	}
}

// A wrong-shaped value next to a valid predicate is the case the catch-all
// check CANNOT see, and the one that makes shape validation necessary: the
// rule still matches on its real predicate, so it is not a catch-all, but the
// mistyped field silently contributes nothing. `domain: chat` plus a scalar
// `capabilities` would have routed every chat request while appearing to
// require vision.
func TestPolicyRulesRejectsWrongShapedValueAlongsidePredicate(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"target": "imageparse",
				"when":   map[string]interface{}{"domain": "chat", "capabilities": "vision"},
			},
		},
	}
	_, err := policyRules(cfg)
	if err == nil {
		t.Fatal("expected an error: `capabilities` is a scalar where a list belongs, so it contributes no predicate")
	}
	if !strings.Contains(err.Error(), "capabilities") {
		t.Errorf("error = %q, want it to name `capabilities`", err)
	}
}

// Same class, other direction: a list where a string belongs.
func TestPolicyRulesRejectsListWhereStringExpected(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"target": "free-fast",
				"when":   map[string]interface{}{"domain": []interface{}{"chat"}},
			},
		},
	}
	if _, err := policyRules(cfg); err == nil {
		t.Fatal("expected an error: `domain` is a list where a string belongs")
	}
}

// A rule that carries both a real predicate and a typo'd key keeps the real
// one: it is narrower than intended, not a catch-all. Only a rule whose every
// key is unusable becomes one, so the two cases need distinguishing.
func TestPolicyRulesKeepsRealPredicateAlongsideUnknownKey(t *testing.T) {
	cfg := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{
				"target": "free-fast",
				"when":   map[string]interface{}{"domain": "chat"},
			},
		},
	}
	rules, err := policyRules(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rules[0].When.Domain != "chat" {
		t.Fatalf("When.Domain = %q, want chat", rules[0].When.Domain)
	}
}
