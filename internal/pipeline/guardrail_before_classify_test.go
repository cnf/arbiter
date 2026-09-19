package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/classifier"
	"github.com/cnf/arbiter/internal/guardrail"
	"github.com/cnf/arbiter/pkg/types"
)

// firstLine is a test helper for error messages: the signature's position in a
// prompt is the whole question here, so the first line is what a failure should
// show.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// TestMatchBlockSurvivesASystemPromptGuardrailPrepending reproduces a live
// report — a title-gen `match` block never fired and the request fell through
// to a decisions classifier that labelled it `search` — and then pins the fix.
//
// The cause was an interaction between two supported features. A
// `system_prompt` guardrail with `override: false` PREPENDS its text to
// req.SystemPrompt (guardrail.go:60), and pre-guardrails run BEFORE classify.
// So a `mode: "prefix"` match — which requires the searched text to START with
// the pattern — saw Arbiter's own preamble first and could not see the client's
// signature behind it. Nothing warned, and the stored preamble still showed the
// signature first, because capture runs before guardrails too: the config
// looked correct in every view the operator had while the matcher saw
// different text.
//
// The fix is that the matcher reads ClientSystemPrompt — the prompt as the
// client sent it — so an injected preamble can no longer hide a signature.
func TestMatchBlockSurvivesASystemPromptGuardrailPrepending(t *testing.T) {
	const titlePrompt = "You name chat sessions. Given the user's opening message, write a title.\n\nRules:\n- 3 to 7 words."

	matcher, err := classifier.NewRequestMatcher(
		[]types.MatchPattern{{Pattern: "You name chat sessions.", Mode: "prefix"}},
		"title_generation", nil, true)
	if err != nil {
		t.Fatalf("NewRequestMatcher: %v", err)
	}
	hc := classifier.NewHeuristicClassifierFull("request-kind", classifier.AxisDomain,
		nil, matcher, nil, 0)

	// As the client sent it, and with the field the pipeline sets.
	asSent := &types.NormalizedRequest{
		SystemPrompt:       titlePrompt,
		ClientSystemPrompt: titlePrompt,
		Messages:           []types.Message{{Role: "user", Content: []types.ContentBlock{types.TextBlock("hi")}}},
	}
	sig, err := hc.Classify(context.Background(), asSent)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "title_generation" {
		t.Fatalf("Domain = %q, want title_generation on the request as the client sent it", sig.Domain)
	}

	// Now the deployment's own guardrail runs first, exactly as the pipeline
	// orders it — and the matcher must still hit, because it reads the prompt
	// as sent rather than as mutated.
	prepender := guardrail.NewSystemPromptGuardrail("inject", "You are a helpful assistant. Be direct and concise.", false)
	afterGuardrail, err := prepender.ApplyPre(context.Background(), asSent)
	if err != nil {
		t.Fatalf("ApplyPre: %v", err)
	}
	// ApplyPre mutates in place and returns the same request, so the check is
	// on the field, not on pointer identity.
	if !strings.HasPrefix(afterGuardrail.SystemPrompt, "You are a helpful assistant.") {
		t.Fatalf("guardrail did not prepend; SystemPrompt now starts %q — this test no longer reproduces the deployment's config",
			firstLine(afterGuardrail.SystemPrompt))
	}

	sig, err = hc.Classify(context.Background(), afterGuardrail)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if sig.Domain != "title_generation" {
		t.Fatalf("Domain = %q, want title_generation — an injected preamble must not hide the client's signature", sig.Domain)
	}
}

// Without the pre-guardrail snapshot the same matcher misses, which is the
// regression this whole change exists to prevent. Asserted on a request that
// carries ONLY the mutated prompt, so it fails if the fallback ever starts
// reading SystemPrompt in preference to ClientSystemPrompt.
func TestMatcherFallsBackToSystemPromptWhenNoSnapshot(t *testing.T) {
	matcher, err := classifier.NewRequestMatcher(
		[]types.MatchPattern{{Pattern: "You name chat sessions.", Mode: "prefix"}},
		"title_generation", nil, false)
	if err != nil {
		t.Fatalf("NewRequestMatcher: %v", err)
	}
	hc := classifier.NewHeuristicClassifierFull("request-kind", classifier.AxisDomain, nil, matcher, nil, 0)

	mutated := &types.NormalizedRequest{
		SystemPrompt: "You are a helpful assistant. Be direct and concise.\n\nYou name chat sessions. Given the user's opening message...",
		Messages:     []types.Message{{Role: "user", Content: []types.ContentBlock{types.TextBlock("hi")}}},
	}
	sig, err := hc.Classify(context.Background(), mutated)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	// No snapshot means nothing to fall back TO but the mutated prompt, so a
	// prefix match genuinely cannot hit. This documents the limit rather than
	// pretending it away: the fix depends on the pipeline setting the field.
	if sig.Domain == "title_generation" {
		t.Fatal("a prefix match hit a signature behind an injected preamble with no snapshot — the fallback is not doing what it claims")
	}
}

// The pipeline's own ordering is what makes this reachable in production: the
// guardrail loop runs before resolveRoute (which is what calls classify). This
// asserts that order rather than restating it in a comment, so a future change
// that moved classification ahead of the guardrails would surface here.
func TestGuardrailsRunBeforeClassification(t *testing.T) {
	var order []string
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	w := &capturingWriter{}
	marker := &orderClassifier{order: &order}
	g := &orderGuardrail{order: &order}

	p := NewPipeline(
		nil, fakeNormalizer{model: ""}, fakeDenormalizer{},
		[]classifier.Classifier{marker}, &fakeRouter{route: primaryRoute()}, fu, testProviders(), nil,
		[]guardrail.Guardrail{g}, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	if _, err := p.Execute(context.Background(), []byte("anything at all here"), "openai", "t1", "chat-1"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(order) != 2 || order[0] != "guardrail" || order[1] != "classify" {
		t.Fatalf("order = %v, want [guardrail classify] — a guardrail runs BEFORE classification, which is what lets an injected preamble hide a signature", order)
	}
}

type orderClassifier struct{ order *[]string }

func (c *orderClassifier) Classify(context.Context, *types.NormalizedRequest) (types.Signals, error) {
	*c.order = append(*c.order, "classify")
	return types.Signals{}, nil
}

type orderGuardrail struct{ order *[]string }

func (g *orderGuardrail) Name() string { return "order" }
func (g *orderGuardrail) ShouldRun(*types.NormalizedRequest) bool {
	return true
}
func (g *orderGuardrail) ApplyPre(_ context.Context, req *types.NormalizedRequest) (*types.NormalizedRequest, error) {
	*g.order = append(*g.order, "guardrail")
	return req, nil
}
func (g *orderGuardrail) ApplyPost(_ context.Context, resp *types.NormalizedResponse, _ types.Route) (*types.NormalizedResponse, error) {
	return resp, nil
}
