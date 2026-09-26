package ui

import (
	"sort"
	"strings"
	"testing"

	"github.com/cnf/arbiter/internal/store"
)

// Tests for the sankey layout (#54). The geometry is computed in Go precisely so
// these can exist: the mockup's coordinates came from a throwaway script, and the
// layout is the one piece of real arithmetic on the page.

// A long tail of small routes must not produce colliding labels. This is the
// bug live data exposed and the seeded tests had missed: 18 model nodes in a
// 420px diagram put label baselines ~9px apart, with two lines of 12px type
// each, so the tail rendered as an illegible pile. The fix is two-part — a lower
// route cap, and per-node gating on whether a band has room for its text at all.
func TestSankeyLabelsNeverCollide(t *testing.T) {
	// One dominant route plus a long tail of tiny ones: the shape that breaks it.
	edges := []store.RoutingEdge{
		{Alias: "coding", Provider: "claude", Model: "big", Measures: store.Measures{Requests: 5000}},
	}
	for i := 0; i < 11; i++ {
		edges = append(edges, store.RoutingEdge{
			Alias:    "arbiter",
			Provider: "openrouter",
			Model:    string(rune('a' + i)),
			Measures: store.Measures{Requests: int64(3 + i)},
		})
	}

	s := buildSankey(edges)

	for _, side := range []string{"alias", "model"} {
		var ys []float64
		for _, n := range s.Nodes {
			if n.Side != side || !n.ShowLabel {
				continue
			}
			ys = append(ys, n.TextY)
			if n.ShowSub && n.Height < subMinBand {
				t.Errorf("%s node %q draws a sub-line in a %.1fpx band (min %.1f)",
					side, n.Label, n.Height, subMinBand)
			}
			if n.Height < labelMinBand {
				t.Errorf("%s node %q draws a label in a %.1fpx band (min %.1f)",
					side, n.Label, n.Height, labelMinBand)
			}
		}
		sort.Float64s(ys)
		for i := 1; i < len(ys); i++ {
			if gap := ys[i] - ys[i-1]; gap < labelMinBand {
				t.Errorf("%s column: label baselines %.1fpx apart, need at least %.1f — "+
					"this is the overlapping-label pile live data produced",
					side, gap, labelMinBand)
			}
		}
	}

	// Every node stays clickable and identifiable even without a drawn label:
	// the id and the pre-rendered sub-line (used as the hover title) are always
	// present, so a bar-only node is not anonymous.
	for _, n := range s.Nodes {
		if n.ID == "" {
			t.Error("a node has no id, so it cannot open a drawer")
		}
		if n.SubLine == "" {
			t.Errorf("node %q has no sub-line, so its hover title would be empty", n.Label)
		}
	}
}

// Ribbons must connect real endpoints and be closed curves: a node's bar height
// is the sum of its ribbons' widths at that end, which is what makes a sankey
// readable rather than decorative.
func TestSankeyRibbonsCoverTheirNodes(t *testing.T) {
	edges := []store.RoutingEdge{
		{Alias: "a", Provider: "p", Model: "m1", Measures: store.Measures{Requests: 100}},
		{Alias: "a", Provider: "p", Model: "m2", Measures: store.Measures{Requests: 50}},
		{Alias: "b", Provider: "p", Model: "m1", Measures: store.Measures{Requests: 25}},
	}
	s := buildSankey(edges)

	if len(s.Ribbons) != 3 {
		t.Fatalf("got %d ribbons, want one per route (3)", len(s.Ribbons))
	}
	for _, r := range s.Ribbons {
		if r.From == "" || r.To == "" {
			t.Errorf("ribbon %+v does not name both ends", r)
		}
		if !strings.HasPrefix(r.Path, "M") || !strings.HasSuffix(r.Path, "Z") {
			t.Errorf("ribbon path is not a closed curve: %q", r.Path)
		}
	}
	// Alias "a" carries 150 of 175 requests, so its bar must be the taller one.
	var aH, bH float64
	for _, n := range s.Nodes {
		switch n.ID {
		case "alias:a":
			aH = n.Height
		case "alias:b":
			bH = n.Height
		}
	}
	if aH <= bH {
		t.Errorf("alias a (150 req) has a %.1fpx bar, alias b (25 req) %.1fpx — "+
			"heights must follow request share", aH, bH)
	}
}

