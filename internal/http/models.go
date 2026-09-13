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
}

// ModelsHandler lists every model declared in the current configuration.
func (h *Handler) ModelsHandler(w http.ResponseWriter, r *http.Request) {
	created := time.Now().Unix()
	data := make([]modelRecord, 0, len(h.models))
	for _, model := range h.models {
		data = append(data, modelRecord{
			ID:      model.ID,
			Object:  "model",
			Created: created,
			OwnedBy: model.Provider,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(modelsResponse{Object: "list", Data: data}); err != nil {
		h.logger.LogError(r.Context(), "error", err, map[string]interface{}{"phase": "encode_models"})
	}
}
