package handlers

import (
	"net/http"

	"github.com/patricksign/AgentClaw/internal/port"
)

// StateHandlers provides HTTP endpoints for agent state inspection.
type StateHandlers struct {
	state  port.StateStore
	router port.LLMRouter
}

// NewStateHandlers creates state handlers.
func NewStateHandlers(state port.StateStore, router port.LLMRouter) *StateHandlers {
	return &StateHandlers{state: state, router: router}
}

// Register mounts state routes on the given mux.
func (h *StateHandlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/state", h.listStates)
	mux.HandleFunc("GET /api/state/{agentID}", h.getState)
	mux.HandleFunc("GET /api/llm/stats", h.llmStats)
}

func (h *StateHandlers) listStates(w http.ResponseWriter, r *http.Request) {
	states, err := h.state.ReadAllStates()
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, states)
}

func (h *StateHandlers) getState(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentID")
	if agentID == "" {
		respondError(w, http.StatusBadRequest, "agentID is required")
		return
	}
	st, err := h.state.ReadState(agentID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if st == nil {
		respondError(w, http.StatusNotFound, "agent state not found")
		return
	}
	respondJSON(w, http.StatusOK, st)
}

func (h *StateHandlers) llmStats(w http.ResponseWriter, r *http.Request) {
	stats := h.router.Stats()
	respondJSON(w, http.StatusOK, stats)
}
