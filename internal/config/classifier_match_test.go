package config

import (
	"strings"
	"testing"
)

// The `match` block lets a classifier label a request by its own text — a
// title generator's system prompt, say — which is text no classifier read
// before. A shape the builder would drop must be a load error here, because a
// dropped match block looks exactly like one that never hits: the operator
// concludes the signature is wrong rather than the config.
func TestClassifierMatchLoads(t *testing.T) {
	if err := loadConfig(t, baseConfig+`
classifiers:
  - name: "request-kind"
    type: "heuristic"
    axis: "domain"
    config:
      match:
        - mode: "prefix"
          pattern: "You are a title generator."
        - mode: "regex"
          pattern: "(?i)you are (a|the) .{0,20}title (generator|writer)"
      value: "title_generation"
      where: ["system"]
      decisive: true
`); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// A bare string is one prefix pattern: the shape an operator writes for the
// common case, and forcing a list for one entry would be noise.
func TestClassifierMatchAcceptsBareString(t *testing.T) {
	if err := loadConfig(t, baseConfig+`
classifiers:
  - name: "request-kind"
    type: "heuristic"
    axis: "domain"
    config:
      match: "You are a title generator."
      value: "title_generation"
`); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestClassifierMatchRequiresValue(t *testing.T) {
	err := loadConfig(t, baseConfig+`
classifiers:
  - name: "request-kind"
    type: "heuristic"
    axis: "domain"
    config:
      match: "You are a title generator."
`)
	if err == nil || !strings.Contains(err.Error(), `match requires "value"`) {
		t.Fatalf("want a missing-value error, got %v", err)
	}
}

// An empty pattern would match everything, which is never what an operator
// means — a classifier would label every request as a title generation.
func TestClassifierMatchRejectsEmptyPattern(t *testing.T) {
	err := loadConfig(t, baseConfig+`
classifiers:
  - name: "request-kind"
    type: "heuristic"
    axis: "domain"
    config:
      match:
        - mode: "prefix"
          pattern: ""
      value: "title_generation"
`)
	if err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("want an empty-pattern error, got %v", err)
	}
}

func TestClassifierMatchRejectsBadRegex(t *testing.T) {
	err := loadConfig(t, baseConfig+`
classifiers:
  - name: "request-kind"
    type: "heuristic"
    axis: "domain"
    config:
      match:
        - mode: "regex"
          pattern: "(unclosed"
      value: "title_generation"
`)
	if err == nil || !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("want an invalid-regex error, got %v", err)
	}
}

func TestClassifierMatchRejectsUnknownWhere(t *testing.T) {
	err := loadConfig(t, baseConfig+`
classifiers:
  - name: "request-kind"
    type: "heuristic"
    axis: "domain"
    config:
      match: "x"
      value: "title_generation"
      where: ["prompt"]
`)
	if err == nil || !strings.Contains(err.Error(), "unknown where") {
		t.Fatalf("want an unknown-where error, got %v", err)
	}
}

// Structural capability detection: the request either carries the bytes or it
// does not, so this needs no signature from the operator.
func TestCapabilityDetectLoads(t *testing.T) {
	if err := loadConfig(t, baseConfig+`
classifiers:
  - name: "capability"
    type: "capability_detector"
    config:
      detect: ["tool_use", "attachment"]
      keywords:
        vision: ["screenshot"]
`); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// long_context is the one capability needing a threshold: without one it can
// never fire, and "never fires" is indistinguishable from "the request was
// short".
func TestCapabilityDetectLongContextRequiresThreshold(t *testing.T) {
	err := loadConfig(t, baseConfig+`
classifiers:
  - name: "capability"
    type: "capability_detector"
    config:
      detect: ["long_context"]
`)
	if err == nil || !strings.Contains(err.Error(), "long_context_tokens") {
		t.Fatalf("want a missing-threshold error, got %v", err)
	}

	if err := loadConfig(t, baseConfig+`
classifiers:
  - name: "capability"
    type: "capability_detector"
    config:
      detect: ["long_context"]
      long_context_tokens: 100000
`); err != nil {
		t.Fatalf("Load with a threshold: %v", err)
	}
}

func TestCapabilityDetectRejectsUnknownCapability(t *testing.T) {
	err := loadConfig(t, baseConfig+`
classifiers:
  - name: "capability"
    type: "capability_detector"
    config:
      detect: ["telepathy"]
`)
	if err == nil || !strings.Contains(err.Error(), "unknown capability") {
		t.Fatalf("want an unknown-capability error, got %v", err)
	}
}

// detect fills the capabilities axis, so it is meaningless anywhere else — the
// structural hit would be discarded by the axis it is written to.
func TestCapabilityDetectRejectsWrongAxis(t *testing.T) {
	err := loadConfig(t, baseConfig+`
classifiers:
  - name: "domain"
    type: "heuristic"
    axis: "domain"
    config:
      detect: ["tool_use"]
`)
	if err == nil || !strings.Contains(err.Error(), "capabilities axis") {
		t.Fatalf("want a wrong-axis error, got %v", err)
	}
}

// A decisive matcher ends classification for everything after it, so its
// type must be a boolean rather than a truthy string.
func TestClassifierMatchRejectsNonBooleanDecisive(t *testing.T) {
	err := loadConfig(t, baseConfig+`
classifiers:
  - name: "request-kind"
    type: "heuristic"
    axis: "domain"
    config:
      match: "x"
      value: "title_generation"
      decisive: "yes"
`)
	if err == nil || !strings.Contains(err.Error(), "decisive must be a boolean") {
		t.Fatalf("want a decisive-type error, got %v", err)
	}
}

// The matcher's request-kind key is `request_kind:`, the same word the router's
// `when:` clause uses — one spelling for one concept across the whole config
// language (the old `kind:` was a deliberate break, not a deprecation, so the
// old spelling must fail at load with an error that NAMES it: an operator
// upgrading a config that still says `kind:` must be told what happened, not
// handed a generic "value or request_kind required" that reads like a missing
// field).
func TestClassifierMatchAcceptsRequestKindKey(t *testing.T) {
	cfg := loadConfigOK(t, baseConfig+`
classifiers:
  - name: "request-kind"
    type: "heuristic"
    axis: "domain"
    config:
      match: "You are a title generator."
      request_kind: "title"
`)
	cc := cfg.Classifiers[0]
	if got, _ := cc.Config["request_kind"].(string); got != "title" {
		t.Fatalf("request_kind = %v, want title", cc.Config["request_kind"])
	}
}

func TestClassifierMatchRejectsOldKindSpelling(t *testing.T) {
	err := loadConfig(t, baseConfig+`
classifiers:
  - name: "request-kind"
    type: "heuristic"
    axis: "domain"
    config:
      match: "You are a title generator."
      kind: "title"
`)
	if err == nil {
		t.Fatal("the retired `kind:` spelling must be a load error, not silently dropped")
	}
	if !strings.Contains(err.Error(), `match requires "value"`) || !strings.Contains(err.Error(), "request_kind") || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("error must name both the required keys and the retired spelling, got %v", err)
	}
}
