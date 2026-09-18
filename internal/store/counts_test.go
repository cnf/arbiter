package store

import (
	"context"
	"testing"
	"time"
)

// CountSince feeds a rate-limit guardrail's baseline. The properties that matter:
// it respects the time bound, and it counts every kind of request — including
// Arbiter's own internal calls, which are real upstream requests and so are part
// of what an upstream's own cap would see.

func TestCountSinceRespectsTheBound(t *testing.T) {
	r, db := newTestReader(t)
	ctx := context.Background()

	insertRow(t, db, Event{TraceID: "old", Ts: time.Now().Add(-2 * time.Hour)})
	insertRow(t, db, Event{TraceID: "new1", Ts: time.Now().Add(-time.Minute)})
	insertRow(t, db, Event{TraceID: "new2", Ts: time.Now()})

	n, err := r.CountSince(ctx, time.Now().Add(-30*time.Minute))
	if err != nil {
		t.Fatalf("CountSince: %v", err)
	}
	if n != 2 {
		t.Errorf("CountSince = %d, want 2 (the 2h-old row is outside the window)", n)
	}
}

// TestCountSinceIncludesInternalKinds proves classifier and other internal rows
// are counted. The spend aggregates filter them out, because classifier cost is
// not the operator's traffic; a rate limit mirroring an upstream's cap is a
// different question — the upstream counts those requests.
func TestCountSinceIncludesInternalKinds(t *testing.T) {
	r, db := newTestReader(t)
	ctx := context.Background()

	insertRow(t, db, Event{TraceID: "c1", Ts: time.Now(), Kind: "client"})
	insertRow(t, db, Event{TraceID: "c2", Ts: time.Now(), Kind: "classifier"})

	n, err := r.CountSince(ctx, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("CountSince: %v", err)
	}
	if n != 2 {
		t.Errorf("CountSince = %d, want 2 — internal requests reach the upstream too", n)
	}
}

func TestCountSinceEmptyStore(t *testing.T) {
	r, _ := newTestReader(t)
	n, err := r.CountSince(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("CountSince: %v", err)
	}
	if n != 0 {
		t.Errorf("CountSince = %d on an empty store, want 0", n)
	}
}

// TestCountSinceByProvider is the per-provider variant REQUIREMENTS §3 asks for
// (mirroring e.g. an upstream's published 10/min), so it must narrow correctly.
func TestCountSinceByProvider(t *testing.T) {
	r, db := newTestReader(t)
	ctx := context.Background()

	insertRow(t, db, Event{TraceID: "a", Provider: "primary", Ts: time.Now()})
	insertRow(t, db, Event{TraceID: "b", Provider: "primary", Ts: time.Now()})
	insertRow(t, db, Event{TraceID: "c", Provider: "other", Ts: time.Now()})

	n, err := r.CountSinceByProvider(ctx, "primary", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("CountSinceByProvider: %v", err)
	}
	if n != 2 {
		t.Errorf("CountSinceByProvider = %d, want 2", n)
	}
}
