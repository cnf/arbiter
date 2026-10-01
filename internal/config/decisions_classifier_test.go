package config

import (
	"strings"
	"testing"
)

// decisionsBase is a complete minimal config carrying a provider that speaks
// the decisions protocol. Self-contained rather than built on baseConfig,
// because baseConfig already owns `providers:` and `routers:` and YAML will
// not accept a second key of either name.
const decisionsBase = `
version: "1.0"
providers:
  claude:
    type: "anthropic"
    endpoint: "https://api.anthropic.com"
    models: ["claude-3-haiku"]
  or-decisions:
    type: "decisions"
    endpoint: "https://openrouter.ai/api/alpha/decisions"
    models: ["~typesafe/jev-latest"]
routers:
  - name: "primary"
    type: "simple"
    config:
      default_provider: "claude"
logging:
  level: "info"
aliases:
  jev:
    type: "pinned"
    provider: "or-decisions"
    model: "~typesafe/jev-latest"
  claude-pinned:
    type: "pinned"
    provider: "claude"
    model: "claude-3-haiku"
`

const domainHeuristic = `
  - name: "domain-heuristic"
    type: "heuristic"
    config:
      keywords: { code_generation: ["write"] }
`

func TestDecisionsClassifierLoads(t *testing.T) {
	if err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "domain-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        domain:
          axis: "domain"
          type: "choice"
          labels:
            code_generation: "wants code written"
            chat: "small talk"
            none: "nothing fits"
          escape: "none"
          instructions: "Pick the category."
      fallback: "domain-heuristic"
      timeout: "5s"
`); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// only_if_unset on a decisions classifier is accepted — a decision call is
// exactly the expensive upstream round trip the gate exists to avoid.
func TestDecisionsClassifierAcceptsOnlyIfUnset(t *testing.T) {
	if err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "domain-decisions"
    type: "decisions"
    only_if_unset: true
    config:
      alias: "jev"
      questions:
        domain:
          axis: "domain"
          type: "choice"
          labels:
            code_generation: "wants code written"
            chat: "small talk"
            none: "nothing fits"
          escape: "none"
      fallback: "domain-heuristic"
`); err != nil {
		t.Fatalf("Load: want only_if_unset on a decisions classifier to load, got %v", err)
	}
}

// Several questions, several axes, one classifier — the shape Phase B exists
// for. Each question declares its own axis, so the classifier itself declares
// none.
func TestDecisionsClassifierLoadsMultipleQuestions(t *testing.T) {
	if err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "multi-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        domain:
          axis: "domain"
          type: "choice"
          labels: { code_generation: "wants code", chat: "small talk" }
        cost_class:
          axis: "cost_class"
          type: "choice"
          labels: { budget: "cheap is fine", quality_first: "spend for quality" }
      fallback: "domain-heuristic"
`); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// A decisions classifier MAY fall back to an llm one — the deliberate
// relaxation of the no-chained-LLM rule. The chain still terminates because the
// llm classifier's own fallback must be a non-model classifier.
func TestDecisionsClassifierMayFallBackToLLM(t *testing.T) {
	err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "domain-llm"
    type: "llm"
    axis: "domain"
    config:
      alias: "jev"
      labels: ["code_generation", "chat"]
      fallback: "domain-heuristic"
  - name: "domain-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        domain:
          axis: "domain"
          type: "choice"
          labels: ["code_generation", "chat"]
      fallback: "domain-llm"
`)
	if err != nil {
		t.Fatalf("decisions -> llm fallback should be allowed, got: %v", err)
	}
}

// Two questions filling the same axis would race for it with nothing to break
// the tie — the verdict would depend on map iteration order.
func TestDecisionsClassifierRejectsTwoQuestionsOnOneAxis(t *testing.T) {
	err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "multi-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        first:
          axis: "domain"
          type: "choice"
          labels: ["code_generation", "chat"]
        second:
          axis: "domain"
          type: "choice"
          labels: ["code_generation", "chat"]
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), "both fill axis") {
		t.Fatalf("want a duplicate-axis error, got %v", err)
	}
}

