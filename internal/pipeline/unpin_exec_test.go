package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/guardrail"
	"github.com/cnf/arbiter/pkg/types"
)

// newUnpinPipeline builds the affinity pipeline with an unpin pre-guardrail
// wired to its own affinity store — the production wiring path, in miniature.
func newUnpinPipeline(t *testing.T, rr *recordingRouter, fu *fakeUpstream, n fakeNormalizer, match string, strip bool) *Pipeline {
	t.Helper()
	p := newAffinityPipeline(rr, fu, n, nil, time.Minute)
	g, err := guardrail.NewUnpinGuardrail("unpin", match, "prefix", strip)
	if err != nil {
		t.Fatalf("NewUnpinGuardrail: %v", err)
	}
	p.preGuardrails = []guardrail.Guardrail{g}
	p.SetGuardrailPins(g)
	return p
}

// TestUnpinMarkerReroutesAPinnedConversation is the end-to-end property the
// feature exists for: turn 1 pins the conversation; turn 2 carries the marker,
// so the pin is cleared and the routing chain runs again instead of the pin
// short-circuiting it.
func TestUnpinMarkerReroutesAPinnedConversation(t *testing.T) {
	rr := &recordingRouter{route: primaryRoute()}
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	n := fakeNormalizer{model: "auto"}
	p := newUnpinPipeline(t, rr, fu, n, "#reclassify", false)

	// The session header forces both turns onto the same session key, so the
	// pin from turn 1 is what turn 2 must be able to hit (and, with the
	// marker, must NOT).
	if _, err := p.Execute(context.Background(), []byte("explain how the custom parser handles nesting"), "openai", "t1", "chat-1"); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if rr.calls != 1 {
		t.Fatalf("routing calls after turn 1 = %d, want 1", rr.calls)
	}

	// Turn 2 without a marker would hit the pin (routing stays 1) — that is
	// TestAffinityPinRecordedAndReused. WITH the marker, the pin is cleared
	// and routing runs again.
	if _, err := p.Execute(context.Background(), []byte("#reclassify now take another look"), "openai", "t2", "chat-1"); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if rr.calls != 2 {
		t.Fatalf("routing calls after the marker turn = %d, want 2 (the pin must have been cleared)", rr.calls)
	}
}

// TestUnpinMarkerOnlyFiresOnTheLastMessage is the one-shot property end to
// end: a marker that has fallen into history (an earlier message) must not
// keep re-routing. Here the marker is in turn 1's payload but turn 2 is
// ordinary, and the session header keeps them on one key — so turn 2 hits the
// pin (routing skipped) exactly as it would without the feature.
func TestUnpinFiresOnlyOnTheTurnCarryingTheMarker(t *testing.T) {
	rr := &recordingRouter{route: primaryRoute()}
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	n := fakeNormalizer{model: "auto"}
	p := newUnpinPipeline(t, rr, fu, n, "#reclassify", false)

	// Turn 1 itself carries the marker: no pin exists yet, so routing runs
	// once and records a pin.
	if _, err := p.Execute(context.Background(), []byte("#reclassify start here"), "openai", "t1", "chat-1"); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if rr.calls != 1 {
		t.Fatalf("routing calls after turn 1 = %d, want 1", rr.calls)
	}

	// Turn 2 has NO marker, so the pin from turn 1 applies and routing is
	// skipped — proving the marker does not linger in history.
	if _, err := p.Execute(context.Background(), []byte("continue please"), "openai", "t2", "chat-1"); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if rr.calls != 1 {
		t.Fatalf("routing calls after an unmarked turn = %d, want still 1 (the pin must hold)", rr.calls)
	}
}

// TestUnpinStripStillReroutes proves strip does not defeat the unpin: the pin
// is cleared in the same pass that removes the marker text.
func TestUnpinStripStillReroutes(t *testing.T) {
	rr := &recordingRouter{route: primaryRoute()}
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	n := fakeNormalizer{model: "auto"}
	p := newUnpinPipeline(t, rr, fu, n, "#reclassify", true)

	if _, err := p.Execute(context.Background(), []byte("explain how the custom parser handles nesting"), "openai", "t1", "chat-1"); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if _, err := p.Execute(context.Background(), []byte("#reclassify retry the route"), "openai", "t2", "chat-1"); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if rr.calls != 2 {
		t.Fatalf("routing calls = %d, want 2 (strip must not stop the unpin)", rr.calls)
	}
}
