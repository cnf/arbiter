package http

import (
	"encoding/json"
	"net/http"
	"time"
)

// modelsResponse follows the OpenAI model-list shape, which is also what
// OpenAI-compatible clients expect from /models and /v1/models.
type modelsResponse struct {
	Object string        `json:"object"`
	Data   []modelRecord `json:"data"`
}

type modelRecord struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`

	// Capability fields. Emitted only when known: a model with no catalog row
	// (or one whose upstream source stated nothing) omits them entirely rather
	// than advertising an empty set, because "we know nothing about this
	// model" and "this model accepts nothing" are different statements and only
	// one of them is true.
	//
	// This is what a client gates on before sending an attachment: seeing no
	// capability metadata, a client assumes the model is text-only and refuses
	// to send an image without ever making a request.
	InputModalities []string               `json:"input_modalities,omitempty"`
	MaxInputTokens  *int                   `json:"max_input_tokens,omitempty"`
	MaxOutputTokens *int                   `json:"max_output_tokens,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
}

// ModelsHandler lists every model declared in the current configuration.
func (h *Handler) ModelsHandler(w http.ResponseWriter, r *http.Request) {
	rt := h.current()
	created := time.Now().Unix()
	data := make([]modelRecord, 0, len(rt.models))
	for _, model := range rt.models {
		data = append(data, modelRecord{
			ID:              model.ID,
			Object:          "model",
			Created:         created,
			OwnedBy:         model.Provider,
			InputModalities: model.InputModalities,
			MaxInputTokens:  model.MaxInputTokens,
			MaxOutputTokens: model.MaxOutputTokens,
			Metadata:        model.Metadata,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(modelsResponse{Object: "list", Data: data}); err != nil {
		h.logger.LogError(r.Context(), "error", err, map[string]interface{}{"phase": "encode_models"})
	}
}
