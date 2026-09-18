package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cnf/arbiter/internal/logging"
)

// Capability advertising. A client gates on this before it sends an attachment:
// seeing no capability metadata it assumes text-only and refuses, without ever
// making a request. The subtle half is the absence rule — a model nothing is
// known about must OMIT the fields, not advertise an empty set, because "we know
// nothing" and "it accepts nothing" are different claims.

func intp(i int) *int { return &i }

// TestModelsAdvertiseInputModalities is the core of the feature: a model with
// known modalities must say so, in a field a client can read.
func TestModelsAdvertiseInputModalities(t *testing.T) {
	h := NewHandler(NewRuntime(nil, []Model{
		{
			ID:              "claude-sonnet-5",
			Provider:        "claude",
			InputModalities: []string{"text", "image", "file"},
			MaxInputTokens:  intp(200000),
			Metadata:        map[string]interface{}{"function_calling": true},
		},
	}, ""), logging.NewStdoutLogger("error"))

	body := serveModels(t, h)

	if !strings.Contains(body, `"input_modalities":["text","image","file"]`) {
		t.Errorf("input_modalities not advertised: %s", body)
	}
	if !strings.Contains(body, `"max_input_tokens":200000`) {
		t.Errorf("max_input_tokens not advertised: %s", body)
	}
	if !strings.Contains(body, `"function_calling":true`) {
		t.Errorf("metadata not carried through: %s", body)
	}
}

// TestModelsOmitUnknownCapabilities is the absence rule, and the one that would
// quietly re-create the original bug if it regressed: a model with no capability
// data must emit no capability fields at all. Emitting `"input_modalities":[]`
// would be read by a client as "accepts nothing".
func TestModelsOmitUnknownCapabilities(t *testing.T) {
	h := NewHandler(NewRuntime(nil, []Model{
		{ID: "mystery", Provider: "p"}, // no capability data
	}, ""), logging.NewStdoutLogger("error"))

	body := serveModels(t, h)

	for _, field := range []string{"input_modalities", "max_input_tokens", "max_output_tokens", "metadata"} {
		if strings.Contains(body, field) {
			t.Errorf("%s present for a model with no capability data; unknown must omit the field, not advertise an empty one:\n%s", field, body)
		}
	}
	// The identity fields are still there — capability data is additive.
	if !strings.Contains(body, `"id":"mystery"`) {
		t.Errorf("identity fields missing: %s", body)
	}
}

// TestModelsDistinguishUnknownFromEmptyList pins the distinction the whole
// design turns on. An empty (non-nil) modality list is a positive claim — "this
// model accepts nothing" — and must serialize differently from an absent one.
func TestModelsDistinguishUnknownFromEmptyList(t *testing.T) {
	h := NewHandler(NewRuntime(nil, []Model{
		{ID: "text-only", Provider: "p", InputModalities: []string{"text"}},
	}, ""), logging.NewStdoutLogger("error"))

	body := serveModels(t, h)

	if !strings.Contains(body, `"input_modalities":["text"]`) {
		t.Errorf("explicit text-only list not advertised: %s", body)
	}
	if strings.Contains(body, `"input_modalities":[]`) {
		t.Errorf("empty modality list advertised, which reads as 'accepts nothing': %s", body)
	}
}

// TestModelsHandlerStillServesIdentity guards the pre-existing contract: adding
// capability fields must not disturb the shape every OpenAI-compatible client
// already parses.
func TestModelsHandlerStillServesIdentity(t *testing.T) {
	h := NewHandler(NewRuntime(nil, []Model{
		{ID: "gpt-4o", Provider: "openai", InputModalities: []string{"text", "image"}},
	}, ""), logging.NewStdoutLogger("error"))

	var got modelsResponse
	if err := json.Unmarshal([]byte(serveModels(t, h)), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Object != "list" || len(got.Data) != 1 {
		t.Fatalf("envelope changed: %+v", got)
	}
	rec := got.Data[0]
	if rec.ID != "gpt-4o" || rec.Object != "model" || rec.OwnedBy != "openai" || rec.Created == 0 {
		t.Errorf("identity fields changed: %+v", rec)
	}
}

func serveModels(t *testing.T, h *Handler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	resp := httptest.NewRecorder()
	h.ModelsHandler(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	return resp.Body.String()
}
