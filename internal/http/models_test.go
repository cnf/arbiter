package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestModelsHandler(t *testing.T) {
	h := &Handler{
		models: []Model{
			{ID: "gpt-4o", Provider: "openai"},
			{ID: "llama2", Provider: "local"},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	resp := httptest.NewRecorder()
	h.ModelsHandler(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}

	var got modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Object != "list" {
		t.Fatalf("object = %q, want list", got.Object)
	}
	if len(got.Data) != 2 {
		t.Fatalf("got %d models, want 2", len(got.Data))
	}
	if got.Data[0].ID != "gpt-4o" || got.Data[0].OwnedBy != "openai" {
		t.Fatalf("first model = %+v", got.Data[0])
	}
	if got.Data[0].Object != "model" || got.Data[0].Created == 0 {
		t.Fatalf("first model metadata = %+v", got.Data[0])
	}
}
