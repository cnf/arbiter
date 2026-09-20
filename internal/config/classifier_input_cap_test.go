package config

import (
	"strings"
	"testing"
)

// The `max_input_chars` cap is read by the builder with a helper that returns 0
// for any shape it does not recognise, and 0 means "unset, take the default".
// So a value the builder cannot read would silently become the default while the
// operator believes they set a cap — the same class of silent-drop failure the
// `match` validation above exists to prevent.

func TestClassifierMaxInputCharsLoads(t *testing.T) {
	if err := loadConfig(t, baseConfig+`
classifiers:
  - name: "caps"
    type: "heuristic"
    axis: "capabilities"
    config:
      detect: ["vision"]
      max_input_chars: 2048
`); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// A negative value is the explicit "no limit" opt-out, so it must load: the
// whole reason unset gets a default is that unlimited has to be asked for.
func TestClassifierMaxInputCharsAcceptsNegativeForUnlimited(t *testing.T) {
	if err := loadConfig(t, baseConfig+`
classifiers:
  - name: "caps"
    type: "heuristic"
    axis: "capabilities"
    config:
      detect: ["vision"]
      max_input_chars: -1
`); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// 0 would read as "unset" and take the default, so an operator writing it
// expects either no limit or no input at all — both different from what they
// would get. Rejected rather than guessed at.
func TestClassifierMaxInputCharsRejectsZero(t *testing.T) {
	err := loadConfig(t, baseConfig+`
classifiers:
  - name: "caps"
    type: "heuristic"
    axis: "capabilities"
    config:
      detect: ["vision"]
      max_input_chars: 0
`)
	if err == nil {
		t.Fatal("expected an error for max_input_chars: 0")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error = %v, want it to name the ambiguity", err)
	}
}

// A string is the shape that would silently take the default. YAML unquoted
// "8k" is a string, and an operator writing it believes they set a cap.
func TestClassifierMaxInputCharsRejectsNonInteger(t *testing.T) {
	err := loadConfig(t, baseConfig+`
classifiers:
  - name: "caps"
    type: "heuristic"
    axis: "capabilities"
    config:
      detect: ["vision"]
      max_input_chars: "8k"
`)
	if err == nil {
		t.Fatal("expected an error for a non-integer max_input_chars")
	}
	if !strings.Contains(err.Error(), "whole number") {
		t.Errorf("error = %v, want it to say a whole number is required", err)
	}
}
