package httpserver

import (
	"context"
	"net/http"
	"time"

	"hass-actron/internal/ac"
)

// New builds the HTTP server with all routes registered.
func New(registry *ac.Registry, forwardToOriginal bool) *http.Server {
	mux := http.NewServeMux()

	h := &handler{
		registry:          registry,
		forwardToOriginal: forwardToOriginal,
	}

	// Route 1: GET /commands (long-poll)
	mux.HandleFunc("GET /rest/{version}/block/{device}/commands", h.commands)

	// Route 2: POST /data
	mux.HandleFunc("POST /rest/{version}/block/{device}/data", h.data)

	// Route 3: GET /activate (proxy)
	mux.HandleFunc("GET /rest/{version}/block/{device}/activate", h.activate)

	// Route 4: DELETE /block (proxy)
	mux.HandleFunc("DELETE /rest/{version}/block/{device}", h.deleteBlock)

	// Route 5: POST /usage/log
	mux.HandleFunc("POST /usage/log", h.usageLog)

	// Route 6: GET /v0/AConnect
	mux.HandleFunc("GET /v0/AConnect", h.aconnect)

	// Route 7: GET /
	mux.HandleFunc("GET /", h.root)

	// Route 8: GET /status
	mux.HandleFunc("GET /status", h.status)

	return &http.Server{
		Addr:    ":180",
		Handler: mux,
	}
}

type handler struct {
	registry          *ac.Registry
	forwardToOriginal bool
}

// setCORSHeaders sets the three CORS headers used by device routes.
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Authorization, Content-Length, X-Requested-With, X-Ninja-Token")
	w.Header().Set("Access-Control-Allow-Methods", "GET,PUT,POST,DELETE,OPTIONS")
	w.Header().Set("Access-Control-Allow-Origin", "*")
}

// Shutdown gracefully stops the server.
func Shutdown(srv *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}
