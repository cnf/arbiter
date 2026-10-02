package guardrail

import (
	"context"
	"strings"
	"testing"

	"github.com/cnf/arbiter/pkg/types"
)

// fakeForget records the (sessionKey, promptHash) pairs a guardrail asked to
// clear, so a test can assert both that a pin was cleared and WHICH family was
// targeted — the whole point of keying on promptHash.
type fakeForget struct {
	calls [][2]string
}

func (f *fakeForget) ForgetPin(_ context.Context, sessionKey, promptHash string) {
	f.calls = append(f.calls, [2]string{sessionKey, promptHash})
}

func newUnpin(t *testing.T, match, mode string, strip bool) *UnpinGuardrail {
	t.Helper()
	g, err := NewUnpinGuardrail("test", match, mode, strip)
	if err != nil {
		t.Fatalf("NewUnpinGuardrail: %v", err)
	}
	return g
}

// reqWithLast builds a request whose LAST message carries lastText, plus an
// optional single earlier message. sessionKey/promptHash are set so ShouldRun
// passes.
func reqWithLast(sessionKey, promptHash, lastText string, earlier ...string) *types.NormalizedRequest {
	msgs := make([]types.Message, 0, len(earlier)+1)
	for _, e := range earlier {
		msgs = append(msgs, types.Message{Role: "user", Content: []types.ContentBlock{types.TextBlock(e)}})
	}
	msgs = append(msgs, types.Message{Role: "user", Content: []types.ContentBlock{types.TextBlock(lastText)}})
	return &types.NormalizedRequest{SessionKey: sessionKey, PromptHash: promptHash, Messages: msgs}
}

