package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

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

// TestRequestsHandlerFiltersAndOrders proves the filter query parameters reach
// the Reader and narrow the result — a filter param that is accepted and
// ignored is the failure a UI's filter controls would hide.
func TestRequestsHandlerFiltersAndOrders(t *testing.T) {
	base := time.Now().UTC()
	h := newTestStatsHandler(t,
		store.Event{TraceID: "t1", Format: "openai", Provider: "a", Model: "old", Ts: base.Add(-2 * time.Hour)},
		store.Event{TraceID: "t2", Format: "openai", Provider: "a", Model: "new", Ts: base, StatusCode: 200},
		store.Event{TraceID: "t3", Format: "openai", Provider: "b", Model: "failed", Ts: base, StatusCode: 500},
	)

	// Newest first, unfiltered: two rows share the newest ts, so the id
	// tiebreaker is what makes their order deterministic.
	resp := httptest.NewRecorder()
	h.RequestsHandler(resp, httptest.NewRequest(http.MethodGet, "/admin/requests", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", resp.Code, resp.Body.String())
	}
	var all []store.RequestRow
	if err := json.Unmarshal(resp.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("len = %d, want 3", len(all))
	}
	if all[2].Model != "old" {
		t.Errorf("last row = %q, want the oldest (old)", all[2].Model)
	}
	if all[0].Model != "failed" || all[1].Model != "new" {
		t.Errorf("same-timestamp order = %q,%q; want failed,new by descending id", all[0].Model, all[1].Model)
	}

	cases := []struct {
		query string
		want  int
	}{
		{"/admin/requests?provider=b", 1},
		{"/admin/requests?errors", 1},
		{"/admin/requests?status=500", 1},
		{"/admin/requests?status=200", 1},
		{"/admin/requests?provider=a&errors", 0},
		{"/admin/requests?limit=1", 1},
		{"/admin/requests?since=1h", 2},
	}
	for _, tc := range cases {
		resp := httptest.NewRecorder()
		h.RequestsHandler(resp, httptest.NewRequest(http.MethodGet, tc.query, nil))
		if resp.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", tc.query, resp.Code)
			continue
		}
		var got []store.RequestRow
		if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
			t.Errorf("%s: decode: %v", tc.query, err)
			continue
		}
		if len(got) != tc.want {
			t.Errorf("%s returned %d rows, want %d", tc.query, len(got), tc.want)
		}
	}
}

// TestRequestsHandlerDefaultsToClientKind proves the request list defaults to
// real client traffic only — a classifier call (or, later, title-gen/subagent)
// must not clutter the view nobody asked to widen. An explicit ?kind= narrows
// or (via "all") removes the default.
func TestRequestsHandlerDefaultsToClientKind(t *testing.T) {
	h := newTestStatsHandler(t,
		store.Event{TraceID: "client-row", Format: "openai", Provider: "a", Model: "m"},
		store.Event{TraceID: "classifier-row", Format: "openai", Provider: "a", Model: "m", Kind: "classifier"},
	)

	cases := []struct {
		query string
		want  int
	}{
		{"/admin/requests", 1},                 // default: client only
		{"/admin/requests?kind=classifier", 1}, // explicit narrow
		{"/admin/requests?kind=all", 2},        // explicit "everything"
	}
	for _, tc := range cases {
		resp := httptest.NewRecorder()
		h.RequestsHandler(resp, httptest.NewRequest(http.MethodGet, tc.query, nil))
		if resp.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", tc.query, resp.Code)
			continue
		}
		var got []store.RequestRow
		if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
			t.Errorf("%s: decode: %v", tc.query, err)
			continue
		}
		if len(got) != tc.want {
			t.Errorf("%s returned %d rows, want %d", tc.query, len(got), tc.want)
		}
	}
}

// TestRequestsHandlerRejectsBadParams proves a malformed status/limit is a 400
// naming the parameter, not a silent fallback to "no filter" — which would
// answer a broken query with plausible-looking unfiltered data.
func TestRequestsHandlerRejectsBadParams(t *testing.T) {
	h := newTestStatsHandler(t)

	for _, q := range []string{
		"/admin/requests?status=abc",
		"/admin/requests?status=99",
		"/admin/requests?status=600",
		"/admin/requests?limit=0",
		"/admin/requests?limit=-3",
		"/admin/requests?limit=abc",
	} {
		resp := httptest.NewRecorder()
		h.RequestsHandler(resp, httptest.NewRequest(http.MethodGet, q, nil))
		if resp.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, resp.Code)
		}
	}
}

