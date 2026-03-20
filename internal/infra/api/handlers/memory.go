package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/patricksign/agentclaw/internal/port"
)

// MemoryHandlers provides HTTP endpoints for memory operations.
type MemoryHandlers struct {
	mem port.MemoryStore
}

// NewMemoryHandlers creates memory handlers.
func NewMemoryHandlers(mem port.MemoryStore) *MemoryHandlers {
	return &MemoryHandlers{mem: mem}
}

// Register mounts memory routes on the given mux.
func (h *MemoryHandlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/memory/context", h.getContext)
	mux.HandleFunc("POST /api/memory/project", h.appendProjectDoc)
}

func (h *MemoryHandlers) getContext(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	role := r.URL.Query().Get("role")
	title := r.URL.Query().Get("title")
	complexity := r.URL.Query().Get("complexity")

	ctx := h.mem.BuildContext(agentID, role, title, complexity)
	respondJSON(w, http.StatusOK, ctx)
}

func (h *MemoryHandlers) appendProjectDoc(w http.ResponseWriter, r *http.Request) {
	limitBody(w, r)
	var body struct {
		Section string `json:"section"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.mem.AppendProjectDoc(body.Section); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "appended"})
}
