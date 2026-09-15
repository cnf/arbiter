package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/store"
	"github.com/cnf/arbiter/pkg/types"
)

func usage(input, output int, costUSD float64) types.Usage {
	return types.Usage{InputTokens: input, OutputTokens: output, CostUSD: costUSD}
}

// newTestStatsHandler writes a handful of events through a real SQLiteWriter
// and reopens the file with a Reader, so these tests exercise the actual
// query path rather than a mock.
func newTestStatsHandler(t *testing.T, events ...store.Event) *StatsHandler {
	t.Helper()
	logger := logging.NewStdoutLogger("error")
	path := filepath.Join(t.TempDir(), "events.db")

	w, err := store.NewSQLiteWriter(path, logger)
	if err != nil {
		t.Fatalf("NewSQLiteWriter: %v", err)
	}
	for _, ev := range events {
		w.Record(ev)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reader, err := store.OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	return NewStatsHandler(reader, logger)
}

// TestOverallHandlerReturnsAggregates proves the handler round-trips a real
// Reader query into JSON, not just that it calls something.
func TestOverallHandlerReturnsAggregates(t *testing.T) {
	h := newTestStatsHandler(t,
		store.Event{TraceID: "t1", Format: "openai", Provider: "primary", Model: "m1", StatusCode: 200, Usage: usage(100, 50, 0.01)},
		store.Event{TraceID: "t2", Format: "openai", Provider: "primary", Model: "m1", StatusCode: 500, Usage: usage(200, 80, 0.02)},
	)

	resp := httptest.NewRecorder()
	h.OverallHandler(resp, httptest.NewRequest(http.MethodGet, "/admin/stats", nil))

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", resp.Code, resp.Body.String())
	}
	var got store.OverallStats
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Requests != 2 {
		t.Errorf("Requests = %d, want 2", got.Requests)
	}
	if got.Errors != 1 {
		t.Errorf("Errors = %d, want 1", got.Errors)
	}
}

// TestOverallHandlerOnDisabledStoreReturns503 proves a nil Reader (storage
// disabled) fails loudly and specifically, rather than panicking or silently
// returning zeroes that look like "nothing happened yet".
func TestOverallHandlerOnDisabledStoreReturns503(t *testing.T) {
	h := NewStatsHandler(nil, logging.NewStdoutLogger("error"))

	resp := httptest.NewRecorder()
	h.OverallHandler(resp, httptest.NewRequest(http.MethodGet, "/admin/stats", nil))

	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when the event store is disabled", resp.Code)
	}
}

// TestProvidersHandlerOrdersByCost proves the JSON list preserves the
// Reader's cost ordering end to end.
func TestProvidersHandlerOrdersByCost(t *testing.T) {
	h := newTestStatsHandler(t,
		store.Event{TraceID: "t1", Format: "openai", Provider: "cheap", Model: "small", Usage: usage(10, 5, 0.01)},
		store.Event{TraceID: "t2", Format: "openai", Provider: "pricey", Model: "big", Usage: usage(10, 5, 5.00)},
	)

	resp := httptest.NewRecorder()
	h.ProvidersHandler(resp, httptest.NewRequest(http.MethodGet, "/admin/stats/providers", nil))

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", resp.Code, resp.Body.String())
	}
	var got []store.ProviderStats
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].Provider != "pricey" {
		t.Fatalf("got %+v, want pricey/big sorted first", got)
	}
}

// TestEpochsHandlerGroupsByEpoch proves epoch grouping survives the JSON
// round-trip, including the empty-string group for pre-epoch rows.
func TestEpochsHandlerGroupsByEpoch(t *testing.T) {
	h := newTestStatsHandler(t,
		store.Event{TraceID: "t1", Format: "openai", Provider: "p", Model: "m", ConfigEpoch: "epoch-a", Usage: usage(10, 5, 1)},
		store.Event{TraceID: "t2", Format: "openai", Provider: "p", Model: "m", Usage: usage(10, 5, 2)},
	)

	resp := httptest.NewRecorder()
	h.EpochsHandler(resp, httptest.NewRequest(http.MethodGet, "/admin/stats/epochs", nil))

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", resp.Code, resp.Body.String())
	}
	var got []store.EpochStats
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d epoch groups, want 2", len(got))
	}
}

// TestToolsHandlerCountsUsage proves tool counts survive the handler.
func TestToolsHandlerCountsUsage(t *testing.T) {
	h := newTestStatsHandler(t,
		store.Event{TraceID: "t1", Format: "openai", Provider: "p", Model: "m", ToolCalls: []string{"read_file", "grep"}},
		store.Event{TraceID: "t2", Format: "openai", Provider: "p", Model: "m", ToolCalls: []string{"read_file"}},
	)

	resp := httptest.NewRecorder()
	h.ToolsHandler(resp, httptest.NewRequest(http.MethodGet, "/admin/stats/tools", nil))

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", resp.Code, resp.Body.String())
	}
	var got []store.ToolStat
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	counts := map[string]int64{}
	for _, s := range got {
		counts[s.Tool] = s.Uses
	}
	if counts["read_file"] != 2 || counts["grep"] != 1 {
		t.Fatalf("counts = %+v, want read_file=2 grep=1", counts)
	}
}

// TestEmptyListIsJSONArrayNotNull proves an empty result marshals as [] rather
// than null — a null body reads to an operator as "broken", not "nothing yet".
func TestEmptyListIsJSONArrayNotNull(t *testing.T) {
	h := newTestStatsHandler(t)

	for name, fn := range map[string]http.HandlerFunc{
		"providers": h.ProvidersHandler,
		"epochs":    h.EpochsHandler,
		"tools":     h.ToolsHandler,
	} {
		resp := httptest.NewRecorder()
		fn(resp, httptest.NewRequest(http.MethodGet, "/admin/stats/"+name, nil))
		if got := resp.Body.String(); got != "[]\n" {
			t.Errorf("%s body = %q, want [] for an empty result", name, got)
		}
	}
}

// TestSessionHandlerRequiresKey proves a missing ?key is rejected 400 rather
// than silently querying for an empty session key.
func TestSessionHandlerRequiresKey(t *testing.T) {
	h := newTestStatsHandler(t)

	resp := httptest.NewRecorder()
	h.SessionHandler(resp, httptest.NewRequest(http.MethodGet, "/admin/stats/session", nil))

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when ?key is missing", resp.Code)
	}
}

// TestSessionHandlerReturnsTrajectory proves the ?key/?limit params reach the
// Reader and the trajectory comes back in order.
func TestSessionHandlerReturnsTrajectory(t *testing.T) {
	base := time.Now().UTC()
	h := newTestStatsHandler(t,
		store.Event{TraceID: "t1", Format: "openai", Provider: "p", Model: "first", SessionKey: "s1", Ts: base},
		store.Event{TraceID: "t2", Format: "openai", Provider: "p", Model: "second", SessionKey: "s1", Ts: base.Add(time.Minute)},
	)

	resp := httptest.NewRecorder()
	h.SessionHandler(resp, httptest.NewRequest(http.MethodGet, "/admin/stats/session?key=s1", nil))

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", resp.Code, resp.Body.String())
	}
	var got []store.SessionRequest
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].Model != "first" || got[1].Model != "second" {
		t.Fatalf("got %+v, want [first, second] in order", got)
	}
}