// TestRequestsHandlerOnDisabledStoreReturns503 keeps "store off" distinct from
// "no traffic", same as the aggregate endpoints.
func TestRequestsHandlerOnDisabledStoreReturns503(t *testing.T) {
	h := NewStatsHandler(nil, logging.NewStdoutLogger("error"))

	for name, fn := range map[string]http.HandlerFunc{
		"list":   h.RequestsHandler,
		"detail": h.RequestHandler,
	} {
		resp := httptest.NewRecorder()
		fn(resp, httptest.NewRequest(http.MethodGet, "/admin/requests", nil))
		if resp.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503 with the store disabled", name, resp.Code)
		}
	}
}

// TestRequestHandlerDetail proves the detail route reads the {id} the list
// returned, and that bad/missing ids are told apart: 400 for malformed, 404
// for well-formed but absent.
func TestRequestHandlerDetail(t *testing.T) {
	h := newTestStatsHandler(t,
		store.Event{TraceID: "t1", Format: "openai", Provider: "p", Model: "m1",
			Domain: "code_generation", Effort: "hard", ConfigEpoch: "epoch-a",
			Usage:     types.Usage{InputTokens: 10, OutputTokens: 20, CostUSD: 1.5, CacheRead: 5, CacheWrite: 7},
			ToolCalls: []string{"read_file"}},
	)

	// The id comes from the list, so the two endpoints agree on the handle.
	listResp := httptest.NewRecorder()
	h.RequestsHandler(listResp, httptest.NewRequest(http.MethodGet, "/admin/requests", nil))
	var rows []store.RequestRow
	if err := json.Unmarshal(listResp.Body.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("list = %v, %v; want one row", rows, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/requests/1", nil)
	req = mux.SetURLVars(req, map[string]string{"id": strconv.FormatInt(rows[0].ID, 10)})
	resp := httptest.NewRecorder()
	h.RequestHandler(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", resp.Code, resp.Body.String())
	}
	var got store.RequestDetail
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Model != "m1" || got.Effort != "hard" || got.CacheReadTokens != 5 || got.CacheWriteTokens != 7 {
		t.Errorf("detail = %+v, want m1/hard/5/7", got)
	}
	if got.ToolCalls != `["read_file"]` {
		t.Errorf("ToolCalls = %q, want the raw JSON array", got.ToolCalls)
	}

	for id, want := range map[string]int{
		"abc": http.StatusBadRequest,
		"0":   http.StatusBadRequest,
		"-1":  http.StatusBadRequest,
		"999": http.StatusNotFound,
	} {
		req := httptest.NewRequest(http.MethodGet, "/admin/requests/"+id, nil)
		req = mux.SetURLVars(req, map[string]string{"id": id})
		resp := httptest.NewRecorder()
		h.RequestHandler(resp, req)
		if resp.Code != want {
			t.Errorf("id %q: status = %d, want %d", id, resp.Code, want)
		}
	}
}

// TestRequestHandlerIncludesCapturedContent proves the detail endpoint surfaces
// stored blocks, and that a request with no captured content omits the field
// entirely rather than returning an empty array — "capture off" must not read
// the same as "this request had no content".
func TestRequestHandlerIncludesCapturedContent(t *testing.T) {
	h := newTestStatsHandler(t,
		store.Event{TraceID: "t1", Format: "openai", Provider: "p", Model: "m1", StatusCode: 200,
			Content: &store.CapturedContent{
				Request: []store.Block{
					{Kind: "text", Body: []byte("the client's system prompt"), Role: "system", MsgIndex: 0, Position: 0},
					{Kind: "text", Body: []byte("what is the sky"), Role: "user", MsgIndex: 1, Position: 0},
				},
				Response: []store.Block{
					{Kind: "text", Body: []byte("blue"), Role: "assistant", MsgIndex: 0, Position: 0},
				},
			}},
		store.Event{TraceID: "t2", Format: "openai", Provider: "p", Model: "m2", StatusCode: 200},
	)

	listResp := httptest.NewRecorder()
	h.RequestsHandler(listResp, httptest.NewRequest(http.MethodGet, "/admin/requests", nil))
	var rows []store.RequestRow
	if err := json.Unmarshal(listResp.Body.Bytes(), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("list = %v, %v; want 2 rows", rows, err)
	}

	// Find the captured one (ordering is newest first).
	var withContent, without store.RequestRow
	for _, r := range rows {
		if r.Model == "m1" {
			withContent = r
		} else {
			without = r
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/requests/1", nil)
	req = mux.SetURLVars(req, map[string]string{"id": strconv.FormatInt(withContent.ID, 10)})
	resp := httptest.NewRecorder()
	h.RequestHandler(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", resp.Code, resp.Body.String())
	}
	var got store.RequestDetail
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Content) != 3 {
		t.Fatalf("content blocks = %d, want 3: %+v", len(got.Content), got.Content)
	}
	if got.Content[0].Body != "the client's system prompt" || !got.Content[0].Captured {
		t.Errorf("first block = %+v, want the captured system text", got.Content[0])
	}
	if got.Content[0].Hash == "" {
		t.Error("hash is empty; the UI needs the address to group repeats")
	}
	if got.Content[2].Direction != "response" || got.Content[2].Body != "blue" {
		t.Errorf("last block = %+v, want the response body", got.Content[2])
	}

	// The request with no captured content omits the field.
	req = httptest.NewRequest(http.MethodGet, "/admin/requests/2", nil)
	req = mux.SetURLVars(req, map[string]string{"id": strconv.FormatInt(without.ID, 10)})
	resp = httptest.NewRecorder()
	h.RequestHandler(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	if strings.Contains(resp.Body.String(), `"content"`) {
		t.Errorf("body = %s, want no content field for a request with nothing captured", resp.Body.String())
	}
}

// TestRepeatedContentHandler proves the boilerplate-detection endpoint reaches
// the reader and honours min_sessions, which is the parameter that separates a
// client's preamble from one long conversation.
func TestRepeatedContentHandler(t *testing.T) {
	base := time.Now().UTC()
	preamble := store.Block{Kind: "text", Body: []byte("Always answer in Markdown."), Role: "system", MsgIndex: 0, Position: 0}

	var events []store.Event
	for i, session := range []string{"s1", "s2"} {
		events = append(events, store.Event{
			TraceID: "t", Ts: base, SessionKey: session, Format: "openai",
			Provider: "p", Model: "m", StatusCode: 200,
			Content: &store.CapturedContent{Request: []store.Block{
				preamble,
				{Kind: "text", Body: []byte("unique question " + string(rune('a'+i)) + " with padding"),
					Role: "user", MsgIndex: 1, Position: 0},
			}},
		})
	}
	h := newTestStatsHandler(t, events...)

	resp := httptest.NewRecorder()
	h.RepeatedContentHandler(resp, httptest.NewRequest(http.MethodGet, "/admin/content/repeated?since=1h", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", resp.Code, resp.Body.String())
	}
	var got []store.RepeatedContent
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("repeated = %+v, want just the preamble", got)
	}
	if got[0].Requests != 2 || got[0].Sessions != 2 || got[0].Role != "system" {
		t.Errorf("preamble reach = %+v, want 2 requests / 2 sessions / system", got[0])
	}

	// min_sessions above what exists excludes it.
	resp = httptest.NewRecorder()
	h.RepeatedContentHandler(resp, httptest.NewRequest(http.MethodGet, "/admin/content/repeated?since=1h&min_sessions=9", nil))
	var none []store.RepeatedContent
	if err := json.Unmarshal(resp.Body.Bytes(), &none); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("min_sessions=9 returned %+v, want none", none)
	}

	// A malformed threshold is a 400, not a silently ignored parameter.
	resp = httptest.NewRecorder()
	h.RepeatedContentHandler(resp, httptest.NewRequest(http.MethodGet, "/admin/content/repeated?min_sessions=abc", nil))
	if resp.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a malformed min_sessions", resp.Code)
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
		"requests":  h.RequestsHandler,
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
