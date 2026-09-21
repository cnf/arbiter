package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cnf/arbiter/internal/translator"
	"github.com/cnf/arbiter/pkg/types"
)

// anthropicCaptureServer records the headers and body it received on the
// Anthropic path, then answers with a minimal valid response.
func anthropicCaptureServer(t *testing.T, seen *http.Header, body *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		*body = b
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant",
			"content":[{"type":"text","text":"ok"}],"model":"claude-sonnet-5",
			"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":1}}`))
	}))
}

func anthropicRoute(endpoint string) types.Route {
	return types.Route{
		Provider: "claude",
		Model:    "claude/claude-sonnet-5",
		Config: types.ProviderConfig{
			Name:     "claude",
			Type:     "anthropic",
			Endpoint: endpoint,
			APIKey:   "test-key",
		},
	}
}

// The client's anthropic-beta header must reach the upstream. It is
// per-request negotiation — interleaved thinking is gated on one of these —
// and the outbound body is rebuilt, so without forwarding it a feature the
// client asked Arbiter for is silently disabled in the middle of the chain.
//
// Both send paths are asserted: they build their own requests from the same
// normalized struct, and a fix applied to only one of them is the classic way
// this regresses.
func TestClientBetaHeaderReachesAnthropicUpstream(t *testing.T) {
	const beta = "interleaved-thinking-2025-05-14,effort-2025-11-24"

	t.Run("non-streaming", func(t *testing.T) {
		var seen http.Header
		var body []byte
		srv := anthropicCaptureServer(t, &seen, &body)
		defer srv.Close()

		c := NewHTTPClient(translator.NewDefaultTranslator())
		req := &types.NormalizedRequest{
			Model: "claude/claude-sonnet-5", MaxTokens: 32000, ClientBeta: beta,
		}
		if _, err := c.Send(context.Background(), anthropicRoute(srv.URL), req); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if got := seen.Get("anthropic-beta"); got != beta {
			t.Errorf("anthropic-beta = %q, want %q", got, beta)
		}
	})

	t.Run("streaming", func(t *testing.T) {
		var seen http.Header
		var body []byte
		srv := anthropicCaptureServer(t, &seen, &body)
		defer srv.Close()

		c := NewHTTPClient(translator.NewDefaultTranslator())
		req := &types.NormalizedRequest{
			Model: "claude/claude-sonnet-5", MaxTokens: 32000, Stream: true, ClientBeta: beta,
		}
		// A non-SSE body here is fine: the assertion is on the request, and the
		// read goroutine's outcome is drained via errCh.
		_, errCh, err := c.SendStream(context.Background(), anthropicRoute(srv.URL), req)
		if err != nil {
			t.Fatalf("SendStream: %v", err)
		}
		<-errCh
		if got := seen.Get("anthropic-beta"); got != beta {
			t.Errorf("anthropic-beta = %q, want %q", got, beta)
		}
	})
}

// A client that sent no beta header must not acquire one, and the provider's
// static headers must still be applied — the forwarding must not have replaced
// the config header loop.
func TestNoClientBetaSendsNoBetaHeader(t *testing.T) {
	var seen http.Header
	var body []byte
	srv := anthropicCaptureServer(t, &seen, &body)
	defer srv.Close()

	route := anthropicRoute(srv.URL)
	route.Config.Headers = map[string]string{"X-Static": "configured"}

	c := NewHTTPClient(translator.NewDefaultTranslator())
	req := &types.NormalizedRequest{
		Model: "claude/claude-sonnet-5", MaxTokens: 100,
	}
	if _, err := c.Send(context.Background(), route, req); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := seen.Get("anthropic-beta"); got != "" {
		t.Errorf("anthropic-beta = %q, want absent for a client that sent none", got)
	}
	if got := seen.Get("X-Static"); got != "configured" {
		t.Errorf("provider config header lost: X-Static = %q", got)
	}
	if got := seen.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want the pinned version", got)
	}
}

// The thinking request must be on the outbound BODY too, not only the header.
// This asserts the whole chain end to end against a live-ish server: parse the
// captured client body, normalize, send, and read what the upstream got.
func TestThinkingRequestReachesUpstreamBody(t *testing.T) {
	raw := `{"model":"claude-sonnet","max_tokens":32000,
	         "thinking":{"type":"adaptive"},
	         "output_config":{"effort":"high"},
	         "system":"You are Claude Code.",
	         "messages":[{"role":"user","content":"hi"}]}`

	var seen http.Header
	var body []byte
	srv := anthropicCaptureServer(t, &seen, &body)
	defer srv.Close()

	tr := translator.NewDefaultTranslator()
	norm, err := tr.ToNormalized([]byte(raw), "anthropic")
	if err != nil {
		t.Fatalf("ToNormalized: %v", err)
	}

	c := NewHTTPClient(tr)
	if _, err := c.Send(context.Background(), anthropicRoute(srv.URL), norm); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var sent map[string]interface{}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("upstream body is not JSON: %v\n%s", err, body)
	}
	thinking, ok := sent["thinking"].(map[string]interface{})
	if !ok {
		t.Fatalf("upstream body has no thinking block: %s", body)
	}
	if thinking["type"] != "adaptive" {
		t.Errorf("thinking.type = %v, want adaptive", thinking["type"])
	}
	oc, ok := sent["output_config"].(map[string]interface{})
	if !ok {
		t.Fatalf("upstream body has no output_config: %s", body)
	}
	if oc["effort"] != "high" {
		t.Errorf("output_config.effort = %v, want high", oc["effort"])
	}
	// The cap must still be the client's own, not the translator's default.
	if mt, _ := sent["max_tokens"].(float64); int(mt) != 32000 {
		t.Errorf("max_tokens = %v, want the client's 32000", sent["max_tokens"])
	}
}

// The prompt-cache marker must reach the upstream through BOTH send paths.
// They each build their own HTTP request from the same normalized struct, so a
// fix applied to one is the standard way the other regresses — and the marker
// is the entire feature: without it every Anthropic-format request is a full
// uncached read, which is what #28 is about.
//
// Asserted on the bytes the upstream actually received, from a raw client
// payload, so this cannot pass by Arbiter agreeing with its own structs.
func TestCacheControlMarkerReachesAnthropicUpstream(t *testing.T) {
	const rawBody = `{
      "model": "claude/claude-sonnet-5",
      "max_tokens": 8192,
      "system": "You are a helpful agent.",
      "messages": [{"role": "user", "content": "hello"}]
    }`

	t.Run("non-streaming", func(t *testing.T) {
		var seen http.Header
		var body []byte
		srv := anthropicCaptureServer(t, &seen, &body)
		defer srv.Close()

		tr := translator.NewDefaultTranslator()
		norm, err := tr.ToNormalized([]byte(rawBody), "anthropic")
		if err != nil {
			t.Fatalf("ToNormalized: %v", err)
		}

		c := NewHTTPClient(tr)
		if _, err := c.Send(context.Background(), anthropicRoute(srv.URL), norm); err != nil {
			t.Fatalf("Send: %v", err)
		}
		assertCacheMarkerOnUpstreamBody(t, body)
	})

	t.Run("streaming", func(t *testing.T) {
		var seen http.Header
		var body []byte
		srv := anthropicCaptureServer(t, &seen, &body)
		defer srv.Close()

		tr := translator.NewDefaultTranslator()
		norm, err := tr.ToNormalized([]byte(rawBody), "anthropic")
		if err != nil {
			t.Fatalf("ToNormalized: %v", err)
		}
		norm.Stream = true

		c := NewHTTPClient(tr)
		// A non-SSE body here is fine: the assertion is on the request. The
		// read goroutine's outcome is drained via errCh.
		_, errCh, err := c.SendStream(context.Background(), anthropicRoute(srv.URL), norm)
		if err != nil {
			t.Fatalf("SendStream: %v", err)
		}
		<-errCh
		assertCacheMarkerOnUpstreamBody(t, body)
	})
}

// assertCacheMarkerOnUpstreamBody checks the two markers Arbiter emits — one on
// the system prompt, one on the last message block — are both on the wire, in
// the shape Anthropic documents.
func assertCacheMarkerOnUpstreamBody(t *testing.T, body []byte) {
	t.Helper()

	var sent map[string]interface{}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("upstream body is not JSON: %v\n%s", err, body)
	}

	system, ok := sent["system"].([]interface{})
	if !ok {
		t.Fatalf("system is not the block form, so it cannot carry a cache marker: %s", body)
	}
	if !hasEphemeralMarker(t, system, "system") {
		t.Errorf("no cache_control marker on the system prompt: %s", body)
	}

	messages, _ := sent["messages"].([]interface{})
	if len(messages) == 0 {
		t.Fatalf("no messages on the upstream body: %s", body)
	}
	last, _ := messages[len(messages)-1].(map[string]interface{})
	content, _ := last["content"].([]interface{})
	if !hasEphemeralMarker(t, content, "last message") {
		t.Errorf("no cache_control marker on the last message block: %s", body)
	}
}

func hasEphemeralMarker(t *testing.T, blocks []interface{}, what string) bool {
	t.Helper()

	for _, b := range blocks {
		block, ok := b.(map[string]interface{})
		if !ok {
			continue
		}
		cc, ok := block["cache_control"].(map[string]interface{})
		if !ok {
			continue
		}
		if cc["type"] != types.AnthropicCacheControlEphemeral {
			t.Errorf("%s: cache_control.type = %v, want %q", what, cc["type"], types.AnthropicCacheControlEphemeral)
			continue
		}
		return true
	}
	return false
}
