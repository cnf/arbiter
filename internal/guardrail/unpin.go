package guardrail

import (
	"context"
	"fmt"

	"github.com/cnf/arbiter/pkg/types"
)

// PinClearer is implemented by a guardrail that clears session-affinity pins.
// The wiring code collects the guardrails that implement it and hands them the
// pipeline's affinity store once the pipeline exists (see
// Pipeline.SetGuardrailPins).
type PinClearer interface {
	SetPinForgetter(PinForgetter)
}

// PinForgetter drops one prompt family's session-affinity pin — both the
// in-process cache entry and the persisted row. A narrow interface so this
// package never depends on the pipeline's affinity store, and so tests can
// substitute a fake — the shape CountSource establishes.
//
// It carries no error: clearing a pin is best-effort, exactly as the affinity
// store's own forget is (a persistence failure there is already swallowed, so
// the conversation stays pinned for this process and only loses durability).
// Failing a client request because a pin could not be cleared would be the
// worse outcome.
//
// It takes promptHash as well as sessionKey because a pin is keyed by
// (sessionKey, promptHash): one chat session contains several prompt families
// (the main thread, a title call, a subagent run), and only the family the
// directive appeared in must be cleared — see pipeline.affinityKey.
type PinForgetter interface {
	ForgetPin(ctx context.Context, sessionKey, promptHash string)
}

// UnpinGuardrail clears the session-affinity pin for the prompt family a
// request belongs to, when the request itself carries a configured marker
// (e.g. `#reclassify`).
//
// Purpose: a user's directive to RE-ROUTE this conversation. A pinned
// conversation short-circuits classification and rule matching entirely
// (resolveRoute returns from the affinity branch before the router runs), so
// the only way to get a fresh routing decision mid-conversation is to drop the
// pin first. Because pre-guardrails run before resolveRoute, a pre-hook is the
// right place to do it: the pin is gone by the time the affinity lookup runs,
// and the request falls through to classify + rules.
//
// The marker is matched against the request's LAST message only, never the
// conversation history. That is what makes the directive a one-shot: the whole
// conversation is re-sent on every turn, so a marker that lived in history
// would keep matching on every later turn and re-route forever. Checking only
// the newest message means a `#reclassify` typed once fires once.
//
// It is deliberately NOT a classifier. Its whole vocabulary is "clear the pin"
// (and, optionally, remove the marker text), which is a pre-request mutation of
// the same kind system_prompt and prompt_rewrite perform. Anything that
// produces ROUTING SIGNALS belongs to a classifier — that is RequestMatcher's
// job (internal/classifier/match.go), and a `match:` block there is how a
// `#code`-style marker can instead FORCE an axis on the turn(s) it is meant to
// apply.
type UnpinGuardrail struct {
	name    string
	matcher *types.TextMatcher

	// strip removes the matched marker from the last message before the
	// request goes upstream. Default false: a guardrail whose job is to unpin
	// must not silently edit the prompt. Set true to keep the marker out of
	// the upstream call. Note the consequence: pre-guardrails run BEFORE
	// classification, so a stripped marker is also invisible to a classifier's
	// `match:` block — strip=true is for pure "unpin and let normal
	// classification decide", while strip=false keeps the marker available to
	// a classifier that routes on it.
	strip bool

	// pins is injected after construction, because the affinity store it
	// clears is created by the pipeline (which is built after guardrails).
	// Nil means nothing to clear — the correct degradation when no store and
	// no pipeline are wired (a bare guardrail in a test).
	pins PinForgetter
}

// NewUnpinGuardrail builds the guardrail from config values already extracted
// by the caller. match is required; mode defaults to prefix inside
// types.NewTextMatcher (the mode a marker like `#reclassify` almost always
// wants: it is the opening of the message).
func NewUnpinGuardrail(name, match, mode string, strip bool) (*UnpinGuardrail, error) {
	if name == "" {
		return nil, fmt.Errorf("unpin guardrail needs a name")
	}
	if match == "" {
		return nil, fmt.Errorf("unpin guardrail %q: match is required (an empty pattern would unpin on every request)", name)
	}
	// Compiled by the shared matcher, so an invalid regex or an unknown mode
	// is a load-time error — a guardrail that silently never fires would leave
	// the operator believing the marker re-routes when every turn stays pinned.
	matcher, err := types.NewTextMatcher(match, types.MatchMode(mode))
	if err != nil {
		return nil, fmt.Errorf("unpin guardrail %q: %w", name, err)
	}
	return &UnpinGuardrail{name: name, matcher: matcher, strip: strip}, nil
}

