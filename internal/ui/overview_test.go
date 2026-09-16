package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// The pivot groups by the chosen dimension and ranks by the chosen metric.
func TestOverviewPivotsAndRanks(t *testing.T) {
	base := timeAt()
	mk := func(ts time.Time, provider, model string, cost float64, status int, epoch, domain string) store.Event {
		ev := store.Event{TraceID: "t", Provider: provider, Model: model, StatusCode: status,
			LatencyMs: 10, Ts: ts, ConfigEpoch: epoch, Domain: domain}
		ev.Usage.CostUSD = cost
		ev.Usage.InputTokens = 100
		return ev
	}
	h, _ := newSeededHandler(t,
		mk(base, "alpha", "m1", 1.0, 200, "e1", "code_generation"),
		mk(base.Add(time.Second), "alpha", "m1", 2.0, 200, "e1", "code_generation"),
		mk(base.Add(2*time.Second), "beta", "m2", 0.5, 500, "e2", ""),
		mk(base.Add(3*time.Second), "beta", "m3", 0.25, 200, "e2", ""),
	)

	// Grouped by provider, ranked by cost: alpha (3.0 across 2) before beta.
	body := serve(t, h, "GET", "/admin/ui/overview?dim=provider&metric=cost", false).Body.String()
	if !strings.Contains(body, "alpha") || !strings.Contains(body, "beta") {
		t.Fatalf("both providers missing from the pivot; body = %s", firstLine(body))
	}
	if strings.Index(body, "alpha") > strings.Index(body, "beta") {
		t.Error("rows are not ranked by cost: beta (0.75) appears before alpha (3.0)")
	}

	// Every row carries every metric, not just the ranking one.
	for _, want := range []string{"requests", "cost", "cost / request", "tokens (in+out)", "avg latency", "errors", "error rate"} {
		if !strings.Contains(body, want) {
			t.Errorf("the table does not carry a %q column; a row's numbers are incomplete", want)
		}
	}

	// Grouped by model: three groups, alpha/m1 and beta/m2 and beta/m3.
	byModel := serve(t, h, "GET", "/admin/ui/overview?dim=model&metric=requests", false).Body.String()
	if !strings.Contains(byModel, "alpha/m1") || !strings.Contains(byModel, "beta/m2") {
		t.Errorf("the model dimension does not render provider/model pairs; body = %s", firstLine(byModel))
	}

	// A NULL axis gets a label, not a blank cell.
	byDomain := serve(t, h, "GET", "/admin/ui/overview?dim=domain&metric=requests", false).Body.String()
	if !strings.Contains(byDomain, "(unclassified)") {
		t.Error("an empty domain renders as blank rather than a labelled group")
	}
	if !strings.Contains(byDomain, "code_generation") {
		t.Error("a filled domain is missing from the pivot")
	}
}

// Rankings depend on the metric, which is what makes the metric selector
// meaningful: the same rows, ordered differently.
func TestOverviewRankingFollowsMetric(t *testing.T) {
	base := timeAt()
	// alpha: 1 request, expensive. beta: 4 requests, cheap. So cost ranks alpha
	// first and requests ranks beta first.
	events := []store.Event{}
	mk := func(provider string, cost float64, latency int64) store.Event {
		ev := store.Event{TraceID: "t", Provider: provider, Model: "m", StatusCode: 200,
			Ts: base.Add(time.Duration(len(events)) * time.Second), LatencyMs: latency}
		ev.Usage.CostUSD = cost
		return ev
	}
	events = append(events, mk("alpha", 5.0, 10))
	for i := 0; i < 4; i++ {
		events = append(events, mk("beta", 0.1, 900))
	}
	h, _ := newSeededHandler(t, events...)

	byCost := serve(t, h, "GET", "/admin/ui/overview?dim=provider&metric=cost", false).Body.String()
	if strings.Index(byCost, "alpha") > strings.Index(byCost, "beta") {
		t.Error("ranked by cost, alpha (5.0) should lead beta (0.4)")
	}
	byRequests := serve(t, h, "GET", "/admin/ui/overview?dim=provider&metric=requests", false).Body.String()
	if strings.Index(byRequests, "beta") > strings.Index(byRequests, "alpha") {
		t.Error("ranked by requests, beta (4) should lead alpha (1)")
	}
	byLatency := serve(t, h, "GET", "/admin/ui/overview?dim=provider&metric=latency", false).Body.String()
	if strings.Index(byLatency, "beta") > strings.Index(byLatency, "alpha") {
		t.Error("ranked by latency, beta (900ms) should lead alpha (10ms)")
	}
}

