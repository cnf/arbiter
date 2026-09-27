package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/store"
	"github.com/cnf/arbiter/internal/ui"
	"github.com/cnf/arbiter/pkg/types"
	"github.com/gorilla/mux"
)

func main() {
	dir, _ := os.MkdirTemp("", "arbiter-preview")
	path := filepath.Join(dir, "preview.db")
	logger := logging.NewStdoutLogger("error")
	w, err := store.NewSQLiteWriter(path, logger)
	if err != nil {
		log.Fatal(err)
	}

	base := time.Now().Add(-2 * time.Hour)
	key := "conv-demo-abcdef1234567890"

	sys := store.Block{MsgIndex: 0, Position: 0, Role: "system", Kind: "text",
		Body: []byte("You are a careful senior engineer pairing with the user. Read files before editing. Match existing style.")}
	sysGuardrailed := store.Block{MsgIndex: 0, Position: 0, Role: "system", Kind: "text",
		Body: []byte("[Arbiter injected preamble]\nYou are a careful senior engineer pairing with the user. Read files before editing. Match existing style.\n[end injected]")}
	q1 := store.Block{MsgIndex: 1, Position: 0, Role: "user", Kind: "text",
		Body: []byte("Can you look at internal/ui/sessions.go and tell me what SessionHandler does, and whether it handles the case where a session key has no turns in the current window?")}
	toolUse := store.Block{MsgIndex: 0, Position: 1, Role: "assistant", Kind: "tool_use",
		Body: []byte(`{"id":"tu_1","name":"terminal","input":{"command":"grep -n 'func (h *Handler) SessionHandler' -A 20 internal/ui/sessions.go"}}`)}
	toolResult := store.Block{MsgIndex: 0, Position: 2, Role: "user", Kind: "tool_result",
		Body: []byte(`{"for_id":"tu_1","content":"208:func (h *Handler) SessionHandler(w http.ResponseWriter, r *http.Request) {\n209:\tif disabled := h.storeDisabled(w, r); disabled \u0026\u0026 fragmentsRequested(r) {\n210:\t\treturn\n211:\t}\n212:\tkey := r.URL.Query().Get(\"key\")\n","is_error":false}`)}
	reply1 := store.Block{MsgIndex: 0, Position: 3, Role: "assistant", Kind: "text",
		Body: []byte("SessionHandler reads the `key` query param, then queries the store for every request under that session key. If there are none, it still returns 200 with an empty-state message rather than a 404 — that matters because a link from an old request can point at a key that has aged out of the retention window.\n\nHere's the relevant excerpt:\n\n```go\nfunc (h *Handler) SessionHandler(w http.ResponseWriter, r *http.Request) {\n    key := r.URL.Query().Get(\"key\")\n    if key == \"\" {\n        h.fail(w, r, http.StatusBadRequest, \"missing required query parameter: key\")\n        return\n    }\n    ...\n}\n```\n\nSo: missing key = 400, unknown/empty key = 200 with an explanation. Want me to add a test for the empty-window case specifically?")}

	q2 := store.Block{MsgIndex: 2, Position: 0, Role: "user", Kind: "text",
		Body: []byte("Yes please, and also check the sibling sessions_test.go for how similar cases are tested.")}
	reply2 := store.Block{MsgIndex: 0, Position: 0, Role: "assistant", Kind: "text",
		Body: []byte("Added TestUnknownSessionKeyIsEmptyNotError — covers both the empty-window case (200, explanatory text) and the missing-key case (400). Ran it against the seeded handler, passes.")}

	w.Record(store.Event{TraceID: "t1", Provider: "anthropic", Model: "claude-opus-4", StatusCode: 200,
		LatencyMs: 4200, Usage: types.Usage{InputTokens: 1850, OutputTokens: 340, CostUSD: 0.0412},
		Ts: base, SessionKey: key, Kind: "client",
		Content: &store.CapturedContent{
			Request:            []store.Block{sys, q1},
			RequestGuardrailed: []store.Block{sysGuardrailed, q1},
			Response:           []store.Block{toolUse, toolResult, reply1},
		},
	})
	w.Record(store.Event{TraceID: "t2", Provider: "anthropic", Model: "claude-opus-4", StatusCode: 200,
		LatencyMs: 2100, Usage: types.Usage{InputTokens: 2400, OutputTokens: 95, CostUSD: 0.0290},
		Ts: base.Add(90 * time.Second), SessionKey: key, Kind: "client",
		Content: &store.CapturedContent{
			Request: []store.Block{sys, q1,
				{MsgIndex: 0, Position: 1, Role: "assistant", Kind: "tool_use", Body: toolUse.Body},
				{MsgIndex: 0, Position: 2, Role: "user", Kind: "tool_result", Body: toolResult.Body},
				{MsgIndex: 0, Position: 3, Role: "assistant", Kind: "text", Body: reply1.Body},
				q2},
			RequestGuardrailed: []store.Block{sysGuardrailed, q1,
				{MsgIndex: 0, Position: 1, Role: "assistant", Kind: "tool_use", Body: toolUse.Body},
				{MsgIndex: 0, Position: 2, Role: "user", Kind: "tool_result", Body: toolResult.Body},
				{MsgIndex: 0, Position: 3, Role: "assistant", Kind: "text", Body: reply1.Body},
				q2},
			Response: []store.Block{reply2},
		},
	})
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}

	reader, err := store.OpenReader(path)
	if err != nil {
		log.Fatal(err)
	}
	h := ui.New(reader, logger)

	r := mux.NewRouter()
	r.HandleFunc("/admin/ui/requests/{id}/guardrail-diff", h.GuardrailDiffHandler).Methods("GET")
	r.HandleFunc("/admin/ui/sessions", h.SessionsHandler).Methods("GET")
	r.HandleFunc("/admin/ui/session", h.SessionHandler).Methods("GET")
	r.HandleFunc("/admin/ui/overview", h.OverviewHandler).Methods("GET")
	r.HandleFunc("/admin/ui/overview/node", h.OverviewNodeHandler).Methods("GET")
	r.HandleFunc("/admin/ui/overview/node/close", h.OverviewNodeCloseHandler).Methods("GET")
	r.HandleFunc("/admin/ui/content/repeated", h.DiscoveryHandler).Methods("GET")
	r.HandleFunc("/admin/ui/content/block", h.BlockRequestsHandler).Methods("GET")
	r.PathPrefix("/admin/ui/static/").HandlerFunc(h.StaticHandler).Methods("GET")

	log.Println("session key:", key)
	log.Println("listening on :8099")
	log.Fatal(http.ListenAndServe("127.0.0.1:8099", r))
}
