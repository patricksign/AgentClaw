package api

import (
	"net/http"
)

func (s *Server) HandlerHealth(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealthz)
}

// GET /healthz — lightweight health check for Docker/k8s probes.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
