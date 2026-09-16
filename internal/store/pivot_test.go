package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cnf/arbiter/pkg/types"
)

// usageOf builds a usage record with the three fields the pivot aggregates.
func usageOf(in, out int64, cost float64) types.Usage {
	return types.Usage{InputTokens: int(in), OutputTokens: int(out), CostUSD: cost}
}

// everyDimensionAndMetricExecutes is the check that catches a typo'd SQL
// expression: a bad column name in one of the dimension or metric maps is a
// query error, and without this it would only surface when an operator happened
// to pick that axis.
func TestEveryDimensionAndMetricExecutes(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	withAxis := func(ts time.Time) Event {
		ev := event(ts, "alpha", "m1")
		ev.ConfigEpoch = "e1"
		ev.Domain = "code_generation"
		ev.Effort = "hard"
		ev.AliasUsed = "auto"
		return ev
	}
	keyless := withAxis(base.Add(time.Second))
	keyless.Provider = "beta"
	keyless.StatusCode = 500
	r := openSeeded(t, withAxis(base), keyless)

	w := Window{Since: base.Add(-time.Hour)}
	for _, d := range Dimensions() {
		for _, m := range Metrics() {
			rows, err := r.PivotTotals(context.Background(), w, d, m, 10)
			if err != nil {
				t.Errorf("dim=%s metric=%s: %v", d, m, err)
				continue
			}
			if len(rows) == 0 {
				t.Errorf("dim=%s metric=%s returned no rows for a non-empty window", d, m)
			}
			// Every row carries every metric, so no column is a zero by accident
			// of the projection being wrong.
			for _, row := range rows {
				if row.Requests == 0 {
					t.Errorf("dim=%s metric=%s produced a row with 0 requests", d, m)
				}
				if row.Providers == 0 {
					t.Errorf("dim=%s metric=%s produced a row with 0 models", d, m)
				}
			}
		}
	}
}

// An unknown dimension or metric is a sentinel a caller can tell from a query
// failure, so a handler can answer 400 rather than 500.
func TestUnknownAxesAreSentinels(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	r := openSeeded(t, event(base, "p", "m"))
	w := Window{Since: base.Add(-time.Hour)}

	if _, err := r.PivotTotals(context.Background(), w, "nonsense", MetricCost, 10); !errors.Is(err, ErrUnknownDimension) {
		t.Errorf("unknown dimension error = %v, want ErrUnknownDimension", err)
	}
	if _, err := r.PivotTotals(context.Background(), w, DimProvider, "nonsense", 10); !errors.Is(err, ErrUnknownMetric) {
		t.Errorf("unknown metric error = %v, want ErrUnknownMetric", err)
	}
	// And it is refused before touching the database, so a bad parameter cannot
	// become a malformed query.
	if _, err := r.PivotTotals(context.Background(), w, "provider'; DROP TABLE requests; --", MetricCost, 10); !errors.Is(err, ErrUnknownDimension) {
		t.Errorf("an injection-shaped dimension = %v, want ErrUnknownDimension", err)
	}
}

// The dimension expression is the group key, so a NULL axis is grouped under its
// label rather than collapsing with other NULLs into an unlabelled row — and
// that label is what a UI compares against.
func TestPivotLabelsNullGroups(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	plain := event(base, "p", "m")  // no epoch/domain/effort/alias
	filled := event(base, "p", "m") // explicitly filled
	filled.ConfigEpoch = "e1"
	filled.Domain = "code_generation"
	filled.Effort = "hard"
	filled.AliasUsed = "auto"
	r := openSeeded(t, plain, filled)

	w := Window{Since: base.Add(-time.Hour)}
	for _, tc := range []struct {
		d    Dimension
		null string
		full string
	}{
		{DimEpoch, "(none)", "e1"},
		{DimDomain, "(unclassified)", "code_generation"},
		{DimEffort, "(unclassified)", "hard"},
		{DimAlias, "(literal)", "auto"},
	} {
		rows, err := r.PivotTotals(context.Background(), w, tc.d, MetricRequests, 10)
		if err != nil {
			t.Fatalf("dim=%s: %v", tc.d, err)
		}
		keys := map[string]int64{}
		for _, row := range rows {
			keys[row.Key] = row.Requests
			if row.Key == "" {
				t.Errorf("dim=%s produced a blank group key; a NULL group must be labelled", tc.d)
			}
		}
		if _, ok := keys[tc.null]; !ok {
			t.Errorf("dim=%s has no %q group; got %v", tc.d, tc.null, keys)
		}
		if _, ok := keys[tc.full]; !ok {
			t.Errorf("dim=%s has no %q group; got %v", tc.d, tc.full, keys)
		}
		if keys[tc.null] != 1 || keys[tc.full] != 1 {
			t.Errorf("dim=%s counts are %v, want one row each", tc.d, keys)
		}
	}
}

