package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestReadmeExampleConfigLoads keeps the README's configuration example honest.
// The example is the first thing a reader copies, and config loading is strict
// (unknown fields are an error), so a stale field name in the docs produces a
// failure that looks like a code bug. This test extracts the example straight
// from README.md and runs it through the real loader.
func TestReadmeExampleConfigLoads(t *testing.T) {
	readme := readReadme(t)

	// The example is the first ```yaml block after "## Configuration".
	re := regexp.MustCompile("(?s)## Configuration.*?```yaml\n(.*?)```")
	m := re.FindSubmatch(readme)
	if m == nil {
		t.Fatal("could not find the ```yaml example under '## Configuration' in README.md")
	}
	example := string(m[1])

	loadReadmeFragment(t, example)
}

// TestReadmeGuardrailExamplesLoad checks the guardrail snippets in the Guardrails
// and Prompt rewriting sections against the config schema.
//
// These are separate from the main example because they are *fragments*: they
// show only the `guardrails:` block, which is what a reader adds to an existing
// config. Each is grafted onto the main example (replacing its empty guardrails
// block) and loaded, so a documented config *key* the loader would reject fails
// here rather than in a reader's config.
//
// What this does NOT check: the `type` strings. Guardrail types are resolved in
// cmd/arbiter's buildGuardrail, not in config.Validate, so a doc block naming a
// type that does not exist still passes here — which is exactly how the first
// version of the Guardrails section came to document `prompt_replace`, a name
// that never existed. The type table in the docs is checked by eye and by the
// guardrail package's own tests; do not read this test as covering it.
func TestReadmeGuardrailExamplesLoad(t *testing.T) {
	readme := readReadme(t)

	base := regexp.MustCompile("(?s)## Configuration.*?```yaml\n(.*?)```").FindSubmatch(readme)
	if base == nil {
		t.Fatal("could not find the base example under '## Configuration'")
	}
	const emptyGuardrails = "guardrails:\n  pre: []                              # system_prompt, rate_limit, prompt_rewrite\n  post: []"
	if !strings.Contains(string(base[1]), emptyGuardrails) {
		t.Fatalf("the base example's empty guardrails block changed shape; update this test.\n" +
			"Expected to find:\n" + emptyGuardrails)
	}

	for _, tc := range []struct {
		name    string
		section string // heading the fragment lives under
	}{
		{"guardrails section", "## Guardrails"},
		{"prompt rewriting section", "## Prompt rewriting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			re := regexp.MustCompile("(?s)" + regexp.QuoteMeta(tc.section) + `.*?` + "```yaml\n(.*?)```")
			m := re.FindSubmatch(readme)
			if m == nil {
				t.Fatalf("no ```yaml block found under %q", tc.section)
			}
			grafted := strings.Replace(string(base[1]), emptyGuardrails, strings.TrimRight(string(m[1]), "\n"), 1)
			loadReadmeFragment(t, grafted)
		})
	}
}

func readReadme(t *testing.T) []byte {
	t.Helper()
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	return readme
}

// loadReadmeFragment writes a config and runs it through the real loader and
// validator. Placeholders like ${ANTHROPIC_API_KEY} must resolve to something,
// and no fragment may depend on a real secret to load.
func loadReadmeFragment(t *testing.T, config string) {
	t.Helper()
	for _, v := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "LITELLM_URL", "LITELLM_API_KEY"} {
		t.Setenv(v, "test-"+strings.ToLower(v))
	}

	path := filepath.Join(t.TempDir(), "arbiter.yaml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("the README's example config does not load (a reader copying it gets an error):\n%v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the README's example config fails validation:\n%v", err)
	}
}
