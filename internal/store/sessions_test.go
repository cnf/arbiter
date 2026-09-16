package store

import (
	"context"
	"testing"
	"time"
)

// The sessions index groups conversations and must exclude the requests that
// have no session key — they are not one conversation, and grouping them would
// produce a fake session whose transcript is unrelated requests. They are
// counted separately so they stay visible.
func TestSessionsGroupsAndExcludesKeyless(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	withKey := func(ts time.Time, key, provider, model string, status int) Event {
		ev := event(ts, provider, model)
		ev.SessionKey = key
		ev.StatusCode = status
		return ev
	}
	keyless := func(ts time.Time) Event {
		ev := event(ts, "p", "m")
		ev.SessionKey = ""
		return ev
	}
	r := openSeeded(t,
		withKey(base, "s1", "alpha", "m1", 200),
		withKey(base.Add(time.Minute), "s1", "beta", "m2", 500),
		withKey(base.Add(2*time.Minute), "s2", "alpha", "m1", 200),
		keyless(base.Add(3*time.Minute)),
		keyless(base.Add(4*time.Minute)),
	)

	w := Window{Since: base.Add(-time.Hour)}
	got, err := r.Sessions(context.Background(), w, 0)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Sessions returned %d conversations, want 2 (the keyless rows are not one)", len(got))
	}
	// Most recently active first. s2's only turn is at +2m, s1's last is at +1m,
	// so s2 leads — the ordering is by the session's newest turn, not by its key.
	if got[0].Key != "s2" || got[1].Key != "s1" {
		t.Errorf("order = %s, %s; want s2 then s1 (newest turn first)", got[0].Key, got[1].Key)
	}

	var s1 SessionSummary
	for _, s := range got {
		if s.Key == "s1" {
			s1 = s
		}
	}
	if s1.Key == "" {
		t.Fatal("s1 is missing from the index")
	}
	if s1.Turns != 2 {
		t.Errorf("s1 turns = %d, want 2", s1.Turns)
	}
	if s1.Errors != 1 {
		t.Errorf("s1 errors = %d, want 1", s1.Errors)
	}
	if s1.Models != 2 {
		t.Errorf("s1 distinct models = %d, want 2 (alpha/m1 and beta/m2)", s1.Models)
	}
	// group_concat's default comma separator, both providers present.
	if s1.Providers != "alpha,beta" && s1.Providers != "beta,alpha" {
		t.Errorf("s1 providers = %q, want both providers comma-joined", s1.Providers)
	}
	if s1.FirstSeen == "" || s1.LastSeen == "" {
		t.Error("span is empty; the index would not show when the conversation ran")
	}

	// The keyless requests are counted, not grouped.
	n, err := r.SessionlessRequestCount(context.Background(), w)
	if err != nil {
		t.Fatalf("SessionlessRequestCount: %v", err)
	}
	if n != 2 {
		t.Errorf("sessionless count = %d, want 2", n)
	}
}

// A windowed session reads short: the aggregate counts only turns inside the
// window, which is why the index must label its window rather than present
// itself as the session's totals.
func TestSessionsAreWindowed(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	early := event(base, "p", "m")
	early.SessionKey = "s1"
	late := event(base.Add(2*time.Hour), "p", "m")
	late.SessionKey = "s1"
	r := openSeeded(t, early, late)

	all, err := r.Sessions(context.Background(), Window{Since: base.Add(-time.Hour)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Turns != 2 {
		t.Fatalf("whole window: %+v, want one session with 2 turns", all)
	}

	narrow, err := r.Sessions(context.Background(), Window{Since: base.Add(time.Hour)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(narrow) != 1 || narrow[0].Turns != 1 {
		t.Fatalf("narrow window: %+v, want the same session with 1 turn", narrow)
	}
	// The truncated view's FirstSeen is the window edge, not the session's real
	// start — the documented trade for keeping idx_requests_session.
	if narrow[0].FirstSeen == all[0].FirstSeen {
		t.Error("FirstSeen did not move with the window; the trade is not what the docs claim")
	}
}

// The index cap is a real bound, so a caller can tell a full page from a
// truncated one.
func TestSessionsLimitIsClamped(t *testing.T) {
	base := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	var events []Event
	for i := 0; i < 5; i++ {
		ev := event(base.Add(time.Duration(i)*time.Minute), "p", "m")
		ev.SessionKey = string(rune('a' + i))
		events = append(events, ev)
	}
	r := openSeeded(t, events...)

	got, err := r.Sessions(context.Background(), Window{Since: base.Add(-time.Hour)}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("limit 2 returned %d sessions", len(got))
	}

	// An over-large limit is clamped rather than passed to sqlite.
	many, err := r.Sessions(context.Background(), Window{Since: base.Add(-time.Hour)}, MaxSessionListLimit*10)
	if err != nil {
		t.Fatal(err)
	}
	if len(many) != 5 {
		t.Errorf("clamped limit returned %d sessions, want all 5", len(many))
	}
}
