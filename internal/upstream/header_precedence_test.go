package upstream

import (
	"context"
	"net/http"
	"testing"

	"github.com/cnf/arbiter/internal/translator"
	"github.com/cnf/arbiter/pkg/types"
)

// A provider's static `headers:` map must not be able to mask a header the
// client negotiated. Regression test for #38.
//
// The bug was an ordering inversion, not a missing feature: the client's
// `anthropic-beta` was forwarded correctly, but written *before* the
// `route.Config.Headers` loop, so a config entry of the same name overwrote it
// and `Header.Set` made the config win silently. Anthropic gates interleaved
// thinking on that header, so the symptom was a capability that quietly
// disappeared mid-chain with nothing logged.
//
// The existing beta tests pass with the bug present, because the route they
// build carries no `Config.Headers` at all — the conflict case had no coverage.
// That is the case this file adds.
func TestClientBetaWinsOverProviderStaticHeader(t *testing.T) {
	const (
		clientBeta = "interleaved-thinking-2025-05-14"
		configBeta = "config-pinned-beta-value"
	)

	t.Run("non-streaming", func(t *testing.T) {
		var seen http.Header
		var body []byte
		srv := anthropicCaptureServer(t, &seen, &body)
		defer srv.Close()

		route := anthropicRoute(srv.URL)
		route.Config.Headers = map[string]string{"anthropic-beta": configBeta}

		c := NewHTTPClient(translator.NewDefaultTranslator())
		req := &types.NormalizedRequest{
			Model: "claude/claude-sonnet-5", MaxTokens: 32000, ClientBeta: clientBeta,
		}
		if _, err := c.Send(context.Background(), route, req); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if got := seen.Get("anthropic-beta"); got != clientBeta {
			t.Errorf("anthropic-beta = %q, want the client's %q — a static config value must not mask it",
				got, clientBeta)
		}
	})

	t.Run("streaming", func(t *testing.T) {
		var seen http.Header
		var body []byte
		srv := anthropicCaptureServer(t, &seen, &body)
		defer srv.Close()

		route := anthropicRoute(srv.URL)
		route.Config.Headers = map[string]string{"anthropic-beta": configBeta}

		c := NewHTTPClient(translator.NewDefaultTranslator())
		req := &types.NormalizedRequest{
			Model: "claude/claude-sonnet-5", MaxTokens: 32000, Stream: true, ClientBeta: clientBeta,
		}
		// A non-SSE body is fine here: the assertion is on the request, and the
		// read goroutine's outcome is drained via errCh, as in the sibling test.
		_, errCh, err := c.SendStream(context.Background(), route, req)
		if err != nil {
			t.Fatalf("SendStream: %v", err)
		}
		<-errCh
		if got := seen.Get("anthropic-beta"); got != clientBeta {
			t.Errorf("anthropic-beta = %q, want the client's %q on the streaming path too",
				got, clientBeta)
		}
	})
}

// The ordering fix must not go too far: with no client beta, the provider's
// static value still has to reach the upstream. "Config is overridden by the
// client" is not "config is dropped".
func TestProviderStaticBetaStillAppliesWithoutClientBeta(t *testing.T) {
	const configBeta = "config-pinned-beta-value"

	var seen http.Header
	var body []byte
	srv := anthropicCaptureServer(t, &seen, &body)
	defer srv.Close()

	route := anthropicRoute(srv.URL)
	route.Config.Headers = map[string]string{"anthropic-beta": configBeta}

	c := NewHTTPClient(translator.NewDefaultTranslator())
	req := &types.NormalizedRequest{Model: "claude/claude-sonnet-5", MaxTokens: 32000}
	if _, err := c.Send(context.Background(), route, req); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := seen.Get("anthropic-beta"); got != configBeta {
		t.Errorf("anthropic-beta = %q, want the provider's static %q", got, configBeta)
	}
}

// A provider's `headers:` map must still apply to every other header — the
// precedence rule is about the one header the client negotiates, not a licence
// for config to be ignored.
func TestProviderHeadersStillApplyToOtherHeaders(t *testing.T) {
	var seen http.Header
	var body []byte
	srv := anthropicCaptureServer(t, &seen, &body)
	defer srv.Close()

	route := anthropicRoute(srv.URL)
	route.Config.Headers = map[string]string{
		"x-routing-hint":    "eu-west",
		"anthropic-version": "2024-01-01",
	}

	c := NewHTTPClient(translator.NewDefaultTranslator())
	req := &types.NormalizedRequest{
		Model: "claude/claude-sonnet-5", MaxTokens: 32000,
		ClientBeta: "interleaved-thinking-2025-05-14",
	}
	if _, err := c.Send(context.Background(), route, req); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// A config header with no client equivalent applies verbatim.
	if got := seen.Get("x-routing-hint"); got != "eu-west" {
		t.Errorf("x-routing-hint = %q, want the config value applied", got)
	}
	// anthropic-version is NOT client-negotiated, so config still wins for it.
	// This is the per-header half of the rule: the same ordering must not make
	// every config header lose to a client that never sends one.
	if got := seen.Get("anthropic-version"); got != "2024-01-01" {
		t.Errorf("anthropic-version = %q, want the provider's pin — no client negotiates this header", got)
	}
	// And no header is ever sent empty. Get canonicalizes the key, so this
	// also catches a wrongly-cased literal.
	if got := seen.Get("anthropic-beta"); got == "" {
		if len(seen.Values("anthropic-beta")) > 0 {
			t.Error("anthropic-beta present but empty — should be absent, not empty-sent")
		}
	}
}

// The config's `anthropic-version` pin wins over the built-in default, which is
// what makes a per-header rule meaningful rather than one global ordering.
func TestProviderCanPinAnthropicVersion(t *testing.T) {
	var seen http.Header
	var body []byte
	srv := anthropicCaptureServer(t, &seen, &body)
	defer srv.Close()

	route := anthropicRoute(srv.URL)
	route.Config.Headers = map[string]string{"anthropic-version": "2025-01-01"}

	c := NewHTTPClient(translator.NewDefaultTranslator())
	req := &types.NormalizedRequest{Model: "claude/claude-sonnet-5", MaxTokens: 32000}
	if _, err := c.Send(context.Background(), route, req); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := seen.Get("anthropic-version"); got != "2025-01-01" {
		t.Errorf("anthropic-version = %q, want the provider's pin to override the default", got)
	}
	// And with no config entry, the built-in default is still there.
	var seen2 http.Header
	var body2 []byte
	srv2 := anthropicCaptureServer(t, &seen2, &body2)
	defer srv2.Close()

	c2 := NewHTTPClient(translator.NewDefaultTranslator())
	if _, err := c2.Send(context.Background(), anthropicRoute(srv2.URL), req); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := seen2.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want the built-in default when config sets none", got)
	}
}
