package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDocExampleConfigLoads keeps docs/configuration.md's base example
// honest. It is the first thing a reader copies, and config loading is
// strict (unknown fields are an error), so a stale field name in the docs
// produces a failure that looks like a code bug. This test extracts the
// example straight from the doc and runs it through the real loader.
func TestDocExampleConfigLoads(t *testing.T) {
	configDoc := readDoc(t, "configuration.md")

	// The example is the first ```yaml block after the "Configuration" title.
	re := regexp.MustCompile("(?s)#+ Configuration.*?```yaml\n(.*?)```")
	m := re.FindSubmatch(configDoc)
	if m == nil {
		t.Fatal("could not find the ```yaml example under 'Configuration' in docs/configuration.md")
	}
	example := string(m[1])

	loadReadmeFragment(t, example)
}

// TestDocGuardrailExamplesLoad checks the guardrail snippet in docs/guardrails.md's
// top-level "Guardrails" section (the pre/post shape every type sits inside)
// against the config schema.
//
// The "Prompt rewriting" and "Unpinning a conversation" sections no longer
// embed their own fenced configs — they link to
// docs/examples/guardrail-prompt-rewrite.yaml and
// docs/examples/guardrail-unpin.yaml, loaded by TestDocExamplesLoad (see
// doc_examples_dir_test.go) alongside every other docs/examples/*.yaml file.
//
// This is a *fragment*: it shows only the `guardrails:` block, which is what a
// reader adds to an existing config. It is grafted onto the main example
// (replacing its empty guardrails block) and loaded, so a documented config
// *key* the loader would reject fails here rather than in a reader's config.
//
// What this does NOT check: the `type` strings. Guardrail types are resolved in
// cmd/arbiter's buildGuardrail, not in config.Validate, so a doc block naming a
// type that does not exist still passes here — which is exactly how the first
// version of the Guardrails section came to document `prompt_replace`, a name
// that never existed. The type table is checked by
// cmd/arbiter/guardrails_doc_test.go; do not read this test as covering it.
func TestDocGuardrailExamplesLoad(t *testing.T) {
	configDoc := readDoc(t, "configuration.md")
	guardrails := readDoc(t, "guardrails.md")

	base := regexp.MustCompile("(?s)#+ Configuration.*?```yaml\n(.*?)```").FindSubmatch(configDoc)
	if base == nil {
		t.Fatal("could not find the base example under 'Configuration' in docs/configuration.md")
	}
	const emptyGuardrails = "guardrails:\n  pre: []                              # system_prompt, rate_limit, prompt_rewrite, unpin\n  post: []"
	if !strings.Contains(string(base[1]), emptyGuardrails) {
		t.Fatalf("the base example's empty guardrails block changed shape; update this test.\n" +
			"Expected to find:\n" + emptyGuardrails)
	}

	for _, tc := range []struct {
		name    string
		doc     []byte
		section string // heading the fragment lives under, level-agnostic
	}{
		{"guardrails section", guardrails, "#+ Guardrails"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Level-agnostic: the section is a top-level heading in this doc,
			// and pinning the number of '#' would make a heading-level edit
			// look like a missing section.
			re := regexp.MustCompile("(?s)" + tc.section + `.*?` + "```yaml\n(.*?)```")
			m := re.FindSubmatch(tc.doc)
			if m == nil {
				t.Fatalf("no ```yaml block found under %q", tc.section)
			}
			grafted := strings.Replace(string(base[1]), emptyGuardrails, strings.TrimRight(string(m[1]), "\n"), 1)
			loadReadmeFragment(t, grafted)
		})
	}
}

// The decisions classifier section in docs/routing.md no longer embeds its
// own fenced config: it links to docs/examples/classifier-decisions.yaml,
// which is a complete standalone config loaded by TestDocExamplesLoad (see
// doc_examples_dir_test.go) alongside every other docs/examples/*.yaml file.

