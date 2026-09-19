package main

import (
	"strings"
	"testing"

	"github.com/cnf/arbiter/internal/config"
)

// A match-only classifier is the shape a title-gen detector actually takes: the
// signature is structural, so there is nothing to list as keywords. The builder
// used to demand one of [keywords detectors] and refused to start — which is
// the error this pins.
func TestBuildHeuristicClassifierAcceptsMatchOnly(t *testing.T) {
	cc := config.ClassifierConfig{
		Name: "request-kind",
		Type: "heuristic",
		Axis: "domain",
		Config: map[string]interface{}{
			"match":    "You name chat sessions.",
			"value":    "title_generation",
			"decisive": true,
			"where":    []interface{}{"system"},
		},
	}
	c, err := buildHeuristicClassifier(cc, "domain")
	if err != nil {
		t.Fatalf("buildHeuristicClassifier: %v", err)
	}
	if c == nil {
		t.Fatal("no classifier built")
	}
}

// detect-only is the other new shape: a capability classifier proven entirely
// from request shape.
func TestBuildHeuristicClassifierAcceptsDetectOnly(t *testing.T) {
	cc := config.ClassifierConfig{
		Name: "capability", Type: "capability_detector",
		Config: map[string]interface{}{
			"detect": []interface{}{"tool_use", "attachment"},
		},
	}
	if _, err := buildHeuristicClassifier(cc, "capabilities"); err != nil {
		t.Fatalf("buildHeuristicClassifier: %v", err)
	}
}

// Keywords alone still work, unchanged from before any of this existed.
func TestBuildHeuristicClassifierAcceptsKeywordsOnly(t *testing.T) {
	cc := config.ClassifierConfig{
		Name: "domain", Type: "heuristic",
		Config: map[string]interface{}{
			"keywords": map[string]interface{}{
				"code_generation": []interface{}{"write"},
			},
		},
	}
	if _, err := buildHeuristicClassifier(cc, "domain"); err != nil {
		t.Fatalf("buildHeuristicClassifier: %v", err)
	}
}

// A classifier with NO signals cannot ever produce anything, so it is still
// refused — the message just names all three ways to give it one.
func TestBuildHeuristicClassifierRejectsNoSignals(t *testing.T) {
	cc := config.ClassifierConfig{Name: "empty", Type: "heuristic", Config: map[string]interface{}{}}
	_, err := buildHeuristicClassifier(cc, "domain")
	if err == nil {
		t.Fatal("want an error for a classifier with no keywords, match or detect")
	}
	for _, want := range []string{"keywords", "match", "detect"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// keywords present but the wrong shape is still an error: an absent map means
// "none", but a list where a map belongs is a mistake that must not be read as
// absent.
func TestBuildHeuristicClassifierRejectsMalformedKeywords(t *testing.T) {
	cc := config.ClassifierConfig{
		Name: "domain", Type: "heuristic",
		Config: map[string]interface{}{
			"keywords": []interface{}{"write", "refactor"},
		},
	}
	if _, err := buildHeuristicClassifier(cc, "domain"); err == nil {
		t.Fatal("want an error for keywords that are a list, not a map")
	}
}

// The real config shape from the deployment, built end to end, so a change that
// only breaks the full path is caught here rather than at process start.
func TestBuildDecisiveTitleMatcherEndToEnd(t *testing.T) {
	cc := config.ClassifierConfig{
		Name: "request-kind", Type: "heuristic", Axis: "domain",
		Config: map[string]interface{}{
			"match": []interface{}{
				map[string]interface{}{"mode": "prefix", "pattern": "You name chat sessions."},
				map[string]interface{}{"mode": "regex", "pattern": `(?i)^\s*you (name|title|summari[sz]e) (chat )?sessions?\b`},
			},
			"value":    "title_generation",
			"where":    []interface{}{"system"},
			"decisive": true,
		},
	}
	if _, err := buildHeuristicClassifier(cc, "domain"); err != nil {
		t.Fatalf("buildHeuristicClassifier: %v", err)
	}
}