func TestDecisionsClassifierRejectsUnsupportedQuestionType(t *testing.T) {
	err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "effort-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        effort:
          axis: "difficulty"
          type: "score"
          labels: ["easy", "hard"]
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), "not supported yet") {
		t.Fatalf("want an unsupported-question-type error, got %v", err)
	}
}

// The alias must reach a provider of type "decisions". A chat provider here
// would send a state/questions body to /chat/completions and fail at request
// time, which is a far worse place to learn it than config load.
func TestDecisionsClassifierRejectsNonDecisionsProvider(t *testing.T) {
	err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "domain-decisions"
    type: "decisions"
    config:
      alias: "claude-pinned"
      questions:
        domain:
          axis: "domain"
          type: "choice"
          labels: ["code_generation", "chat"]
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), `needs a provider of type "decisions"`) {
		t.Fatalf("want a wrong-provider-type error, got %v", err)
	}
}

// "other" is the option name this codebase sends for the escape label, so a
// label of that name would silently collide in the criteria map.
func TestDecisionsClassifierRejectsReservedOtherLabel(t *testing.T) {
	err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "domain-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        domain:
          axis: "domain"
          type: "choice"
          labels: ["code_generation", "other"]
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), `"other" is reserved`) {
		t.Fatalf("want a reserved-label error, got %v", err)
	}
}

// "unmatched" is the sentinel value #43 reserves for an escape verdict (see
// types.UnmatchedValue) — a real label of that name would be indistinguishable
// in a `when: {domain: unmatched}` rule from "nothing matched".
func TestDecisionsClassifierRejectsReservedUnmatchedLabel(t *testing.T) {
	err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "domain-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        domain:
          axis: "domain"
          type: "choice"
          labels: ["code_generation", "unmatched"]
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), `"unmatched" is reserved`) {
		t.Fatalf("want a reserved-label error, got %v", err)
	}
}

// A decisions classifier fills the axis its questions declare, so an axis on
// the classifier itself is a second, silently-ignored answer to the same
// question.
func TestDecisionsClassifierRejectsClassifierLevelAxis(t *testing.T) {
	err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "domain-decisions"
    type: "decisions"
    axis: "domain"
    config:
      alias: "jev"
      questions:
        domain:
          axis: "domain"
          type: "choice"
          labels: ["code_generation", "chat"]
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), "must not set axis") {
		t.Fatalf("want a must-not-set-axis error, got %v", err)
	}
}

func TestDecisionsClassifierRejectsMissingFallback(t *testing.T) {
	err := loadConfig(t, decisionsBase+`
classifiers:
  - name: "domain-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        domain:
          axis: "domain"
          type: "choice"
          labels: ["code_generation", "chat"]
`)
	if err == nil || !strings.Contains(err.Error(), `requires "fallback"`) {
		t.Fatalf("want a missing-fallback error, got %v", err)
	}
}

// A decision model's answers are only as good as its criteria, so an escape
// label that is not a declared label is a config error rather than a runtime
// surprise.
func TestDecisionsClassifierRejectsEscapeNotALabel(t *testing.T) {
	err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "domain-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        domain:
          axis: "domain"
          type: "choice"
          labels: ["code_generation", "chat"]
          escape: "nothing"
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), "is not one of the declared labels") {
		t.Fatalf("want an escape-not-a-label error, got %v", err)
	}
}

// The axis must be named on the question; without one there is no way to know
// which Signals field the answer belongs to.
func TestDecisionsClassifierRejectsQuestionWithoutAxis(t *testing.T) {
	err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "domain-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        domain:
          type: "choice"
          labels: ["code_generation", "chat"]
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), `requires "axis"`) {
		t.Fatalf("want a missing-axis error, got %v", err)
	}
}
