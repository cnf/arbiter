package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The bucket expressions are the sharpest edge in this package, and the failure
// is silent: a date function over the stored ts returns NULL for every row rather
// than erroring, so a bucket query can look fine and produce nothing. These tests
// therefore assert on *rows placed in known buckets*, seeded through the real
// writer — a hand-written INSERT would store a timestamp layout production never
// writes, which is precisely what this code is sensitive to.

// TestBucketsGroupKnownHours is the test that catches the layout landmine. Rows
// at three known hours must land in three buckets with the right starts.
func TestBucketsGroupKnownHours(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 29, 33, 123456789, time.UTC)
	r := openSeeded(t,
		event(base, "p", "m"),
		event(base.Add(31*time.Minute), "p", "m"), // 06:00
		event(base.Add(61*time.Minute), "p", "m"), // 06:30
	)

	w := Window{Since: base.Add(-time.Hour)}
	rows, err := r.db.Query(`
SELECT `+bucketSQL[BucketHour]+` AS b, COUNT(*)
FROM requests WHERE ts >= ? GROUP BY b ORDER BY b`, w.Since)
	if err != nil {
		t.Fatalf("bucket query: %v", err)
	}
	defer func() { _ = rows.Close() }()

	got := map[string]int{}
	for rows.Next() {
		var b string
		var n int
		if err := rows.Scan(&b, &n); err != nil {
			t.Fatal(err)
		}
		got[b] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	want := map[string]int{
		"2026-09-16 05:00:00": 1,
		"2026-09-16 06:00:00": 2,
	}
	if len(got) != len(want) {
		t.Fatalf("buckets = %v, want %v (a NULL bucket key means the ts layout is unparseable)", got, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("bucket %q has %d rows, want %d; got %v", k, got[k], n, got)
		}
	}
}

// A row stored with a nanosecond fraction and one stored without must land in the
// same bucket: the first 19 characters are fixed-width regardless of the
// fractional part, which is what makes prefix slicing exact.
func TestBucketsIgnoreFractionalSeconds(t *testing.T) {
	whole := time.Date(2026, 9, 16, 5, 29, 33, 0, time.UTC)
	fractional := time.Date(2026, 9, 16, 5, 29, 33, 987654321, time.UTC)
	r := openSeeded(t, event(whole, "p", "m"), event(fractional, "p", "m"))

	w := Window{Since: whole.Add(-time.Hour)}
	var buckets int
	if err := r.db.QueryRow(`
SELECT COUNT(DISTINCT `+bucketSQL[BucketMin10]+`)
FROM requests WHERE ts >= ?`, w.Since).Scan(&buckets); err != nil {
		t.Fatal(err)
	}
	if buckets != 1 {
		t.Errorf("two rows in the same second fell into %d buckets; want 1", buckets)
	}
	// And the two rows' stored text really does differ, so the test is not
	// passing because the fractions were dropped on write.
	var distinct int
	if err := r.db.QueryRow(`SELECT COUNT(DISTINCT ts) FROM requests`).Scan(&distinct); err != nil {
		t.Fatal(err)
	}
	if distinct != 2 {
		t.Errorf("the two rows store identical ts text (%d distinct); the fixture does not test fractions", distinct)
	}
}

// Every bucket expression produces a key that is parseable — by Go, which is what
// turns it into a chart's x value.
func TestBucketKeysAreParseable(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 29, 33, 0, time.UTC)
	r := openSeeded(t, event(base, "p", "m"))
	w := Window{Since: base.Add(-time.Hour)}

	for _, b := range Buckets() {
		var key string
		if err := r.db.QueryRow(`
SELECT `+bucketSQL[b]+` FROM requests WHERE ts >= ? LIMIT 1`, w.Since).Scan(&key); err != nil {
			t.Fatalf("bucket %s: %v", b, err)
		}
		layout := "2006-01-02 15:04:05"
		if b == BucketDay {
			layout = "2006-01-02"
		}
		if _, err := time.Parse(layout, key); err != nil {
			t.Errorf("bucket %s produced %q, which does not parse with %q: %v", b, key, layout, err)
		}
		// A bucket key starts at the bucket's boundary. Positions: 0-9 date,
		// 10 space, 11-12 hour, 13 colon, 14-15 minute, 16 colon, 17-18 second.
		if b == BucketMin10 {
			if key[17:] != "00" {
				t.Errorf("10-minute bucket %q does not start on the minute", key)
			}
			// The expression keeps the tens-of-minutes digit and zeroes the
			// units, so the minute is always a multiple of 10.
			mins := int(key[14]-'0')*10 + int(key[15]-'0')
			if mins%10 != 0 {
				t.Errorf("10-minute bucket %q does not start on a 10-minute boundary", key)
			}
		}
		if b == BucketHour {
			if key[14:] != "00:00" {
				t.Errorf("hourly bucket %q does not start on the hour", key)
			}
		}
	}
}

// The escape hatch the plan named is real: substr(ts,1,19) is a valid SQLite time
// string even though raw ts is not. Asserting it keeps a future change from
// removing the only route to a date function.
func TestSubstrTimestampIsValidForSqlite(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 29, 33, 123456789, time.UTC)
	r := openSeeded(t, event(base, "p", "m"))

	// Raw ts is NOT parseable — the landmine itself.
	var rawEpoch interface{}
	if err := r.db.QueryRow(`SELECT strftime('%s', ts) FROM requests LIMIT 1`).Scan(&rawEpoch); err != nil {
		t.Fatal(err)
	}
	if rawEpoch != nil {
		t.Errorf("strftime over raw ts returned %v; the column's layout changed and every bucket comment is now wrong", rawEpoch)
	}

	// The sliced form is.
	var epoch int64
	if err := r.db.QueryRow(`SELECT strftime('%s', substr(ts,1,19)) FROM requests LIMIT 1`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if epoch != base.Unix() {
		t.Errorf("strftime over substr(ts,1,19) = %d, want %d", epoch, base.Unix())
	}
}