// TestDocClassifierMatchExamplesLoad checks the `detect` snippet in
// docs/routing.md's "Structural capability detection" section against the
// config schema. The sibling "Matching a request's own text" section no
// longer embeds its own fenced config — it links to
// docs/examples/classifier-request-kind-match.yaml, loaded by TestDocExamplesLoad (see
// doc_examples_dir_test.go) alongside every other docs/examples/*.yaml file.
//
// Like the guardrail fragments, this shows only a `classifiers:` block, so it
// is grafted onto the base example (replacing its classifiers block). Without
// this the section could document a top-level classifier key the loader
// rejects — which is exactly how the Guardrails section once came to document
// a guardrail type that never existed.
//
// Same caveat as the guardrail fragments: each classifier's `config:` map is
// untyped, so an unknown key *inside* it (a typo in `keywords:` or
// `long_context_tokens:`) loads fine and is not caught here. Only top-level
// ClassifierConfig fields are schema-checked.
func TestDocClassifierMatchExamplesLoad(t *testing.T) {
	configDoc := readDoc(t, "configuration.md")
	routing := readDoc(t, "routing.md")

	base := regexp.MustCompile("(?s)#+ Configuration.*?```yaml\n(.*?)```").FindSubmatch(configDoc)
	if base == nil {
		t.Fatal("could not find the base example under 'Configuration' in docs/configuration.md")
	}
	// The base example's classifiers block, replaced wholesale by each fragment.
	//
	// Found by slicing rather than by regex: Go's regexp has no lookahead, and
	// the block must be bounded by the NEXT top-level key — bounding it by blank
	// lines would run past the end, since the block contains blank lines
	// internally and the keys after it hold the rest of a loadable config.
	baseText := string(base[1])
	start := strings.Index(baseText, "classifiers:\n")
	if start < 0 {
		t.Fatal("could not locate the base example's classifiers block; update this test")
	}
	rest := baseText[start+len("classifiers:\n"):]
	end := len(rest)
	lineRe := regexp.MustCompile(`(?m)^[a-zA-Z_]+:`)
	if loc := lineRe.FindStringIndex(rest); loc != nil {
		end = loc[0]
	}
	classifiersBlock := baseText[start : start+len("classifiers:\n")+end]

	for _, tc := range []struct {
		name    string
		section string // heading the fragment lives under, level-agnostic
	}{
		{"detect section", "#+ Structural capability detection"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			re := regexp.MustCompile("(?s)" + tc.section + `.*?` + "```yaml\n(.*?)```")
			m := re.FindSubmatch(routing)
			if m == nil {
				t.Fatalf("no ```yaml block found under %q in docs/routing.md", tc.section)
			}
			grafted := strings.Replace(string(base[1]), classifiersBlock, strings.TrimRight(string(m[1]), "\n")+"\n", 1)
			loadReadmeFragment(t, grafted)
		})
	}
}

// readDoc reads a file out of the repo's docs/ directory.
func readDoc(t *testing.T, name string) []byte {
	t.Helper()
	doc, err := os.ReadFile("../../docs/" + name)
	if err != nil {
		t.Fatalf("read docs/%s: %v", name, err)
	}
	return doc
}

// loadReadmeFragment writes a config and runs it through the real loader and
// validator. Placeholders like ${ANTHROPIC_API_KEY} must resolve to something,
// and no fragment may depend on a real secret to load.
//
// Named for its origin (this test file used to read only README.md); it now
// loads fragments sourced from docs/*.md just the same.
func loadReadmeFragment(t *testing.T, config string) {
	t.Helper()
	for _, v := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "LITELLM_URL", "LITELLM_API_KEY", "OPENROUTER_API_KEY"} {
		t.Setenv(v, "test-"+strings.ToLower(v))
	}

	path := filepath.Join(t.TempDir(), "arbiter.yaml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("the doc's example config does not load (a reader copying it gets an error):\n%v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the doc's example config fails validation:\n%v", err)
	}
}
