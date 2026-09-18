package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/cnf/arbiter/internal/config"
)

// The README documents the guardrail `type` strings in a table, and those
// strings are resolved here in buildGuardrail rather than in config.Validate —
// so nothing else catches a doc that names a type which does not exist. That is
// not hypothetical: the first version of the Guardrails section documented
// `prompt_replace`, a name that was never implemented, and every test passed.
//
// This test reads the type column straight out of README.md and asks
// buildGuardrail to build each one, so the docs and the switch cannot drift.
func TestReadmeGuardrailTypesAreBuildable(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("read README: %v", err)
	}

	// The type table in the Guardrails section: rows of the form
	// | `type_name` | description |
	section := regexp.MustCompile(`(?s)## Guardrails.*?(?:\n## |\z)`).FindString(string(readme))
	if section == "" {
		t.Fatal("could not find the '## Guardrails' section in README.md")
	}

	// Skip the header row and its `|---|---|` separator: the header's first
	// column is the literal word `type`, which is not a type name.
	row := regexp.MustCompile("(?m)^\\|\\s*`([a-z_]+)`\\s*\\|")
	matches := row.FindAllStringSubmatch(section, -1)
	filtered := matches[:0]
	for _, m := range matches {
		if m[1] != "type" {
			filtered = append(filtered, m)
		}
	}
	matches = filtered
	if len(matches) == 0 {
		t.Fatal("no `type` table rows found under '## Guardrails' — the table moved or changed shape")
	}

	// Minimal config per type, so a build failure means the *type* is wrong
	// rather than a missing required key. prompt_rewrite's match is required.
	cfgFor := map[string]map[string]interface{}{
		"system_prompt":  {"prompt": "test"},
		"rate_limit":     {"per_minute": 1},
		"prompt_rewrite": {"match": "test", "mode": "prefix", "action": "strip"},
	}

	for _, m := range matches {
		typeName := m[1]
		t.Run(typeName, func(t *testing.T) {
			cfg, ok := cfgFor[typeName]
			if !ok {
				t.Fatalf("README documents guardrail type %q but this test has no sample config for it; "+
					"if the type is real, add it here — if it is not, the docs are wrong", typeName)
			}
			gc := config.GuardrailConfig{Name: "doc-" + typeName, Type: typeName, Config: cfg}
			if _, err := buildGuardrail(gc, nil); err != nil {
				t.Errorf("README documents guardrail type %q but it does not build: %v", typeName, err)
			}
		})
	}
}

// TestReadmeGuardrailTypesCoverTheSwitch is the other direction: every type the
// switch accepts should be documented, so a new guardrail cannot ship without a
// line in the table.
func TestReadmeGuardrailTypesCoverTheSwitch(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	section := regexp.MustCompile(`(?s)## Guardrails.*?(?:\n## |\z)`).FindString(string(readme))
	if section == "" {
		t.Fatal("could not find the '## Guardrails' section in README.md")
	}

	// The type list is the switch's own case labels. Parsing the source keeps
	// this honest without a second hand-maintained list.
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := regexp.MustCompile(`(?s)func buildGuardrail\(.*?\n\}\n`).FindString(string(src))
	if body == "" {
		t.Fatal("could not find buildGuardrail in main.go")
	}
	cases := regexp.MustCompile(`(?m)^\tcase "([a-z_]+)":`).FindAllStringSubmatch(body, -1)
	if len(cases) == 0 {
		t.Fatal("no case labels found in buildGuardrail")
	}

	for _, c := range cases {
		if !strings.Contains(section, "`"+c[1]+"`") {
			t.Errorf("buildGuardrail accepts type %q but the README's Guardrails section never names it", c[1])
		}
	}
}