// A bad dimension or metric is a 400 naming the parameter, never a silent
// fallback: grouping by something other than what was asked answers a question
// nobody put.
func TestOverviewRejectsUnknownAxes(t *testing.T) {
	h, _ := newSeededHandler(t, store.Event{TraceID: "t", Provider: "p", Model: "m",
		StatusCode: 200, LatencyMs: 1})

	for _, tc := range []struct{ q, want string }{
		{"?dim=nonsense", "dim"},
		{"?metric=nonsense", "metric"},
		{"?since=7d", "since"},
	} {
		rec := serve(t, h, "GET", "/admin/ui/overview"+tc.q, false)
		if rec.Code != 400 {
			t.Errorf("%s = %d, want 400", tc.q, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: the 400 does not name %q", tc.q, tc.want)
		}
	}
	// The valid axes are offered in the message, so the failure is fixable.
	rec := serve(t, h, "GET", "/admin/ui/overview?dim=nonsense", false)
	if !strings.Contains(rec.Body.String(), "provider") {
		t.Error("the 400 does not say what a valid dimension looks like")
	}
}

// A dimension with only one group is explained, not presented as a finding.
func TestOverviewExplainsSingleValuedDimension(t *testing.T) {
	base := timeAt()
	ev := store.Event{TraceID: "t", Provider: "p", Model: "m", StatusCode: 200, LatencyMs: 1, Ts: base}
	h, _ := newSeededHandler(t, ev)

	body := serve(t, h, "GET", "/admin/ui/overview?dim=domain&metric=requests", false).Body.String()
	if !strings.Contains(body, "one group") {
		t.Errorf("a single-group pivot is not explained; body = %s", firstLine(body))
	}
	// And the other single-valued axes are named, so the operator knows where to
	// look rather than concluding the page is broken.
	if !strings.Contains(body, "Single-valued in this window") {
		t.Error("the page does not name the other single-valued axes")
	}
	// The explanation says *why*, which is the part that would otherwise cost an
	// hour of confusion.
	if !strings.Contains(body, "classifiers") {
		t.Error("the explanation does not say where the axis comes from")
	}
}

// The epoch dimension shows cost per request, because epochs differ in how long
// they were live and raw spend across them misleads.
func TestOverviewEpochShowsPerRequest(t *testing.T) {
	base := timeAt()
	mk := func(ts time.Time, epoch string) store.Event {
		return store.Event{TraceID: "t", Provider: "p", Model: "m", StatusCode: 200,
			LatencyMs: 1, Ts: ts, ConfigEpoch: epoch}
	}
	h, _ := newSeededHandler(t, mk(base, "e1"), mk(base, "e1"), mk(base, "e2"))

	body := serve(t, h, "GET", "/admin/ui/overview?dim=epoch&metric=cost", false).Body.String()
	if !strings.Contains(body, "cost / request") {
		t.Error("the pivot does not show cost per request")
	}
	if !strings.Contains(body, "e1") || !strings.Contains(body, "e2") {
		t.Errorf("both epochs are missing; body = %s", firstLine(body))
	}
	// An epoch column that is NULL/empty is labelled rather than blank.
	ev := mk(base, "")
	h2, _ := newSeededHandler(t, ev)
	none := serve(t, h2, "GET", "/admin/ui/overview?dim=epoch&metric=cost", false).Body.String()
	if !strings.Contains(none, "(none)") {
		t.Error("a missing config epoch renders as blank rather than a labelled group")
	}
}

// Every table header must have a matching cell in every row, and every row must
// have the same number of cells. A conditional header with no cell (or vice
// versa) renders a table that looks plausible and is lying about its columns.
func TestPivotHeaderAndCellCountsMatch(t *testing.T) {
	base := timeAt()
	mk := func(ts time.Time, epoch, provider string) store.Event {
		return store.Event{TraceID: "t", Provider: provider, Model: "m", StatusCode: 200,
			LatencyMs: 1, Ts: ts, ConfigEpoch: epoch}
	}
	h, _ := newSeededHandler(t,
		mk(base, "e1", "alpha"), mk(base, "e1", "beta"), mk(base, "e2", "alpha"))

	for _, d := range []string{"provider", "model", "epoch", "domain", "status"} {
		for _, m := range []string{"requests", "cost", "error_rate"} {
			body := serve(t, h, "GET", "/admin/ui/overview?dim="+d+"&metric="+m, false).Body.String()
			// "<th " with the space, so the opening <thead> tag is not counted
			// as a column — a distinction that cost a debug cycle.
			th := len(regexp.MustCompile(`<th[ >]`).FindAllString(body, -1))
			td := len(regexp.MustCompile(`<td class="pivotkey">`).FindAllString(body, -1))
			if td == 0 {
				continue
			}
			// Every row must have the same cell count as the header's column
			// count. A mismatch is a column that exists in one and not the other.
			cells := len(regexp.MustCompile(`<td[ >]`).FindAllString(body, -1))
			if cells%td != 0 {
				t.Errorf("dim=%s metric=%s: %d cells across %d rows is not a whole number of columns",
					d, m, cells, td)
			}
			if cells/td != th {
				t.Errorf("dim=%s metric=%s: %d columns of header but %d columns of cells",
					d, m, th, cells/td)
			}
		}
	}
}

// The page is reachable and is HTML, with the fragments behaving like the rest.
func TestOverviewPageAndFragment(t *testing.T) {
	h, _ := newSeededHandler(t, store.Event{TraceID: "t", Provider: "p", Model: "m",
		StatusCode: 200, LatencyMs: 1})
	full := serve(t, h, "GET", "/admin/ui/overview", false).Body.String()
	if !strings.Contains(full, "<!doctype html>") {
		t.Error("the overview page is not a full document")
	}
	if !strings.Contains(full, `href="/admin/ui/overview"`) {
		t.Error("the nav does not link to the overview")
	}
	frag := serve(t, h, "GET", "/admin/ui/overview", true).Body.String()
	if strings.Contains(frag, "<!doctype") || !strings.Contains(frag, `id="pivot-table"`) {
		t.Errorf("the fragment form is wrong; got %s", firstLine(frag))
	}
}
