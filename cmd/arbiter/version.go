package main

import (
	"runtime/debug"
	"time"
)

// buildVersion reads the VCS stamp Go's toolchain embeds automatically (as
// of Go 1.18, for a `go build` run inside a git checkout — no ldflags, no
// build step of our own) so a running binary can say which commit it is, not
// just that it started. revision is truncated to a short hash: full 40-char
// SHAs are precise but unreadable in a log line, and `git log --oneline`
// already trained the eye on 7-12 characters elsewhere in this repo's own
// tooling. modified is true when the tree had uncommitted changes at build
// time — worth surfacing, since "which commit" is a different question from
// "was it exactly that commit's code". Not read at package init: a `go test`
// binary and other non-`go build` invocations may not carry VCS settings, so
// this fails soft (empty fields) rather than panicking main before it can
// even load the config.
func buildVersion() (revision string, modified bool, buildTime time.Time) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false, time.Time{}
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
			if len(revision) > 12 {
				revision = revision[:12]
			}
		case "vcs.modified":
			modified = s.Value == "true"
		case "vcs.time":
			buildTime, _ = time.Parse(time.RFC3339, s.Value)
		}
	}
	return revision, modified, buildTime
}
