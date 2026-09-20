package main

import (
	"testing"

	"github.com/cnf/arbiter/internal/config"
)

// maxInputChars is the one line between a validated config field and a
// classifier that honours it, and nothing else covers it: the config package
// proves the value is accepted, the classifier package proves the Full
// constructor applies it, and neither would notice this helper reading the
// wrong key. That failure is silent in the way this whole feature exists to
// prevent — the config loads, the classifier is built, and the cap is simply
// the default the operator thought they had overridden.
func TestMaxInputCharsReadsTheConfiguredKey(t *testing.T) {
	cc := config.ClassifierConfig{
		Name:   "routing-decisions",
		Config: map[string]interface{}{"max_input_chars": 2048},
	}
	if got := maxInputChars(cc); got != 2048 {
		t.Errorf("maxInputChars = %d, want 2048 (the configured cap)", got)
	}
}

// An absent field must read as 0, which is the sentinel the Full constructors
// turn into the safe default. It must NOT read as "unlimited" — that is the
// defect this feature fixes — and it must not read as a negative sentinel,
// which would mean the opposite.
func TestMaxInputCharsUnsetReadsAsTheDefaultSentinel(t *testing.T) {
	cc := config.ClassifierConfig{Name: "routing-decisions", Config: map[string]interface{}{}}
	if got := maxInputChars(cc); got != 0 {
		t.Errorf("maxInputChars with no cap set = %d, want 0 (the constructors read 0 as \"take the default\")", got)
	}
}
