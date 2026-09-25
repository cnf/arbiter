package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// The pivot groups by the chosen dimension and ranks by the chosen metric.
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