// SetPinForgetter wires the store this guardrail clears. Called by the wiring
// code after the pipeline exists, following the same post-construction shape
// as SetCaptureContent/SetNoPin: the collaborator is created by NewPipeline,
// and threading it through the guardrail constructors would require building
// the pipeline before the guardrails.
func (g *UnpinGuardrail) SetPinForgetter(p PinForgetter) {
	g.pins = p
}

func (g *UnpinGuardrail) Name() string { return g.name }

// ShouldRun reports whether this request is even a candidate: it must belong to
// a session (otherwise there is no pin to clear) and carry a last message with
// text to match. A request with no session key is skipped so the applied/
// rejected log line stays meaningful — an "applied" line for a guardrail that
// could not have done anything would make the log useless.
func (g *UnpinGuardrail) ShouldRun(req *types.NormalizedRequest) bool {
	return req.SessionKey != "" && len(lastMessageText(req)) > 0
}

// ApplyPre clears the pin when the last message carries the marker, optionally
// stripping the marker text. A non-matching request is returned untouched, and
// no pin is cleared — the safety property the whole guardrail rests on.
func (g *UnpinGuardrail) ApplyPre(ctx context.Context, req *types.NormalizedRequest) (*types.NormalizedRequest, error) {
	// No session, no pin to clear. ShouldRun normally gates this, but the
	// guard also lives here so a direct call cannot clear the empty-session
	// slot by accident.
	if req.SessionKey == "" {
		return req, nil
	}
	hit := false
	for _, b := range lastMessageTextBlocks(req) {
		if !g.matcher.Matches(b.Text) {
			continue
		}
		hit = true
		if g.strip {
			b.Text = stripFirstMatch(b.Text, g.matcher)
		}
	}
	if !hit {
		return req, nil
	}
	if g.pins != nil {
		g.pins.ForgetPin(ctx, req.SessionKey, req.PromptHash)
	}
	return req, nil
}

func (g *UnpinGuardrail) ApplyPost(ctx context.Context, resp *types.NormalizedResponse, route types.Route) (*types.NormalizedResponse, error) {
	// No-op for post — clearing a pin is a pre-request concern.
	return resp, nil
}

// lastMessageTextBlocks returns pointers to the text blocks of the request's
// FINAL message, in order. Pointers so a caller (the strip path) can rewrite
// the text in place. Non-text blocks are skipped: a tool_result or attachment
// block is not free text, and matching or rewriting it would be wrong.
//
// Only the last message is examined, never earlier ones — see UnpinGuardrail's
// doc for why that is what makes the marker a one-shot. An empty message list,
// or a last message whose blocks are all non-text (an agentic request ending on
// a tool_result), yields nothing and therefore no match.
func lastMessageTextBlocks(req *types.NormalizedRequest) []*types.ContentBlock {
	if req == nil || len(req.Messages) == 0 {
		return nil
	}
	last := req.Messages[len(req.Messages)-1]
	var out []*types.ContentBlock
	for i := range last.Content {
		if last.Content[i].Type == "text" {
			out = append(out, &last.Content[i])
		}
	}
	return out
}

// lastMessageText is the concatenated text of the last message's text blocks,
// used only for the ShouldRun emptiness check.
func lastMessageText(req *types.NormalizedRequest) string {
	var s string
	for _, b := range lastMessageTextBlocks(req) {
		s += b.Text
	}
	return s
}

// stripFirstMatch removes the first span the matcher finds in text, leaving the
// surrounding text — the same "remove the matched span only" semantics
// PromptRewriteGuardrail's strip action has. Kept local rather than shared
// because PromptRewriteGuardrail's strip is a method over its own matcher, and
// the two guardrails have separate matchers; the shared matching rules live in
// types.TextMatcher either way.
func stripFirstMatch(text string, m *types.TextMatcher) string {
	loc := m.Find(text)
	if loc == nil {
		return text
	}
	return text[:loc[0]] + text[loc[1]:]
}
