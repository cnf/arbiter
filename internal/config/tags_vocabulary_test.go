package config

import (
	"strings"
	"testing"
)

// policyRouterConfig returns a loadable config: baseConfig's providers and
// logging, but with a "policy" router (not baseConfig's "simple" one) so a
// rules list can be set.
func policyConfigBase(rulesYAML string) string {
	return `
version: "1.0"
providers:
  claude:
    type: "anthropic"
    endpoint: "https://api.anthropic.com"
    models: ["claude-3-opus", "claude-3-haiku"]
routers:
  - name: "policy"
    type: "policy"
    config:
      rules:
` + rulesYAML + `
logging:
  level: "info"
`
}

// A `when: {tags: [...]}` rule naming a tag no classifier or force-alias can
// ever produce is rejected at load time — it is almost always a typo, and
// without this check the rule would silently never match on that condition.
func TestPolicyRuleRejectsUnproducibleTag(t *testing.T) {
	err := loadConfig(t, policyConfigBase(`        - when: { tags: ["python"] }
          provider: "claude"
`))
	if err == nil || !strings.Contains(err.Error(), `requires tag "python"`) {
		t.Fatalf("want an unproducible-tag error, got %v", err)
	}
}

// A heuristic classifier filling "tags" declares its vocabulary as its
// keyword GROUP NAMES — each is a value Classify can actually assign — so a
// rule naming one of them is accepted.
func TestPolicyRuleAcceptsTagProducedByHeuristicKeywords(t *testing.T) {
	err := loadConfig(t, policyConfigBase(`        - when: { tags: ["python"] }
          provider: "claude"
`)+`
classifiers:
  - name: "lang"
    type: "heuristic"
    axis: "tags"
    config:
      keywords: { python: ["def "], cpp: ["#include"] }
`)
	if err != nil {
		t.Fatalf("want a producible tag accepted, got %v", err)
	}
}

// An "llm" classifier filling "tags" declares its vocabulary as its label
// names.
func TestPolicyRuleAcceptsTagProducedByLLMLabels(t *testing.T) {
	err := loadConfig(t, policyConfigBase(`        - when: { tags: ["python"] }
          provider: "claude"
`)+`
aliases:
  jev:
    type: "pinned"
    provider: "claude"
    model: "claude-3-haiku"
classifiers:
  - name: "lang-fallback"
    type: "heuristic"
    axis: "tags"
    config:
      keywords: { none: ["xyzzy"] }
  - name: "lang-llm"
    type: "llm"
    axis: "tags"
    config:
      alias: "jev"
      labels: ["python", "cpp"]
      fallback: "lang-fallback"
`)
	if err != nil {
		t.Fatalf("want a producible tag accepted, got %v", err)
	}
}

// A "decisions" classifier's "choice" question on the tags axis declares its
// vocabulary as its label names; a "noul" question declares its single
// "value". Both are producible. Self-contained (not built on policyConfigBase
// or decisionsBase) because it needs its own provider/router/alias
// combination: a decisions-type provider AND a policy router with rules.
func TestPolicyRuleAcceptsTagProducedByDecisionsQuestions(t *testing.T) {
	err := loadConfig(t, `
version: "1.0"
providers:
  or-decisions:
    type: "decisions"
    endpoint: "https://openrouter.ai/api/alpha/decisions"
    models: ["~typesafe/jev-latest"]
routers:
  - name: "policy"
    type: "policy"
    config:
      rules:
        - when: { tags: ["python"] }
          provider: "or-decisions"
        - when: { tags: ["french"] }
          provider: "or-decisions"
logging:
  level: "info"
aliases:
  jev:
    type: "pinned"
    provider: "or-decisions"
    model: "~typesafe/jev-latest"
classifiers:
  - name: "fb"
    type: "heuristic"
    config:
      keywords: { chat: ["hi"] }
  - name: "multi-tag"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        language:
          axis: "tags"
          type: "choice"
          labels: ["python", "cpp"]
        is_french:
          axis: "tags"
          type: "noul"
          value: "french"
      fallback: "fb"
`)
	if err != nil {
		t.Fatalf("want both decisions-produced tags accepted, got %v", err)
	}
}

// A force-alias (`force: {tags: [...]}`) declares its vocabulary directly.
func TestPolicyRuleAcceptsTagProducedByForceAlias(t *testing.T) {
	err := loadConfig(t, policyConfigBase(`        - when: { tags: ["python"] }
          provider: "claude"
`)+`
aliases:
  py:
    force: { tags: ["python"] }
`)
	if err != nil {
		t.Fatalf("want a force-alias-produced tag accepted, got %v", err)
	}
}

// A rule with no `when`, or a `when` that sets no `tags` key, requires
// nothing — the vocabulary check must not fire when there is nothing to
// check, even with zero classifiers configured.
func TestPolicyRuleWithNoTagsRequirementNeedsNoVocabulary(t *testing.T) {
	err := loadConfig(t, policyConfigBase(`        - when: { domain: "code_generation" }
          provider: "claude"
`))
	if err != nil {
		t.Fatalf("want no vocabulary check triggered, got %v", err)
	}
}
