package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cnf/arbiter/internal/config"
)

// The guardrail `type` strings are documented in docs/guardrails.md's type
// table, and those strings are resolved here in buildGuardrail rather than in
// config.Validate — so nothing else catches a doc that names a type which does
// not exist. That is not hypothetical: the first version of the Guardrails
// section documented `prompt_replace`, a name that was never implemented, and
// every test passed.
//
// This test reads the type table straight out of the docs and asks
// buildGuardrail to build each one, so the docs and the switch cannot drift.
func TestGuardrailDocTypesAreBuildable(t *testing.T) {
	table := guardrailTypeTable(t)

	// Skip the header row and its `|---|---|` separator: the header's first
	// column is the literal word `type`, which is not a type name.
	row := regexp.MustCompile("(?m)^\\|\\s*`([a-z_]+)`\\s*\\|")
	matches := row.FindAllStringSubmatch(table, -1)
	filtered := matches[:0]
	for _, m := range matches {
		if m[1] != "type" {
			filtered = append(filtered, m)
		}
	}
	matches = filtered
	if len(matches) == 0 {
		t.Fatal("no `type` table rows found in docs/guardrails.md — the table moved or changed shape")
	}

	// Minimal config per type, so a build failure means the *type* is wrong
	// rather than a missing required key. prompt_rewrite's match is required.
	cfgFor := map[string]map[string]interface{}{
		"system_prompt":  {"prompt": "test"},
		"rate_limit":     {"per_minute": 1},
		"prompt_rewrite": {"match": "test", "mode": "prefix", "action": "strip"},
		"unpin":          {"match": "#reclassify"},
	}

	for _, m := range matches {
		typeName := m[1]
		t.Run(typeName, func(t *testing.T) {
			cfg, ok := cfgFor[typeName]
			if !ok {
				t.Fatalf("docs/guardrails.md documents guardrail type %q but this test has no sample config for it; "+
					"if the type is real, add it here — if it is not, the docs are wrong", typeName)
			}
			gc := config.GuardrailConfig{Name: "doc-" + typeName, Type: typeName, Config: cfg}
			if _, err := buildGuardrail(gc, nil); err != nil {
				t.Errorf("docs/guardrails.md documents guardrail type %q but it does not build: %v", typeName, err)
			}
		})
	}
}

// TestGuardrailDocTypesCoverTheSwitch is the other direction: every type the
// switch accepts should be documented, so a new guardrail cannot ship without a
// line in the table.
func TestGuardrailDocTypesCoverTheSwitch(t *testing.T) {
	table := guardrailTypeTable(t)

	// The type list is the switch's own case labels. Parsing the source keeps
	// this honest without a second hand-maintained list. Scan the whole
	// package rather than one file, so moving buildGuardrail between files
	// does not break this test on a rename that changed no behaviour.
	var src strings.Builder
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package sources: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		src.Write(b)
		src.WriteString("\n")
	}
	body := regexp.MustCompile(`(?s)func buildGuardrail\(.*?\n\}\n`).FindString(src.String())
	if body == "" {
		t.Fatal("could not find buildGuardrail in the package sources")
	}
	cases := regexp.MustCompile(`(?m)^\tcase "([a-z_]+)":`).FindAllStringSubmatch(body, -1)
	if len(cases) == 0 {
		t.Fatal("no case labels found in buildGuardrail")
	}

	for _, c := range cases {
		if !strings.Contains(table, "`"+c[1]+"`") {
			t.Errorf("buildGuardrail accepts type %q but docs/guardrails.md's type table never names it", c[1])
		}
	}
}

// guardrailTypeTable returns just the guardrail `type` table out of
// docs/guardrails.md.
//
// It is selected by its own header row rather than by "everything under the
// Guardrails heading". That distinction is not cosmetic: the document also
// carries `mode` and `action` tables, whose rows have exactly the same
// `| `name` | ... |` shape, and a section-wide scrape harvests `regex`,
// `strip`, `replace` and `block` as if they were guardrail types — which
// fails the build with a list of "types" that are not types. Slice from the
// header row to the next blank line and the question does not arise.
func guardrailTypeTable(t *testing.T) string {
	t.Helper()
	doc, err := os.ReadFile("../../docs/guardrails.md")
	if err != nil {
		t.Fatalf("read docs/guardrails.md: %v", err)
	}
	text := string(doc)

	const header = "| `type` |"
	start := strings.Index(text, header)
	if start < 0 {
		t.Fatalf("could not find the guardrail type table (header %q) in docs/guardrails.md", header)
	}
	end := strings.Index(text[start:], "\n\n")
	if end < 0 {
		t.Fatal("guardrail type table in docs/guardrails.md is not terminated by a blank line")
	}
	return text[start : start+end]
}