// Bucket selection follows the window's width, so a fine window does not produce
// a thousand points and a coarse one does not produce a single point.
func TestBucketForFollowsWindowWidth(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		width time.Duration
		want  Bucket
	}{
		{time.Hour, BucketMin10},
		{6 * time.Hour, BucketMin10},
		{24 * time.Hour, BucketHour},
		{4 * 24 * time.Hour, BucketHour},
		{7 * 24 * time.Hour, BucketDay},
		{90 * 24 * time.Hour, BucketDay},
	}
	for _, tc := range cases {
		got := BucketFor(Window{Since: now.Add(-tc.width)}, now)
		if got != tc.want {
			t.Errorf("a %s window chose %s, want %s", tc.width, got, tc.want)
		}
	}
	// An unbounded window takes the coarsest bucket rather than the finest: a
	// zero Since means "all time", which would otherwise pick 10-minute.
	if got := BucketFor(Window{}, now); got != BucketDay {
		t.Errorf("an all-time window chose %s, want %s", got, BucketDay)
	}
}

// The series query groups by the same dimension expression the totals query does.
// If they disagreed, the chart's lines and the table's rows would describe
// different groups under the same names.
func TestSeriesAndTotalsAgreeOnGroupKeys(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	plain := event(base, "p", "m") // no epoch, no domain
	filled := event(base.Add(time.Minute), "p", "m")
	filled.ConfigEpoch = "e1"
	filled.Domain = "code_generation"
	r := openSeeded(t, plain, filled)

	w := Window{Since: base.Add(-time.Hour)}
	for _, d := range []Dimension{DimEpoch, DimDomain, DimProvider, DimModel} {
		totals, err := r.PivotTotals(context.Background(), w, d, MetricRequests, 20)
		if err != nil {
			t.Fatalf("totals dim=%s: %v", d, err)
		}
		totalKeys := map[string]bool{}
		for _, row := range totals {
			totalKeys[row.Key] = true
		}

		points, err := r.PivotSeries(context.Background(), w, d, MetricRequests, BucketHour, 20)
		if err != nil {
			t.Fatalf("series dim=%s: %v", d, err)
		}
		for _, p := range points {
			if !totalKeys[p.Key] {
				t.Errorf("dim=%s: the series has a group %q the totals do not; the chart and table disagree",
					d, p.Key)
			}
		}
		if len(points) == 0 {
			t.Errorf("dim=%s: the series is empty for a non-empty window", d)
		}
	}
}

// An unknown bucket is a sentinel, like the dimension and metric ones.
func TestUnknownBucketIsSentinel(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	r := openSeeded(t, event(base, "p", "m"))
	w := Window{Since: base.Add(-time.Hour)}

	if _, err := r.PivotSeries(context.Background(), w, DimProvider, MetricCost, "nonsense", 6); !errors.Is(err, ErrUnknownBucket) {
		t.Errorf("unknown bucket = %v, want ErrUnknownBucket", err)
	}
	if _, err := r.PivotSeries(context.Background(), w, "nonsense", MetricCost, BucketHour, 6); !errors.Is(err, ErrUnknownDimension) {
		t.Errorf("unknown dimension = %v, want ErrUnknownDimension", err)
	}
	if _, err := r.PivotSeries(context.Background(), w, DimProvider, "nonsense", BucketHour, 6); !errors.Is(err, ErrUnknownMetric) {
		t.Errorf("unknown metric = %v, want ErrUnknownMetric", err)
	}
}

// The series is capped to the top-N groups by the chosen metric, so a chart does
// not try to draw a line per distinct model.
func TestSeriesIsLimitedToTopGroups(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	var events []Event
	for i := 0; i < 6; i++ {
		ev := event(base.Add(time.Duration(i)*time.Minute), string(rune('a'+i)), "m")
		ev.Usage = usageOf(1, 1, float64(i))
		events = append(events, ev)
	}
	r := openSeeded(t, events...)
	w := Window{Since: base.Add(-time.Hour)}

	points, err := r.PivotSeries(context.Background(), w, DimProvider, MetricCost, BucketHour, 2)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, p := range points {
		keys[p.Key] = true
	}
	if len(keys) != 2 {
		t.Errorf("series returned %d groups with topN=2: %v", len(keys), keys)
	}
	// The two kept must be the highest-cost ones, which is what the metric means.
	if !keys["e"] || !keys["f"] {
		t.Errorf("kept groups = %v, want the two most expensive (e, f)", keys)
	}
}

// An empty window is an empty slice, not nil.
func TestSeriesEmptyIsEmptySlice(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	r := openSeeded(t, event(base, "p", "m"))
	points, err := r.PivotSeries(context.Background(), Window{Since: base.Add(time.Hour)},
		DimProvider, MetricCost, BucketHour, 6)
	if err != nil {
		t.Fatal(err)
	}
	if points == nil {
		t.Error("an empty series returned nil; it must be an empty slice")
	}
}
