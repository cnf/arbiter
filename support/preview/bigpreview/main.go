package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/store"
	"github.com/cnf/arbiter/internal/ui"
	"github.com/cnf/arbiter/pkg/types"
	"github.com/gorilla/mux"
)

func main() {
	dir, _ := os.MkdirTemp("", "arbiter-bigpreview")
	path := filepath.Join(dir, "preview.db")
	logger := logging.NewStdoutLogger("error")
	w, err := store.NewSQLiteWriter(path, logger)
	if err != nil {
		log.Fatal(err)
	}

	base := time.Now().Add(-3 * time.Hour)
	key := "conv-big-abcdef1234567890"

	sys := store.Block{MsgIndex: 0, Position: 0, Role: "system", Kind: "text",
		Body: []byte("You are a careful senior engineer pairing with the user.")}

	// Build up a growing request-side history across 40 turns, so this
	// looks like a real long coding session with lots of rows in the list.
	var history []store.Block
	history = append(history, sys)

	for i := 1; i <= 40; i++ {
		q := store.Block{MsgIndex: i, Position: 0, Role: "user", Kind: "text",
			Body: []byte(fmt.Sprintf("Turn %d: please look at file_%d.go and tell me about function Handle%d, considering how it interacts with the rest of the request pipeline and whether error handling is consistent with sibling functions.", i, i, i))}
		history = append(history, q)

		toolUse := store.Block{MsgIndex: 0, Position: 1, Role: "assistant", Kind: "tool_use",
			Body: []byte(fmt.Sprintf(`{"id":"tu_%d","name":"terminal","input":{"command":"grep -n 'func Handle%d' -A 20 file_%d.go"}}`, i, i, i))}
		toolResult := store.Block{MsgIndex: 0, Position: 2, Role: "user", Kind: "tool_result",
			Body: []byte(strings.Repeat(fmt.Sprintf("line output for turn %d ", i), 50))}
		reply := store.Block{MsgIndex: 0, Position: 3, Role: "assistant", Kind: "text",
			Body: []byte(strings.Repeat(fmt.Sprintf("Detailed explanation for turn %d covering the function's behavior in depth. ", i), 20))}

		reqBlocks := make([]store.Block, len(history))
		copy(reqBlocks, history)

		w.Record(store.Event{
			TraceID: fmt.Sprintf("t%d", i), Provider: "anthropic", Model: "claude-opus-4", StatusCode: 200,
			LatencyMs: int64(1000 + i*137), Usage: types.Usage{InputTokens: int(1000 + i*80), OutputTokens: int(100 + i*10), CostUSD: 0.001 * float64(i)},
			Ts: base.Add(time.Duration(i) * 90 * time.Second), SessionKey: key, Kind: "client",
			Content: &store.CapturedContent{
				Request:  reqBlocks,
				Response: []store.Block{toolUse, toolResult, reply},
			},
		})

		// Extend history with this turn's assistant-side content so the next
		// turn's request-side resend includes it (real conversation growth).
		history = append(history,
			store.Block{MsgIndex: 0, Position: 1, Role: "assistant", Kind: "tool_use", Body: toolUse.Body},
			store.Block{MsgIndex: 0, Position: 2, Role: "user", Kind: "tool_result", Body: toolResult.Body},
			store.Block{MsgIndex: 0, Position: 3, Role: "assistant", Kind: "text", Body: reply.Body},
		)
	}

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
	log.Println("listening on :8098")
	log.Fatal(http.ListenAndServe("127.0.0.1:8098", r))
}
