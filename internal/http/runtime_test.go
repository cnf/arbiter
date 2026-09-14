package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/cnf/arbiter/internal/logging"
)

// A swap must be visible to requests that start after it — /models should
// advertise the new list, not the one captured at startup.
func TestSwapUpdatesModels(t *testing.T) {
	h := NewHandler(NewRuntime(nil, []Model{{ID: "old", Provider: "p"}}, ""), logging.NewStdoutLogger("error"))
	h.Swap(NewRuntime(nil, []Model{{ID: "new", Provider: "p"}}, ""))

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	resp := httptest.NewRecorder()
	h.ModelsHandler(resp, req)

	var got modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Data) != 1 || got.Data[0].ID != "new" {
		t.Fatalf("models after swap = %+v, want the new list", got.Data)
	}
}

// Holds one runtime snapshot returned by current(), swaps repeatedly, and
// ensures the held snapshot never changes underneath — the guarantee that an
// in-flight request keeps the config it started with. The race detector
// exercises the concurrent store/load path.
func TestCurrentIsStableUnderSwap(t *testing.T) {
	h := NewHandler(NewRuntime(nil, []Model{{ID: "v0", Provider: "p"}}, ""), logging.NewStdoutLogger("error"))

	held := h.current()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			h.Swap(NewRuntime(nil, []Model{{ID: "v1", Provider: "p"}}, ""))
		}
	}()

	for i := 0; i < 1000; i++ {
		if h.current().models[0].ID == "" {
			t.Fatal("current() returned an empty runtime")
		}
	}
	wg.Wait()

	if held.models[0].ID != "v0" {
		t.Fatalf("held snapshot mutated to %q, want v0", held.models[0].ID)
	}
}
