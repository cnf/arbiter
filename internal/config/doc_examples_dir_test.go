package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDocExamplesLoad keeps every docs/examples/*.yaml file honest. Unlike
// the fragments in doc_example_test.go (fenced ```yaml blocks embedded in a
// docs/*.md prose section, grafted onto a base config), these are complete,
// standalone configs committed as their own files — each demonstrates one
// feature in isolation, so a reader can copy the whole file rather than
// assemble one from prose fragments. This test loads every one of them
// through the real loader and validator, the same guarantee
// TestDocExampleConfigLoads gives the base example in docs/configuration.md.
func TestDocExamplesLoad(t *testing.T) {
	const dir = "../../docs/examples"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	found := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		found++
		t.Run(e.Name(), func(t *testing.T) {
			for _, v := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "LITELLM_URL", "LITELLM_API_KEY", "OPENROUTER_API_KEY"} {
				t.Setenv(v, "test-"+v)
			}
			path := filepath.Join(dir, e.Name())
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("%s does not load (a reader copying it gets an error):\n%v", path, err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("%s fails validation:\n%v", path, err)
			}
		})
	}
	if found == 0 {
		t.Fatal("no docs/examples/*.yaml files found — did the directory move?")
	}
}
