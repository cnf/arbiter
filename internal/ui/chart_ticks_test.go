package ui

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The chart's tick formatting is JavaScript, so it cannot be exercised by a Go
// test directly — and it has already been wrong twice in ways no Go test could
// see: an x-axis unit error that labelled the year 58678, and an axis.values
// return shape that drew every timestamp on top of itself. Both rendered fine as
// far as the server was concerned.
//
// So this runs the real chart.js in node and asserts the two contracts it has:
// the axis.values hook takes (u, splits) and returns one label string per split,
// and each label is a UTC clock time. Skipped where node is absent, since the
// repo's build/test environment is devenv and node is only there incidentally.
func TestChartTickFormatterIsUTCAndPerSplit(t *testing.T) {
	findNode := func() string {
		for _, name := range []string{"node", "nodejs"} {
			if p, err := exec.LookPath(name); err == nil {
				return p
			}
		}
		return ""
	}
	node := findNode()
	if node == "" {
		t.Skip("node not available; the chart's tick formatter is only testable with a JS runtime")
	}

	chartPath, err := filepath.Abs("static/chart.js")
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(chartPath)
	if err != nil {
		t.Fatal(err)
	}

	// chart.js is an IIFE that touches document/uPlot on load, so the formatter
	// functions are extracted by text rather than by executing the whole file.
	// That is a real limitation: this asserts the *functions* are correct, not
	// that chart.js wires them up. The wiring is covered by the live checks in
	// the commit that added this.
	formatter := extractFunc(string(src), "function utcTickLabel")
	if formatter == "" {
		t.Fatal("chart.js no longer defines utcTickLabel; this test needs updating")
	}
	valuesFn := extractFunc(string(src), "function utcValues")
	if valuesFn == "" {
		t.Fatal("chart.js no longer defines utcValues; this test needs updating")
	}

	script := `
` + extractFunc(string(src), "function pad2") + `
` + formatter + `
` + valuesFn + `

// A uPlot-shaped call: values(u, splits, axisIdx, foundSpace, foundIncr).
var splits = [1789560000, 1789563600, 1789567200];
var labels = utcValues("1h")({}, splits);
if (!Array.isArray(labels)) { console.error("not an array"); process.exit(1); }
if (labels.length !== splits.length) {
  console.error("labels " + labels.length + " != splits " + splits.length);
  process.exit(1);
}
// A label must be a string, not an array: returning pairs here is exactly the
// bug that drew every timestamp on top of itself.
for (var i = 0; i < labels.length; i++) {
  if (typeof labels[i] !== "string") {
    console.error("label " + i + " is " + Object.prototype.toString.call(labels[i]));
    process.exit(1);
  }
}
console.log(JSON.stringify(labels));
`
	cmd := exec.Command(node, "-e", script)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("running the formatter in node: %v\n%s", err, errb.String())
	}

	got := strings.TrimSpace(out.String())
	// 1789560000 = 2026-09-16 12:00 UTC, and hourly after that. Local time here
	// is CEST, so a formatter using local getters would say 14:00 — the whole
	// point of asserting the exact strings.
	want := `["12:00","13:00","14:00"]`
	if got != want {
		t.Errorf("utcTickLabel produced %s, want %s\n(the labels must be UTC, not the browser's zone)", got, want)
	}
}

// extractFunc returns the source of one top-level function declaration, from its
// `function name(` line to the matching closing brace at the same indent.
func extractFunc(src, decl string) string {
	i := strings.Index(src, decl)
	if i < 0 {
		return ""
	}
	// Find the end by brace balance, which is enough for these functions.
	start := i
	depth := 0
	seenBrace := false
	for j := i; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
			seenBrace = true
		case '}':
			depth--
			if seenBrace && depth == 0 {
				return src[start : j+1]
			}
		}
	}
	return ""
}
