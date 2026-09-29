package main

import (
	"log"
	"net/http"

	"github.com/cnf/arbiter/internal/logging"
	"github.com/cnf/arbiter/internal/store"
	"github.com/cnf/arbiter/internal/ui"
	"github.com/gorilla/mux"
)

// realpreview serves the UI read-only against the real production DB, for
// visual verification only — no writes, no port shared with the real
// binary, throwaway process killed at the end of the check.
func main() {
	reader, err := store.OpenReader("build/livecheck.db")
	if err != nil {
		log.Fatal(err)
	}
	logger := logging.NewStdoutLogger("error")
	h := ui.New(reader, logger)

	r := mux.NewRouter()
	r.HandleFunc("/admin/ui/requests/{id}/guardrail-diff", h.GuardrailDiffHandler).Methods("GET")
	r.HandleFunc("/admin/ui/sessions", h.SessionsHandler).Methods("GET")
	r.HandleFunc("/admin/ui/sessions/tail", h.SessionsTailHandler).Methods("GET")
	r.HandleFunc("/admin/ui/session", h.SessionHandler).Methods("GET")
	r.HandleFunc("/admin/ui/overview", h.OverviewHandler).Methods("GET")
	r.HandleFunc("/admin/ui/overview/node", h.OverviewNodeHandler).Methods("GET")
	r.HandleFunc("/admin/ui/overview/node/close", h.OverviewNodeCloseHandler).Methods("GET")
	r.HandleFunc("/admin/ui/content/repeated", h.DiscoveryHandler).Methods("GET")
	r.HandleFunc("/admin/ui/content/block", h.BlockRequestsHandler).Methods("GET")
	r.PathPrefix("/admin/ui/static/").HandlerFunc(h.StaticHandler).Methods("GET")

	log.Println("listening on :8097 (READ-ONLY against real DB)")
	log.Fatal(http.ListenAndServe("127.0.0.1:8097", r))
}
