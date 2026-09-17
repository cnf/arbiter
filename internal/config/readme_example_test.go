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
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("read README: %v", err)
	}

	// The example is the first ```yaml block after "## Configuration".
	re := regexp.MustCompile("(?s)## Configuration.*?```yaml\n(.*?)```")
	m := re.FindSubmatch(readme)
	if m == nil {
		t.Fatal("could not find the ```yaml example under '## Configuration' in README.md")
	}
	example := string(m[1])

	// Placeholders like ${ANTHROPIC_API_KEY} must resolve to something, and the
	// example must not depend on a real secret to load.
	for _, v := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "LITELLM_URL", "LITELLM_API_KEY"} {
		t.Setenv(v, "test-"+strings.ToLower(v))
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "arbiter.yaml")
	if err := os.WriteFile(path, []byte(example), 0o600); err != nil {
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
