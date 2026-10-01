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

func TestDecisionsClassifierRejectsUnknownQuestionType(t *testing.T) {
	err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "effort-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        effort:
          axis: "difficulty"
          type: "guess"
          labels: ["easy", "hard"]
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), "not a known primitive") {
		t.Fatalf("want an unknown-question-type error, got %v", err)
	}
}

// A "score" question must declare "levels", not "choice"'s "labels" — sending
// the wrong field is exactly the silent no-op this validation exists to catch.
func TestDecisionsClassifierRejectsLabelsOnScoreQuestion(t *testing.T) {
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
	if err == nil || !strings.Contains(err.Error(), `"labels" is not valid for type "score"`) {
		t.Fatalf("want a labels-not-valid-for-score error, got %v", err)
	}
}

// A "noul" question is the yes/no primitive: it declares "value" (the axis
// value applied on yes) instead of "labels".
func TestDecisionsClassifierLoadsNoulQuestion(t *testing.T) {
	if err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "budget-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        is_budget:
          axis: "cost_class"
          type: "noul"
          value: "budget"
          instructions: "Is this a cost-sensitive request?"
      fallback: "domain-heuristic"
`); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestDecisionsClassifierRejectsNoulWithoutValue(t *testing.T) {
	err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "budget-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        is_budget:
          axis: "cost_class"
          type: "noul"
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), `requires a non-empty "value"`) {
		t.Fatalf("want a missing-value error, got %v", err)
	}
}

func TestDecisionsClassifierRejectsNoulWithLabels(t *testing.T) {
	err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "budget-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        is_budget:
          axis: "cost_class"
          type: "noul"
          value: "budget"
          labels: ["budget", "quality_first"]
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), `"labels" is not valid for type "noul"`) {
		t.Fatalf("want a labels-not-valid error, got %v", err)
	}
}

func TestDecisionsClassifierRejectsNoulWithEscape(t *testing.T) {
	err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "budget-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        is_budget:
          axis: "cost_class"
          type: "noul"
          value: "budget"
          escape: "none"
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), `"escape" is not valid for type "noul"`) {
		t.Fatalf("want an escape-not-valid error, got %v", err)
	}
}

func TestDecisionsClassifierRejectsNoulOnCapabilitiesAxis(t *testing.T) {
	err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "vision-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        needs_vision:
          axis: "capabilities"
          type: "noul"
          value: "vision"
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), `does not support the capabilities axis yet`) {
		t.Fatalf("want a capabilities-not-supported error, got %v", err)
	}
}

func TestDecisionsClassifierRejectsNoulWithReservedUnmatchedValue(t *testing.T) {
	err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "budget-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        is_budget:
          axis: "cost_class"
          type: "noul"
          value: "unmatched"
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), `value "unmatched" is reserved`) {
		t.Fatalf("want a reserved-value error, got %v", err)
	}
}

// A "score" question is the ordered-rubric primitive: it declares "levels"
// (low -> high) instead of "labels", and the answer's fractional position
// snaps to the nearest one.
func TestDecisionsClassifierLoadsScoreQuestion(t *testing.T) {
	if err := loadConfig(t, decisionsBase+`
classifiers:`+domainHeuristic+`
  - name: "effort-decisions"
    type: "decisions"
    config:
      alias: "jev"
      questions:
        effort:
          axis: "difficulty"
          type: "score"
          instructions: "How much effort does this take?"
          levels:
            - name: "easy"
              description: "a one-liner or a lookup"
            - name: "medium"
            - name: "hard"
              description: "multi-file, needs design"
      fallback: "domain-heuristic"
`); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestDecisionsClassifierRejectsScoreWithoutLevels(t *testing.T) {
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
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), `requires at least two "levels"`) {
		t.Fatalf("want a missing-levels error, got %v", err)
	}
}

func TestDecisionsClassifierRejectsScoreWithOneLevel(t *testing.T) {
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
          levels: ["easy"]
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), `requires at least two "levels"`) {
		t.Fatalf("want a too-few-levels error, got %v", err)
	}
}

func TestDecisionsClassifierRejectsScoreWithDuplicateLevelNames(t *testing.T) {
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
          levels: ["easy", "Easy"]
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), `is declared more than once`) {
		t.Fatalf("want a duplicate-level error, got %v", err)
	}
}

func TestDecisionsClassifierRejectsScoreWithReservedUnmatchedLevel(t *testing.T) {
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
          levels: ["easy", "unmatched"]
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), `is reserved`) {
		t.Fatalf("want a reserved-level error, got %v", err)
	}
}

func TestDecisionsClassifierRejectsScoreWithValue(t *testing.T) {
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
          value: "hard"
          levels: ["easy", "hard"]
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), `"value" is not valid for type "score"`) {
		t.Fatalf("want a value-not-valid error, got %v", err)
	}
}

func TestDecisionsClassifierRejectsScoreWithEscape(t *testing.T) {
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
          escape: "none"
          levels: ["easy", "hard"]
      fallback: "domain-heuristic"
`)
	if err == nil || !strings.Contains(err.Error(), `"escape" is not valid for type "score"`) {
		t.Fatalf("want an escape-not-valid error, got %v", err)
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
