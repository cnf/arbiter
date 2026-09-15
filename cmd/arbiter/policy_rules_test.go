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
