package api

import "net/http"

func (s *Server) HandlerMemory(mux *http.ServeMux) {
	// Memory
	mux.HandleFunc("GET /api/memory/project", cors(s.handleProjectMemoryGet))
	mux.HandleFunc("PATCH /api/memory/project", cors(s.handleProjectMemoryUpdate))
}

// ─── Memory ──────────────────────────────────────────────────────────────────

// GET   /api/memory/project — đọc project.md
func (s *Server) handleProjectMemoryGet(w http.ResponseWriter, _ *http.Request) {
	// Method already enforced by mux pattern "GET /api/memory/project".
	doc := s.mem.ReadProjectDoc()
	writeJSON(w, http.StatusOK, map[string]string{"content": doc})
}

// PATCH /api/memory/project
func (s *Server) handleProjectMemoryUpdate(w http.ResponseWriter, r *http.Request) {
	// Method already enforced by mux pattern "PATCH /api/memory/project".
	var req struct {
		Section string `json:"section"`
	}
	if err := readJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.mem.AppendProjectDoc(req.Section); err != nil {
		errJSON(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
