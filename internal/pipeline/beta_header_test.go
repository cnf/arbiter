package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/cnf/arbiter/pkg/types"
)

// betaCapturingUpstream records the whole normalized request it was handed, so
// a test can assert on fields the pipeline stamped between ingress and the
// upstream call — not just on what the translator produced.
type betaCapturingUpstream struct {
	got *types.NormalizedRequest
}

func (u *betaCapturingUpstream) Send(_ context.Context, _ types.Route, req *types.NormalizedRequest) (*types.NormalizedResponse, error) {
	u.got = req
	return &types.NormalizedResponse{}, nil
}

func (u *betaCapturingUpstream) SendStream(_ context.Context, _ types.Route, req *types.NormalizedRequest) (<-chan *types.NormalizedStreamEvent, <-chan error, error) {
	u.got = req
	ch := make(chan *types.NormalizedStreamEvent, 1)
	ch <- &types.NormalizedStreamEvent{Type: "message_stop"}
	close(ch)
	errCh := make(chan error, 1)
	errCh <- nil
	return ch, errCh, nil
}

// The client's `anthropic-beta` header must be carried from the inbound HTTP
// request through to the normalized request the upstream is handed. It is
// per-request negotiation — interleaved thinking is gated on one of these — and
// the outbound body is rebuilt, so losing it here disables a feature the
// client asked for, with nothing logged.
//
// This test exists because the earlier revert probe found the hole: with the
// capture removed from Execute, every other test still passed. The translator
// and upstream tests each build their own NormalizedRequest, so neither
// exercises the seam where the header is read off the inbound request.
func TestExecuteCarriesClientBetaToUpstream(t *testing.T) {
	const beta = "interleaved-thinking-2025-05-14,effort-2025-11-24"

	up := &betaCapturingUpstream{}
	p := NewPipeline(
		nil, fakeNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, up, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, nil, nil, nil, nil)

	ctx := WithHeaders(context.Background(), map[string]string{"anthropic-beta": beta})
	if _, err := p.Execute(ctx, []byte("hello"), "anthropic", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if up.got == nil {
		t.Fatal("upstream never called")
	}
	if up.got.ClientBeta != beta {
		t.Errorf("ClientBeta = %q, want %q", up.got.ClientBeta, beta)
	}
}

// Header names are case-insensitive, and captureHeaders preserves the client's
// own casing. A case-sensitive lookup would carry the beta for one client and
// silently drop it for another — a difference that reads as the upstream's
// fault rather than the proxy's.
func TestExecuteCarriesClientBetaRegardlessOfCasing(t *testing.T) {
	const beta = "tool-search-tool-2025-10-19"

	for _, name := range []string{"anthropic-beta", "Anthropic-Beta", "ANTHROPIC-BETA"} {
		t.Run(name, func(t *testing.T) {
			up := &betaCapturingUpstream{}
			p := NewPipeline(
				nil, fakeNormalizer{model: "m-primary"}, fakeDenormalizer{},
				nil, &fakeRouter{}, up, testProviders(), nil, nil, nil,
				fakeLogger{}, time.Minute, nil, nil, nil, nil, nil)

			ctx := WithHeaders(context.Background(), map[string]string{name: beta})
			if _, err := p.Execute(ctx, []byte("hello"), "anthropic", "t1", ""); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if up.got.ClientBeta != beta {
				t.Errorf("header %q: ClientBeta = %q, want %q", name, up.got.ClientBeta, beta)
			}
		})
	}
}

// A request with no beta header must not acquire one, so an ordinary client's
// traffic does not carry a stale negotiation to the upstream.
func TestExecuteLeavesClientBetaEmptyWhenAbsent(t *testing.T) {
	up := &betaCapturingUpstream{}
	p := NewPipeline(
		nil, fakeNormalizer{model: "m-primary"}, fakeDenormalizer{},
		nil, &fakeRouter{}, up, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, nil, nil, nil, nil)

	// Headers present but unrelated: the lookup must not match a near-miss name.
	ctx := WithHeaders(context.Background(), map[string]string{"User-Agent": "opencode/1.0"})
	if _, err := p.Execute(ctx, []byte("hello"), "anthropic", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if up.got.ClientBeta != "" {
		t.Errorf("ClientBeta = %q, want empty", up.got.ClientBeta)
	}
}

// thinkingNormalizer emits a normalized request carrying a thinking request,
// standing in for the translator's output for a client that asked to think.
type thinkingNormalizer struct{}

func (thinkingNormalizer) Detect([]byte) (string, error) { return "anthropic", nil }
func (thinkingNormalizer) ToNormalized(payload []byte, _ string) (*types.NormalizedRequest, error) {
	return &types.NormalizedRequest{
		Model:        "m-primary",
		Messages:     []types.Message{{Role: "user", Content: []types.ContentBlock{types.TextBlock(string(payload))}}},
		Thinking:     &types.AnthropicThinking{Type: "adaptive"},
		OutputEffort: "high",
	}, nil
}

// The client's thinking request must survive the pipeline untouched into the
// upstream request. Execute sits between the translator and the upstream, so a
// field carried correctly at both ends can still be dropped in the middle —
// which is exactly the shape of this bug class, and the reason the assertion
// is made at the upstream boundary rather than on the translator's output.
func TestExecutePreservesThinkingRequest(t *testing.T) {
	up := &betaCapturingUpstream{}
	p := NewPipeline(
		nil, thinkingNormalizer{}, fakeDenormalizer{},
		nil, &fakeRouter{}, up, testProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, nil, nil, nil, nil)

	if _, err := p.Execute(context.Background(), []byte("hello"), "anthropic", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if up.got == nil {
		t.Fatal("upstream never called")
	}
	if up.got.Thinking == nil || up.got.Thinking.Type != "adaptive" {
		t.Errorf("Thinking lost in the pipeline: %+v", up.got.Thinking)
	}
	if up.got.OutputEffort != "high" {
		t.Errorf("OutputEffort = %q, want high", up.got.OutputEffort)
	}
}