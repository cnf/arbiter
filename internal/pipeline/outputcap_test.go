package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/router"
	"github.com/cnf/arbiter/pkg/types"
)

// maxTokensNormalizer is fakeNormalizer with a client-supplied max_tokens, so
// the "client's own cap wins" path is reachable.
type maxTokensNormalizer struct {
	model     string
	maxTokens int
}

func (maxTokensNormalizer) Detect([]byte) (string, error) { return "openai", nil }
func (n maxTokensNormalizer) ToNormalized(payload []byte, _ string) (*types.NormalizedRequest, error) {
	return &types.NormalizedRequest{
		Model:     n.model,
		MaxTokens: n.maxTokens,
		Messages:  []types.Message{{Role: "user", Content: []types.ContentBlock{types.TextBlock(string(payload))}}},
	}, nil
}

// maxOutputCapProviders is a provider whose model declares a real completion
// limit, mirroring the deployment's `claude/claude-sonnet-5` row
// (max_output_tokens: 128000).
func maxOutputCapProviders() map[string]types.ProviderConfig {
	return map[string]types.ProviderConfig{
		"capped": {Name: "capped", Type: "anthropic", Models: []string{"m-capped", "m-uncapped"}},
	}
}

// capCatalogLookup states a max_output_tokens for m-capped and none for
// m-uncapped, so both the "catalog knows" and "catalog is silent" paths are
// reachable.
func capCatalogLookup() router.CostLatencyLookup {
	limit := 128000
	return router.NewStaticCatalog([]types.ModelCost{
		{Provider: "capped", Model: "m-capped", MaxOutputTokens: &limit},
		{Provider: "capped", Model: "m-uncapped"},
	})
}

// recordingUpstream captures the max_tokens the pipeline actually sent, which
// is the only place the substitution is observable: the translator's own
// default is applied after this point, so asserting on the wire request would
// prove nothing about whether the catalog was consulted.
type recordingUpstream struct {
	gotMaxTokens int
	resp         *types.NormalizedResponse
}

func (u *recordingUpstream) Send(_ context.Context, _ types.Route, req *types.NormalizedRequest) (*types.NormalizedResponse, error) {
	u.gotMaxTokens = req.MaxTokens
	if u.resp == nil {
		return &types.NormalizedResponse{}, nil
	}
	return u.resp, nil
}

func (u *recordingUpstream) SendStream(_ context.Context, _ types.Route, req *types.NormalizedRequest) (<-chan *types.NormalizedStreamEvent, <-chan error, error) {
	u.gotMaxTokens = req.MaxTokens
	ch := make(chan *types.NormalizedStreamEvent, 1)
	ch <- &types.NormalizedStreamEvent{Type: "message_stop"}
	close(ch)
	errCh := make(chan error, 1)
	errCh <- nil
	return ch, errCh, nil
}

// TestExecuteSendsTheModelsRealOutputCap is the regression test for the 4096
// cap. A client that sends no max_tokens (every OpenAI-format client: Hermes,
// opencode) reached Anthropic with the translator's fixed 4096 default, so
// every reply was silently truncated at 4096 tokens. The catalog's
// max_output_tokens is the model's real limit and must be what goes upstream.
//
// Reverting the fix makes this fail on the first assertion — the value is 0,
// not 128000 — which is the right reason: the catalog was never consulted.
func TestExecuteSendsTheModelsRealOutputCap(t *testing.T) {
	up := &recordingUpstream{}
	p := NewPipeline(
		nil, fakeNormalizer{model: "m-capped"}, fakeDenormalizer{},
		nil, &fakeRouter{}, up, maxOutputCapProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, nil, capCatalogLookup(), nil, nil)

	if _, err := p.Execute(context.Background(), []byte("hello"), "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if up.gotMaxTokens != 128000 {
		t.Fatalf("sent max_tokens = %d, want 128000 from the catalog's max_output_tokens", up.gotMaxTokens)
	}
}

// A client-supplied cap is an explicit instruction and must survive: the
// catalog fills a gap, it does not override the caller.
func TestExecuteKeepsAClientSuppliedMaxTokens(t *testing.T) {
	up := &recordingUpstream{}
	p := NewPipeline(
		nil, &maxTokensNormalizer{model: "m-capped", maxTokens: 512}, fakeDenormalizer{},
		nil, &fakeRouter{}, up, maxOutputCapProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, nil, capCatalogLookup(), nil, nil)

	if _, err := p.Execute(context.Background(), []byte("hello"), "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if up.gotMaxTokens != 512 {
		t.Fatalf("sent max_tokens = %d, want the client's 512", up.gotMaxTokens)
	}
}

// A model the catalog says nothing about must not gain an invented cap here.
// It goes out as 0, and only the Anthropic translator substitutes its own
// default — an OpenAI-format upstream correctly sees no cap at all, which is
// what a client that sent none asked for.
func TestExecuteSendsNoCapWhenTheCatalogIsSilent(t *testing.T) {
	up := &recordingUpstream{}
	p := NewPipeline(
		nil, fakeNormalizer{model: "m-uncapped"}, fakeDenormalizer{},
		nil, &fakeRouter{}, up, maxOutputCapProviders(), nil, nil, nil,
		fakeLogger{}, time.Minute, nil, nil, capCatalogLookup(), nil, nil)

	if _, err := p.Execute(context.Background(), []byte("hello"), "openai", "t1", ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if up.gotMaxTokens != 0 {
		t.Fatalf("sent max_tokens = %d, want 0 (no invented cap) when the catalog has no row", up.gotMaxTokens)
	}
}
