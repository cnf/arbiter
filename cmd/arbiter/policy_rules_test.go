package main

import "testing"

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
