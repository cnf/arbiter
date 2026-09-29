package upstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cnf/arbiter/internal/translator"
	"github.com/cnf/arbiter/pkg/types"
)

// wireCaptureServer records what arrived and answers with respond's body.
func wireCaptureServer(t *testing.T, seen *http.Header, path *string, body *[]byte, respond string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Header.Clone()
		*path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		*body = b
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respond))
	}))
}

const openaiOKBody = `{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-x",
	"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
	"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`

const anthropicOKBody = `{"id":"msg_1","type":"message","role":"assistant",
	"content":[{"type":"text","text":"ok"}],"model":"claude-sonnet-5",
	"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":1}}`

// testWireRoute builds a route for one provider type. The credential is a
// literal here rather than shared with the other tests' helpers, so this test
// alone proves which header the credential lands in.
func testWireRoute(providerType, endpoint string) types.Route {
	return types.Route{
		Provider: "prov",
		Model:    "prov/model-x",
		Config: types.ProviderConfig{
			Name:     "prov",
			Type:     providerType,
			Endpoint: endpoint,
			APIKey:   "test-key",
		},
	}
}

func testWireRequest() *types.NormalizedRequest {
	return &types.NormalizedRequest{
		Model:     "prov/model-x",
		MaxTokens: 16,
		Messages: []types.Message{{
			Role:    "user",
			Content: []types.ContentBlock{{Type: "text", Text: "hi"}},
		}},
	}
}

// The OpenAI path must send a bearer token, hit /chat/completions, and carry
// the operator's configured Headers. These were three separate copies of the
// same code before the wireFormat table unified them; this pins the behaviour
// so the unification cannot quietly change what goes on the wire.
//
// "ollama" is OpenAI-compatible and shares the OpenAI entry, so it is
// asserted alongside — if the table ever stops routing it there, this fails.
func TestOpenAIWireRequestShape(t *testing.T) {
	for _, providerType := range []string{"openai", "ollama"} {
		t.Run(providerType, func(t *testing.T) {
			var seen http.Header
			var path string
			var body []byte
			srv := wireCaptureServer(t, &seen, &path, &body, openaiOKBody)
			defer srv.Close()

			c := NewHTTPClient(translator.NewDefaultTranslator())
			route := testWireRoute(providerType, srv.URL)
			route.Config.Headers = map[string]string{"X-Org": "acme"}

			if _, err := c.Send(context.Background(), route, testWireRequest()); err != nil {
				t.Fatalf("Send: %v", err)
			}

			if want := "/chat/completions"; path != want {
				t.Errorf("path = %q, want %q", path, want)
			}
			if got, want := seen.Get("Authorization"), "Bearer test-key"; got != want {
				t.Errorf("Authorization = %q, want %q", got, want)
			}
			if got := seen.Get("X-Org"); got != "acme" {
				t.Errorf("configured header X-Org = %q, want %q", got, "acme")
			}
			if got := seen.Get("anthropic-version"); got != "" {
				t.Errorf("anthropic-version = %q on the OpenAI path, want empty", got)
			}
			if len(body) == 0 {
				t.Error("upstream received an empty body")
			}
		})
	}
}

// The Anthropic path must send x-api-key and the API version, and must hit
// /v1/messages — not the OpenAI path. The two entries in the wireFormat table
// differ in exactly these ways, so a copy/paste slip between them is the
// failure mode worth pinning.
func TestAnthropicWireRequestShape(t *testing.T) {
	var seen http.Header
	var path string
	var body []byte
	srv := wireCaptureServer(t, &seen, &path, &body, anthropicOKBody)
	defer srv.Close()

	c := NewHTTPClient(translator.NewDefaultTranslator())
	route := testWireRoute("anthropic", srv.URL)

	if _, err := c.Send(context.Background(), route, testWireRequest()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if want := "/v1/messages"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if got, want := seen.Get("x-api-key"), "test-key"; got != want {
		t.Errorf("x-api-key = %q, want %q", got, want)
	}
	if got, want := seen.Get("anthropic-version"), "2023-06-01"; got != want {
		t.Errorf("anthropic-version = %q, want %q", got, want)
	}
	if got := seen.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q on the Anthropic path, want empty", got)
	}
	if len(body) == 0 {
		t.Error("upstream received an empty body")
	}
}

// An unknown provider type must fail before any request is sent, with an
// upstream error naming the type — the same answer both the streaming and
// non-streaming entry points gave before the wireFormat table replaced their
// duplicated switch statements.
func TestUnknownProviderTypeFailsOnBothPaths(t *testing.T) {
	c := NewHTTPClient(translator.NewDefaultTranslator())

	t.Run("send", func(t *testing.T) {
		_, err := c.Send(context.Background(), testWireRoute("mystery", "http://127.0.0.1:1"), testWireRequest())
		if err == nil {
			t.Fatal("Send with an unknown provider type returned no error")
		}
	})

	t.Run("stream", func(t *testing.T) {
		req := testWireRequest()
		req.Stream = true
		_, _, err := c.SendStream(context.Background(), testWireRoute("mystery", "http://127.0.0.1:1"), req)
		if err == nil {
			t.Fatal("SendStream with an unknown provider type returned no error")
		}
	})
}