// TestUnpinFiresOnTheLastMessageMarker is the happy path: a marker in the
// newest message clears exactly this prompt family's pin.
func TestUnpinFiresOnTheLastMessageMarker(t *testing.T) {
	g := newUnpin(t, "#reclassify", "prefix", false)
	f := &fakeForget{}
	g.SetPinForgetter(f)

	req := reqWithLast("chat-1", "fam-hash", "#reclassify please re-route")
	if _, err := g.ApplyPre(context.Background(), req); err != nil {
		t.Fatalf("ApplyPre: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("ForgetPin calls = %d, want 1", len(f.calls))
	}
	if f.calls[0] != [2]string{"chat-1", "fam-hash"} {
		t.Errorf("ForgetPin called with %v, want [chat-1 fam-hash]", f.calls[0])
	}
}

// TestUnpinIgnoresAMarkerInHistory is the one-shot property the whole design
// turns on: the conversation is re-sent every turn, so a marker that lived in
// history (not the last message) must NOT re-fire on later turns.
func TestUnpinIgnoresAMarkerInHistory(t *testing.T) {
	g := newUnpin(t, "#reclassify", "prefix", false)
	f := &fakeForget{}
	g.SetPinForgetter(f)

	// The marker is in an earlier turn; the newest message is ordinary.
	req := reqWithLast("chat-1", "fam-hash", "and now do the thing", "#reclassify")
	if _, err := g.ApplyPre(context.Background(), req); err != nil {
		t.Fatalf("ApplyPre: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("ForgetPin calls = %d, want 0 (a marker in history must not re-fire)", len(f.calls))
	}
}

// TestUnpinStripRemovesTheMarker proves the toggle: with strip=true the marker
// is gone from the last message before the request goes upstream.
func TestUnpinStripRemovesTheMarker(t *testing.T) {
	g := newUnpin(t, "#reclassify", "prefix", true)
	g.SetPinForgetter(&fakeForget{})

	req := reqWithLast("chat-1", "h", "#reclassify answer this")
	if _, err := g.ApplyPre(context.Background(), req); err != nil {
		t.Fatalf("ApplyPre: %v", err)
	}
	got := req.Messages[len(req.Messages)-1].Content[0].Text
	if strings.Contains(got, "#reclassify") {
		t.Errorf("marker survived strip: %q", got)
	}
	// Only the matched span is removed — the rest of the message stays, so a
	// stripped directive does not wipe what the user actually asked.
	if !strings.Contains(got, "answer this") {
		t.Errorf("strip removed more than the match: %q", got)
	}
}

// TestUnpinDefaultLeavesTheMarker is the other half of the toggle: strip=false
// (the default) must not edit the prompt, so a classifier `match:` block can
// still see the marker and route on it.
func TestUnpinDefaultLeavesTheMarker(t *testing.T) {
	g := newUnpin(t, "#code", "prefix", false)
	g.SetPinForgetter(&fakeForget{})

	req := reqWithLast("chat-1", "h", "#code fix this bug")
	if _, err := g.ApplyPre(context.Background(), req); err != nil {
		t.Fatalf("ApplyPre: %v", err)
	}
	got := req.Messages[len(req.Messages)-1].Content[0].Text
	if !strings.Contains(got, "#code") {
		t.Errorf("strip=false removed the marker: %q", got)
	}
}

// TestUnpinWithoutASessionDoesNothing proves the guardrail stays inert for a
// request that is not part of a session — there is no pin to clear.
func TestUnpinWithoutASessionDoesNothing(t *testing.T) {
	g := newUnpin(t, "#reclassify", "prefix", false)
	f := &fakeForget{}
	g.SetPinForgetter(f)

	req := reqWithLast("", "h", "#reclassify now")
	if g.ShouldRun(req) {
		t.Error("ShouldRun = true for a request with no session key")
	}
	if _, err := g.ApplyPre(context.Background(), req); err != nil {
		t.Fatalf("ApplyPre: %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("ForgetPin calls = %d, want 0 (no session, no pin)", len(f.calls))
	}
}

// TestUnpinNoMatchLeavesTheRequestUntouched is the safety property: a request
// that does not carry the marker must be returned unchanged and clear nothing.
func TestUnpinNoMatchLeavesTheRequestUntouched(t *testing.T) {
	g := newUnpin(t, "#reclassify", "prefix", true)
	f := &fakeForget{}
	g.SetPinForgetter(f)

	req := reqWithLast("chat-1", "h", "an ordinary message")
	before := req.Messages[len(req.Messages)-1].Content[0].Text
	if _, err := g.ApplyPre(context.Background(), req); err != nil {
		t.Fatalf("ApplyPre: %v", err)
	}
	if got := req.Messages[len(req.Messages)-1].Content[0].Text; got != before {
		t.Errorf("text changed on a non-match: %q -> %q", before, got)
	}
	if len(f.calls) != 0 {
		t.Errorf("ForgetPin calls = %d, want 0", len(f.calls))
	}
}

// TestUnpinSkipsANonTextLastMessage proves an agentic request whose newest
// message is a tool_result (no text) is not matched — there is no free text to
// carry a marker.
func TestUnpinSkipsANonTextLastMessage(t *testing.T) {
	g := newUnpin(t, "#reclassify", "prefix", false)
	f := &fakeForget{}
	g.SetPinForgetter(f)

	req := &types.NormalizedRequest{
		SessionKey: "chat-1", PromptHash: "h",
		Messages: []types.Message{
			{Role: "user", Content: []types.ContentBlock{types.TextBlock("#reclassify")}},
			{Role: "tool", Content: []types.ContentBlock{{Type: "tool_result", ToolResult: "ok"}}},
		},
	}
	// The marker is in the FIRST message but the last is tool-only, so nothing
	// may match — the last message is what counts.
	if _, err := g.ApplyPre(context.Background(), req); err != nil {
		t.Fatalf("ApplyPre: %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("ForgetPin calls = %d, want 0 (last message carries no text)", len(f.calls))
	}
}

// TestUnpinEmptyMatchIsALoadError: a pattern that would clear a pin on every
// request is refused at load.
func TestUnpinEmptyMatchIsALoadError(t *testing.T) {
	if _, err := NewUnpinGuardrail("t", "", "prefix", false); err == nil {
		t.Error("expected an error for an empty match")
	}
}

// TestUnpinUnknownModeIsALoadError proves a typo'd mode fails at load rather
// than silently never firing.
func TestUnpinUnknownModeIsALoadError(t *testing.T) {
	if _, err := NewUnpinGuardrail("t", "#x", "substring", false); err == nil {
		t.Error("expected an error for an unknown mode")
	}
}

// TestUnpinRegexModeMatches is the escape hatch for a marker that varies.
func TestUnpinRegexModeMatches(t *testing.T) {
	g := newUnpin(t, `#reclassify\b`, "regex", false)
	f := &fakeForget{}
	g.SetPinForgetter(f)

	req := reqWithLast("chat-1", "h", "#reclassify")
	if _, err := g.ApplyPre(context.Background(), req); err != nil {
		t.Fatalf("ApplyPre: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("ForgetPin calls = %d, want 1", len(f.calls))
	}
}
