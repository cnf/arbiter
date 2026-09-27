package config

import (
	"regexp"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

// TestShippedTitlePatternsAreTheIntendedRegexes keeps the shipped
// `request-kind` classifier honest against the prompts it was written from.
//
// The patterns live inside YAML double quotes, where a backslash must be doubled.
// A single-backslash mistake is invisible in the file, loads without error, and
// shows up only as a pattern that never matches at runtime — which is exactly the
// silent "the signature is wrong" conclusion this classifier exists to prevent.
// So the loaded patterns are compared to the intended regexes, compiled, and run
// against the real prompts.
func TestShippedTitlePatternsAreTheIntendedRegexes(t *testing.T) {
	cfg, err := Load("../../arbiter.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]string{
		"hermes":   `(?i)^\s*You name chat sessions\.`,
		"opencode": `(?i)^\s*You are a title generator\.`,
		"claude":   `(?i)Generate a concise, sentence-case title`,
	}
	var got []string
	for _, c := range cfg.Classifiers {
		if c.Name != "request-kind" {
			continue
		}
		patterns, err := types.ParseMatchPatterns(c.Config["match"])
		if err != nil {
			t.Fatalf("ParseMatchPatterns: %v", err)
		}
		for _, p := range patterns {
			got = append(got, p.Pattern)
			if types.MatchMode(p.Mode) != types.MatchRegex {
				t.Errorf("pattern %q loaded with mode %q, want regex", p.Pattern, p.Mode)
			}
		}
	}
	if len(got) != 3 {
		t.Fatalf("got %d patterns on request-kind, want 3: %q", len(got), got)
	}
	names := []string{"hermes", "opencode", "claude"}
	for i, name := range names {
		if got[i] != want[name] {
			t.Errorf("%s pattern as loaded:\n  got  %q\n  want %q", name, got[i], want[name])
		}
		if _, err := regexp.Compile(got[i]); err != nil {
			t.Errorf("%s pattern does not compile: %v", name, err)
		}
	}

	// The patterns must actually match the live prompts they were written from.
	cases := []struct {
		name   string
		prompt string
		want   int // index of the pattern that must match
	}{
		{"hermes", "You name chat sessions. Given the user's opening message, write a title...", 0},
		{"opencode", "You are a title generator. You output ONLY a thread title. Nothing else.", 1},
		{"claude", "x-anthropic-billing-header: cc_version=2.1.223.ec2; cc_entrypoint=cli;\n\nYou are Claude Code, Anthropic's official CLI for Claude.\n\nGenerate a concise, sentence-case title (3-7 words) that captures the main topic", 2},
	}
	for _, tc := range cases {
		var hit = -1
		for i, p := range got {
			if regexp.MustCompile(p).MatchString(tc.prompt) {
				hit = i
				break
			}
		}
		if hit != tc.want {
			t.Errorf("%s: matched pattern %d, want %d", tc.name, hit, tc.want)
		}
	}

	// And the false-positive that motivated the anchored/long patterns: the
	// Hermes agent prompt mentions title-generation and must NOT match.
	falsePositive := "You are Hermes Agent, built by Nous Research. Be direct...\n" +
		"title-generation grouping in the UI is deferred, gated on real multi-agent traffic"
	for i, p := range got {
		if regexp.MustCompile(p).MatchString(falsePositive) {
			t.Errorf("pattern %d (%q) matched the Hermes agent prompt, a false positive", i, p)
		}
	}
}
