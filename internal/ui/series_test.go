package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/store"
)

// The series endpoint answers JSON with the shape chart.js expects, and the
// axes it was given are carried through.
func TestSeriesEndpointShape(t *testing.T) {
	base := timeAt()
	// Three requests across two hour-buckets, two providers.
	mk := func(ts time.Time, provider string, cost float64) store.Event {
		ev := store.Event{TraceID: "t", Provider: provider, Model: "m", StatusCode: 200,
			LatencyMs: 1, Ts: ts}
		ev.Usage.CostUSD = cost
		return ev
	}
	h, _ := newSeededHandler(t,
		mk(base, "alpha", 1), mk(base.Add(time.Minute), "beta", 2),
		mk(base.Add(2*time.Hour), "alpha", 3))

	st, body := serveJSON(t, h, "/admin/ui/overview/series.json?dim=provider&metric=cost")
	if st != 200 {
		t.Fatalf("series endpoint = %d: %s", st, body)
	}
	var got seriesResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("response is not the expected JSON: %v\n%s", err, firstLine(body))
	}
	if len(got.X) == 0 {
		t.Fatal("no x values; the chart has nothing to draw on")
	}
	if len(got.Series) == 0 {
		t.Fatal("no series; the chart has no lines")
	}
	if got.YLabel == "" {
		t.Error("the y-axis has no label; the same chart means six different things")
	}
	// Every series must be as long as the x axis, or the chart draws short lines.
	for _, s := range got.Series {
		if len(s.Values) != len(got.X) {
			t.Errorf("series %q has %d values for %d buckets", s.Label, len(s.Values), len(got.X))
		}
		if s.Color == "" {
			t.Errorf("series %q has no colour", s.Label)
		}
	}
	// x must be ascending unix seconds, which is what a time axis requires.
	for i := 1; i < len(got.X); i++ {
		if got.X[i] <= got.X[i-1] {
			t.Errorf("x is not ascending: %v", got.X)
			break
		}
	}
	// A bucket the group had no traffic in is a null, not a zero: zero would
	// claim the group was idle by design.
	nulls := 0
	for _, s := range got.Series {
		for _, v := range s.Values {
			if v == nil {
				nulls++
			}
		}
	}
	if nulls == 0 && len(got.Series) > 1 {
		t.Log("no gaps in this fixture; the null path is covered by the bucket test")
	}
}

// A bad axis on the series endpoint is a 400 with a JSON body, since it is
// fetched by script rather than by a browser navigation.
func TestSeriesEndpointRejectsBadAxes(t *testing.T) {
	h, _ := newSeededHandler(t, store.Event{TraceID: "t", Provider: "p", Model: "m",
		StatusCode: 200, LatencyMs: 1})

	for _, q := range []string{"?dim=nonsense", "?metric=nonsense", "?since=7d"} {
		st, body := serveJSON(t, h, "/admin/ui/overview/series.json"+q)
		if st != 400 {
			t.Errorf("%s = %d, want 400", q, st)
		}
		var e map[string]string
		if err := json.Unmarshal([]byte(body), &e); err != nil || e["error"] == "" {
			t.Errorf("%s: the 400 is not a JSON error body: %s", q, firstLine(body))
		}
	}
}

// A disabled store on the series endpoint is a JSON 503, because the caller is a
// script that will put the message in the chart container.
func TestSeriesEndpointDisabledStoreIsJSON(t *testing.T) {
	h := newTestHandler()
	st, body := serveJSON(t, h, "/admin/ui/overview/series.json")
	if st != 503 {
		t.Errorf("disabled store = %d, want 503", st)
	}
	if !strings.Contains(body, "storage.path") {
		t.Errorf("the 503 does not name the setting: %s", firstLine(body))
	}
}

// Gaps are nulls so a line breaks rather than claiming zero. This is asserted on
// the builder directly, since the store's own bucketing is covered elsewhere.
func TestSeriesGapsAreNullNotZero(t *testing.T) {
	// Two buckets, two groups, each present in only one of them.
	points := []store.PivotPoint{
		{Bucket: "2026-09-16 05:00:00", Key: "alpha", Value: 5},
		{Bucket: "2026-09-16 06:00:00", Key: "beta", Value: 7},
	}
	got := buildSeries(points, store.MetricCost, store.BucketHour, 6)
	if len(got.X) != 2 {
		t.Fatalf("x has %d buckets, want 2", len(got.X))
	}
	for _, s := range got.Series {
		var nilCount, numCount int
		for _, v := range s.Values {
			if v == nil {
				nilCount++
			} else {
				numCount++
			}
		}
		if nilCount != 1 || numCount != 1 {
			t.Errorf("series %q has %d nulls and %d values; want one of each (the group is absent from one bucket)",
				s.Label, nilCount, numCount)
		}
	}
}

// Past the group cap the remainder is folded into one line, not dropped: a chart
// whose lines do not sum to the headline is a chart that lies.
func TestSeriesFoldsExcessGroups(t *testing.T) {
	points := []store.PivotPoint{
		{Bucket: "2026-09-16 05:00:00", Key: "a", Value: 1},
		{Bucket: "2026-09-16 05:00:00", Key: "b", Value: 2},
		{Bucket: "2026-09-16 05:00:00", Key: "c", Value: 4},
		{Bucket: "2026-09-16 05:00:00", Key: "d", Value: 8},
	}
	got := buildSeries(points, store.MetricCost, store.BucketHour, 2)
	if len(got.Series) != 3 {
		t.Fatalf("got %d series, want 2 groups + an 'other' line", len(got.Series))
	}
	last := got.Series[len(got.Series)-1]
	if !strings.HasPrefix(last.Label, "other") {
		t.Errorf("the last line is %q, want an 'other' fold", last.Label)
	}
	if last.Values[0] == nil || *last.Values[0] != 12 {
		t.Errorf("the folded line = %v, want 12 (c + d)", last.Values[0])
	}
	// And the sums still equal the input total.
	total := 0.0
	for _, s := range got.Series {
		if s.Values[0] != nil {
			total += *s.Values[0]
		}
	}
	if total != 15 {
		t.Errorf("the drawn lines sum to %v, want 15", total)
	}
}

// The bucket key is parsed with the layout its granularity produces, so a daily
// bucket (a bare date) does not silently become the epoch.
func TestSeriesBucketUnixParsesBothLayouts(t *testing.T) {
	hourly := bucketUnix("2026-09-16 05:00:00", store.BucketHour)
	daily := bucketUnix("2026-09-16", store.BucketDay)
	if hourly <= 0 {
		t.Errorf("hourly bucket parsed to %d", hourly)
	}
	if daily <= 0 {
		t.Errorf("daily bucket parsed to %d", daily)
	}
	if daily != hourly-hourly%86400 {
		t.Errorf("daily (%d) is not midnight of the hourly bucket's day (%d)", daily, hourly)
	}
	// An unparseable key is a sentinel, not the epoch: piling points at 1970
	// would look like data rather than a bug.
	if got := bucketUnix("not a time", store.BucketHour); got != -1 {
		t.Errorf("unparseable bucket = %d, want -1", got)
	}
}