// Each metric aggregates the thing it names. A metric that shared another's
// expression would rank identically and quietly be wrong.
func TestPivotMetricsAggregateWhatTheyName(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	cheapFast := event(base, "alpha", "m1")
	cheapFast.Usage = usageOf(10, 5, 0.1)
	cheapFast.LatencyMs = 10

	dearSlow := event(base.Add(time.Second), "beta", "m2")
	dearSlow.Usage = usageOf(1000, 500, 9.0)
	dearSlow.LatencyMs = 900
	dearSlow.StatusCode = 500
	r := openSeeded(t, cheapFast, dearSlow)

	w := Window{Since: base.Add(-time.Hour)}
	byProvider := func(m Metric) map[string]PivotRow {
		rows, err := r.PivotTotals(context.Background(), w, DimProvider, m, 10)
		if err != nil {
			t.Fatalf("metric=%s: %v", m, err)
		}
		out := map[string]PivotRow{}
		for _, row := range rows {
			out[row.Key] = row
		}
		return out
	}

	alpha, beta := byProvider(MetricRequests)["alpha"], byProvider(MetricRequests)["beta"]
	if alpha.CostUSD != 0.1 || beta.CostUSD != 9.0 {
		t.Errorf("costs = %v/%v, want 0.1/9.0", alpha.CostUSD, beta.CostUSD)
	}
	if alpha.Tokens != 15 || beta.Tokens != 1500 {
		t.Errorf("tokens = %d/%d, want 15/1500", alpha.Tokens, beta.Tokens)
	}
	if alpha.LatencyMs != 10 || beta.LatencyMs != 900 {
		t.Errorf("latency = %d/%d, want 10/900", alpha.LatencyMs, beta.LatencyMs)
	}
	if alpha.Errors != 0 || beta.Errors != 1 {
		t.Errorf("errors = %d/%d, want 0/1", alpha.Errors, beta.Errors)
	}
	if alpha.ErrorRate != 0 || beta.ErrorRate != 1 {
		t.Errorf("error rates = %v/%v, want 0/1", alpha.ErrorRate, beta.ErrorRate)
	}
	// Cost per request is the figure that makes differently-sized groups
	// comparable, which is why it is carried rather than derived by a client.
	if alpha.AvgCost != 0.1 || beta.AvgCost != 9.0 {
		t.Errorf("avg cost = %v/%v, want 0.1/9.0", alpha.AvgCost, beta.AvgCost)
	}
}

// Ordering is per metric and descending, with the group key breaking ties so a
// ranking is stable rather than arbitrary for equal values.
func TestPivotOrderingIsPerMetricAndStable(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	var events []Event
	// Three providers, each with one request, at different costs.
	for i, p := range []string{"alpha", "beta", "gamma"} {
		ev := event(base.Add(time.Duration(i)*time.Second), p, "m")
		ev.Usage = usageOf(10, 5, float64(i+1))
		events = append(events, ev)
	}
	r := openSeeded(t, events...)
	w := Window{Since: base.Add(-time.Hour)}

	rows, err := r.PivotTotals(context.Background(), w, DimProvider, MetricCost, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gamma", "beta", "alpha"}
	for i, key := range want {
		if rows[i].Key != key {
			t.Fatalf("ranked by cost = %v, want %v", keysOf(rows), want)
		}
	}

	// Ties break by key ascending, so the order is deterministic.
	tie1 := event(base, "aaa", "m")
	tie1.Usage = usageOf(1, 1, 1)
	tie2 := event(base.Add(time.Second), "zzz", "m")
	tie2.Usage = usageOf(1, 1, 1)
	r2 := openSeeded(t, tie2, tie1)
	tied, err := r2.PivotTotals(context.Background(), w, DimProvider, MetricCost, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tied) != 2 || tied[0].Key != "aaa" {
		t.Errorf("tied rows = %v, want aaa before zzz (key ascending)", keysOf(tied))
	}
}

// The window is respected: a pivot answers about a range, not about everything.
func TestPivotRespectsWindow(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	old := event(base, "old", "m")
	recent := event(base.Add(2*time.Hour), "recent", "m")
	r := openSeeded(t, old, recent)

	rows, err := r.PivotTotals(context.Background(), Window{Since: base.Add(time.Hour)}, DimProvider, MetricRequests, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Key != "recent" {
		t.Errorf("windowed pivot = %v, want only the recent provider", keysOf(rows))
	}
}

// The limit is clamped, so a caller cannot ask for an unbounded result.
func TestPivotLimitIsClamped(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	var events []Event
	for i := 0; i < 5; i++ {
		events = append(events, event(base.Add(time.Duration(i)*time.Second), string(rune('a'+i)), "m"))
	}
	r := openSeeded(t, events...)
	w := Window{Since: base.Add(-time.Hour)}

	rows, err := r.PivotTotals(context.Background(), w, DimProvider, MetricRequests, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Errorf("limit 2 returned %d rows", len(rows))
	}
	all, err := r.PivotTotals(context.Background(), w, DimProvider, MetricRequests, MaxPivotLimit*10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Errorf("clamped limit returned %d rows, want all 5", len(all))
	}
}

// An empty window is an empty slice, not nil and not an error.
func TestPivotEmptyIsEmptySlice(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	r := openSeeded(t, event(base, "p", "m"))
	rows, err := r.PivotTotals(context.Background(), Window{Since: base.Add(time.Hour)}, DimProvider, MetricRequests, 10)
	if err != nil {
		t.Fatal(err)
	}
	if rows == nil {
		t.Error("an empty pivot returned nil; it must be an empty slice")
	}
	if len(rows) != 0 {
		t.Errorf("len = %d, want 0", len(rows))
	}
}

func keysOf(rows []PivotRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Key)
	}
	return out
}
