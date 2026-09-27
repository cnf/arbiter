package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/classifier"
	"github.com/cnf/arbiter/internal/guardrail"
	"github.com/cnf/arbiter/internal/translator"
	"github.com/cnf/arbiter/pkg/types"
)

// TestTitleGenIsIdentifiableOnTheLiteralModelPath is the end-to-end regression
// test for a live report: a Hermes title-generation request was stored, but
// nothing on its row said it was a title request. Same `model` value as
// ordinary traffic, empty axes, and the generic
// `explicit model "..." -> provider "..."` rationale — so the operator looked
// at the list and could not find it. Row 2930 in the deployment's store.
//
// It drives the actual Execute path with the REAL translator — a raw OpenAI
// payload in, so the system prompt arrives exactly the way Hermes sends it —
// through the deployment's own prepending `system_prompt` guardrail. Calling
// the matcher directly was tried and proved nothing: every wrong explanation in
// the session that filed this came from reasoning about the pieces instead of
// running them together.
//
// Three things are asserted together, because any one of them alone can pass
// while the feature is broken:
//
//  1. the stored row carries request_kind=title (the fix),
//  2. the route is STILL the model the client named (routing must not move),
//  3. the decisions classifier behind the signature never ran (the cost bound).
//
// Note the model is a LITERAL model in testProviders(), not an alias. That is
// the whole point: the alias path already classified, and it was the literal
// path — the one both real clients use — that produced the blank row.
func TestTitleGenIsIdentifiableOnTheLiteralModelPath(t *testing.T) {
	const titlePrompt = "You name chat sessions. Given the user's opening message, write a title that lets them find this conversation again in a list.\n\nRules:\n- 3 to 7 words, sentence case (capitalize only the first word and proper nouns)."

	// The deployment's signature: a kind, no axis. Declared FIRST and
	// decisive, so nothing behind it runs on a hit.
	matcher, err := classifier.NewRequestMatcher(
		[]types.MatchPattern{{Pattern: "You name chat sessions.", Mode: "prefix"}},
		"", "title", nil, true)
	if err != nil {
		t.Fatalf("NewRequestMatcher: %v", err)
	}
	requestKind := classifier.NewHeuristicClassifierFull(
		"request-kind", classifier.AxisDomain, nil, matcher, nil, 0)

	// A stand-in for the decisions classifier: model-backed, expensive, and
	// behind the signature. It must never be consulted for a title request.
	expensive := &countingClassifier{}

	// The deployment's own guardrail, which prepends before classification.
	prepender := guardrail.NewSystemPromptGuardrail(
		"system_prompt", "You are a helpful assistant. Be direct and concise.", false)

	// The snapshot must be taken before guardrails; if it is empty here the
	// signature can never survive. Asserted explicitly so a failure says which
	// step is broken rather than only that the end result is wrong.
	seen := &signalRecorder{route: primaryRoute()}
	probe := &snapshotProbe{inner: requestKind}
	fu := &fakeUpstream{resp: &types.NormalizedResponse{}}
	w := &capturingWriter{}

	// The real translator for all three roles: the request is parsed from wire
	// JSON exactly as a client sends it.
	tr := translator.NewDefaultTranslator()
	p := NewPipeline(
		tr, tr, tr,
		[]classifier.Classifier{probe, expensive}, seen, fu, testProviders(), nil,
		[]guardrail.Guardrail{prepender}, nil,
		fakeLogger{}, time.Minute, nil, w, nil, nil, nil)

	payload, err := json.Marshal(map[string]interface{}{
		"model":      "m-primary",
		"max_tokens": 100,
		"messages": []map[string]string{
			{"role": "system", "content": titlePrompt},
			{"role": "user", "content": "who was the oldest person ever recorded?"},
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	// Normalize the payload here first, so a failure distinguishes "the
	// translator did not produce a system prompt" from "the pipeline lost it".
	norm, err := tr.ToNormalized(payload, "openai")
	if err != nil {
		t.Fatalf("ToNormalized: %v", err)
	}
	t.Logf("after normalize: SystemPrompt starts %q", firstN(norm.SystemPrompt, 40))

	// No session hint: the key is derived from the conversation, which is what
	// a real title request has. Each title prompt carries new conversation text,
	// so each call is its own session and is therefore classified — correctly.
	if _, err := p.Execute(context.Background(), payload, "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	t.Logf("at classify time: SystemPrompt starts %q; ClientSystemPrompt starts %q",
		firstN(probe.systemPrompt, 40), firstN(probe.clientSystemPrompt, 40))
	if probe.clientSystemPrompt == "" {
		t.Errorf("ClientSystemPrompt was empty at classify time — the pipeline did not take the pre-guardrail snapshot")
	}
	if !strings.HasPrefix(probe.clientSystemPrompt, "You name chat sessions.") {
		t.Errorf("ClientSystemPrompt does not start with the signature: %q", firstN(probe.clientSystemPrompt, 60))
	}

	// 3. The signature is free; everything behind it was skipped.
	if expensive.calls != 0 {
		t.Errorf("the model-backed classifier ran %d times, want 0 — a decisive signature must end classification", expensive.calls)
	}

	// 1. The row says what the request was. This is the assertion that fails
	// with the fix reverted, and it fails for the right reason: without it
	// classifyLiteral is never called on this path, so RequestKind is empty.
	ev, ok := w.last()
	if !ok {
		t.Fatal("no event was recorded")
	}
	if ev.RequestKind != "title" {
		t.Fatalf("recorded request_kind = %q, want title.\n"+
			"The literal-model path must classify a request that is not yet part of a "+
			"session, or a title request is indistinguishable from ordinary traffic on "+
			"its row — same model value, empty axes, generic rationale.",
			ev.RequestKind)
	}
	// And it must not claim a content domain it does not have.
	if ev.Domain != "" {
		t.Errorf("recorded domain = %q, want empty — a kind-only signature fills no axis", ev.Domain)
	}

	// 2. Routing did not move. The client named a concrete model and must go
	// exactly there; classification here is a labelling change, never a
	// routing one.
	//
	// Asserted on the recorded row rather than on the router, because on this
	// path the router is deliberately never consulted: a signalRecorder would
	// see nothing whether the fix is present or not, which is a test that
	// cannot fail.
	if ev.Provider != "primary" || ev.Model != "m-primary" {
		t.Errorf("row = %s/%s, want primary/m-primary — the named model must still win",
			ev.Provider, ev.Model)
	}
	if !strings.Contains(ev.RoutingRationale, "explicit model") {
		t.Errorf("rationale = %q, want the literal-model rationale — classification must not replace routing",
			ev.RoutingRationale)
	}
	// An alias would mean the request was routed through the rule path.
	if ev.AliasUsed != "" {
		t.Errorf("alias_used = %q, want empty — a literal model must not resolve to an alias", ev.AliasUsed)
	}
}

// TestSecondTurnOfALiteralSessionIsNotReclassified is the cost bound: the rule
// from the decision ticket is "every request that is NOT YET part of a session
// gets classified", so turn 2 onward of an ordinary conversation must make no
// classifier call at all.
//
// It fails if classifyLiteral is ever changed to run unconditionally — which
// would put a Jev call on every turn of the most common traffic in this
// deployment.
func TestSecondTurnOfALiteralSessionIsNotReclassified(t *testing.T) {
	matcher, err := classifier.NewRequestMatcher(
		[]types.MatchPattern{{Pattern: "You name chat sessions.", Mode: "prefix"}},
		"", "title", nil, true)
	if err != nil {
		t.Fatalf("NewRequestMatcher: %v", err)
	}
	requestKind := classifier.NewHeuristicClassifierFull(
		"request-kind", classifier.AxisDomain, nil, matcher, nil, 0)
	expensive := &countingClassifier{}

	tr := translator.NewDefaultTranslator()
	p := NewPipeline(
		tr, tr, tr,
		[]classifier.Classifier{requestKind, expensive},
		&signalRecorder{route: primaryRoute()},
		&fakeUpstream{resp: &types.NormalizedResponse{}},
		testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, &capturingWriter{}, nil, nil, nil)

	// An ordinary conversation: a system prompt and an opening user turn, long
	// enough together to derive a session key. A literal model, as both real
	// clients use.
	payload := func(turn string) []byte {
		b, err := json.Marshal(map[string]interface{}{
			"model":      "m-primary",
			"max_tokens": 100,
			"messages": []map[string]string{
				{"role": "system", "content": "You are Hermes Agent, built by Nous Research. Be direct."},
				{"role": "user", "content": turn},
			},
		})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		return b
	}

	if _, err := p.Execute(context.Background(), payload("write me a parser for this log format"), "openai", "t1", ""); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	firstCalls := expensive.calls

	// The same opening turn again — the same derived session key, so the
	// session now exists and is pinned from the first call.
	if _, err := p.Execute(context.Background(), payload("write me a parser for this log format"), "openai", "t2", ""); err != nil {
		t.Fatalf("second Execute: %v", err)
	}

	if firstCalls == 0 {
		t.Fatal("the first request of a session was not classified at all — the literal path never reached classifyLiteral")
	}
	if expensive.calls != firstCalls {
		t.Fatalf("the model-backed classifier ran %d extra times on turn 2, want 0 — "+
			"only the FIRST request of a session is classified", expensive.calls-firstCalls)
	}
}

// signalRecorder is a Router that keeps the signals it was given, so a test can
// assert on what routing actually saw rather than on an intermediate value.
type signalRecorder struct {
	signals types.Signals
	route   types.Route
}

func (r *signalRecorder) Route(_ context.Context, _ *types.NormalizedRequest, sig types.Signals) (types.Route, error) {
	r.signals = sig
	return r.route, nil
}

// countingClassifier counts how many times it was consulted, and fills nothing.
// It stands in for a model-backed classifier: the thing that costs an upstream
// call, and therefore the thing the signature must skip.
type countingClassifier struct{ calls int }

func (c *countingClassifier) Classify(context.Context, *types.NormalizedRequest) (types.Signals, error) {
	c.calls++
	return types.Signals{}, nil
}

// snapshotProbe records what the request looked like when classification ran,
// so a failure names the broken step.
//
// It forwards DecisiveMatch, because the merge's decisive check is a type
// assertion on the classifier itself — a wrapper that does not forward it
// silently turns every decisive signature into a non-decisive one, and the
// only symptom is a model call that should not have happened.
type snapshotProbe struct {
	inner              classifier.Classifier
	systemPrompt       string
	clientSystemPrompt string
}

func (p *snapshotProbe) Classify(ctx context.Context, req *types.NormalizedRequest) (types.Signals, error) {
	p.systemPrompt = req.SystemPrompt
	p.clientSystemPrompt = req.ClientSystemPrompt
	return p.inner.Classify(ctx, req)
}

func (p *snapshotProbe) DecisiveMatch(req *types.NormalizedRequest) bool {
	d, ok := p.inner.(interface {
		DecisiveMatch(*types.NormalizedRequest) bool
	})
	return ok && d.DecisiveMatch(req)
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