// An empty window produces an explicitly empty diagram rather than a box with
// nothing in it, which reads as broken rather than idle.
func TestSankeyEmptyIsMarkedEmpty(t *testing.T) {
	s := buildSankey(nil)
	if !s.Empty {
		t.Error("an edgeless flow is not marked empty")
	}
	if len(s.Nodes) != 0 || len(s.Ribbons) != 0 {
		t.Error("an empty flow drew something")
	}
	// A window whose routes all have zero requests is equally empty — a diagram
	// scaled by a zero total would divide by zero.
	s = buildSankey([]store.RoutingEdge{{Alias: "a", Provider: "p", Model: "m"}})
	if !s.Empty {
		t.Error("a zero-request flow is not marked empty")
	}
}

// The remainder bucket is pinned last in both columns regardless of size: it is
// a bucket, not a route, and floating it into the middle of the ranking would
// read as one.
func TestSankeyRemainderSortsLast(t *testing.T) {
	edges := []store.RoutingEdge{
		{Alias: "small", Provider: "p", Model: "m", Measures: store.Measures{Requests: 10}},
		// A remainder larger than the real route above it.
		{FoldedRoutes: 5, Measures: store.Measures{Requests: 900}},
	}
	s := buildSankey(edges)

	var lastAlias, lastModel string
	for _, n := range s.Nodes {
		if n.Side == "alias" {
			lastAlias = n.ID
		} else {
			lastModel = n.ID
		}
	}
	if lastAlias != "alias:__remainder__" {
		t.Errorf("last alias node is %q, want the remainder bucket", lastAlias)
	}
	if lastModel != "model:__remainder__" {
		t.Errorf("last model node is %q, want the remainder bucket", lastModel)
	}
}

// A route whose destination is the alias name and whose every call failed is
// misconfiguration debris present in the real store. It must be marked, and must
// not read as free.
func TestSankeyMarksAllErroredNodes(t *testing.T) {
	s := buildSankey([]store.RoutingEdge{
		{Alias: "coding", Provider: "claude", Model: "coding",
			Measures: store.Measures{Requests: 23, Errors: 23}},
	})
	var found bool
	for _, n := range s.Nodes {
		if n.ID == "alias:coding" {
			found = true
			if !n.Errored {
				t.Error("an all-errored node is not flagged")
			}
			if !strings.Contains(n.SubLine, "every request failed") {
				t.Errorf("all-errored sub-line = %q, want it to say so rather than show $0",
					n.SubLine)
			}
		}
	}
	if !found {
		t.Fatal("the all-errored route was dropped from the diagram")
	}
}

// An alias label is right-aligned, ending before its bar; a model label runs
// rightwards from its bar. Getting this backwards puts text through the bars.
func TestSankeyLabelSidesFaceOutward(t *testing.T) {
	s := buildSankey([]store.RoutingEdge{
		{Alias: "a", Provider: "p", Model: "m", Measures: store.Measures{Requests: 100}},
	})
	for _, n := range s.Nodes {
		switch n.Side {
		case "alias":
			if n.LabelX >= n.X {
				t.Errorf("alias label starts at %.1f, its bar at %.1f — it must end before the bar",
					n.LabelX, n.X)
			}
		case "model":
			if n.LabelX <= n.X+n.Width {
				t.Errorf("model label starts at %.1f, its bar ends at %.1f — it must start after the bar",
					n.LabelX, n.X+n.Width)
			}
		}
		// The click target must cover the label, not just the 18px bar.
		if n.HitWidth <= n.Width {
			t.Errorf("node %q click target (%.1f) is no wider than its bar (%.1f)",
				n.Label, n.HitWidth, n.Width)
		}
	}
}
