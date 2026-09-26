package main

import "testing"

// buildVersion reads runtime/debug.ReadBuildInfo, which only carries VCS
// settings when the test binary itself was built from within a git
// checkout via `go build`/`go test` in module mode (true for this repo's
// devenv, but not guaranteed for every CI/build environment). So this test
// asserts the function never panics and degrades to zero values instead of
// asserting specific non-empty content — the real "does it actually pick up
// vcs.revision" check is the manual smoke test recorded in PICKUP.md, since
// there's no clean way to fake build-time VCS stamps from inside a test.
func TestBuildVersionDoesNotPanic(t *testing.T) {
	revision, modified, buildTime := buildVersion()
	t.Logf("revision=%q modified=%v buildTime=%v", revision, modified, buildTime)
	if len(revision) > 12 {
		t.Errorf("revision %q longer than the 12-char cap", revision)
	}
}
