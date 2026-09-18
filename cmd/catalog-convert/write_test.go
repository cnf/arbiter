package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Atomic catalog writes. This file is meant to be regenerated on a schedule by a
// cron while the config that reads it stays live, so a half-written catalog is a
// real failure mode rather than a theoretical one: a plain truncate-then-write
// leaves a window where a reader sees a partial file.

// TestWriteCatalogFileReplacesContents proves the write lands and the old
// contents are gone.
func TestWriteCatalogFileReplacesContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.yaml")
	if err := os.WriteFile(path, []byte("old contents"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := writeCatalogFile(path, []byte("new contents")); err != nil {
		t.Fatalf("writeCatalogFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "new contents" {
		t.Errorf("contents = %q, want %q", got, "new contents")
	}
}

// TestWriteCatalogFileLeavesNoDebris is the property that makes the rename
// approach safe to run repeatedly: the temp file must not survive, or a nightly
// cron accumulates one file per run.
func TestWriteCatalogFileLeavesNoDebris(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.yaml")

	for i := 0; i < 3; i++ {
		if err := writeCatalogFile(path, []byte("contents")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "catalog.yaml" {
		t.Errorf("directory contains %v, want only catalog.yaml — temp files must not survive", names)
	}
}

// TestWriteCatalogFileCreatesWithReadablePerms pins the permissions, since the
// rename path bypasses os.WriteFile's own mode handling.
func TestWriteCatalogFileCreatesWithReadablePerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.yaml")
	if err := writeCatalogFile(path, []byte("x")); err != nil {
		t.Fatalf("writeCatalogFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("mode = %o, want 644", got)
	}
}

// TestWriteCatalogFileFailsOnMissingDir proves a bad -out is an error, not a
// silent no-op that a cron would treat as success.
func TestWriteCatalogFileFailsOnMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent", "catalog.yaml")
	if err := writeCatalogFile(path, []byte("x")); err == nil {
		t.Error("expected an error writing into a nonexistent directory")
	}
}

// TestHeaderNamesItsSource guards the honesty of the banner: the tool reads
// whatever price list it is handed, so the header must name that file rather
// than a hardcoded one that would become a lie once a second source exists.
func TestHeaderNamesItsSource(t *testing.T) {
	got := header("models.dev-catalog.json", 12, 3)
	if !strings.Contains(got, "models.dev-catalog.json") {
		t.Errorf("header does not name its source:\n%s", got)
	}
	if !strings.Contains(got, "12 rows") || !strings.Contains(got, "3 skipped") {
		t.Errorf("header does not report counts:\n%s", got)
	}
	// Costs are per million tokens from both known sources, but the unit claim
	// must not name one upstream's conversion specifically.
	if strings.Contains(got, "litellm's per-token") {
		t.Errorf("header still claims a litellm-specific unit conversion:\n%s", got)
	}
}
