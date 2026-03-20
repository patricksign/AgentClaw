package handlers

import (
	"net/http"
)

// AgentPoolIface abstracts the agent pool for handler use.
type AgentPoolIface interface {
	ListAgents() []AgentInfo
}

// AgentInfo is a minimal view of an agent for API responses.
type AgentInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Model  string `json:"model"`
	Status string `json:"status"`
}

// AgentHandlers provides HTTP endpoints for agent management.
type AgentHandlers struct {
	pool AgentPoolIface
}

// NewAgentHandlers creates agent handlers.
func NewAgentHandlers(pool AgentPoolIface) *AgentHandlers {
	return &AgentHandlers{pool: pool}
}

// Register mounts agent routes on the given mux.
func (h *AgentHandlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agents", h.listAgents)
}

func (h *AgentHandlers) listAgents(w http.ResponseWriter, r *http.Request) {
	agents := h.pool.ListAgents()
	respondJSON(w, http.StatusOK, agents)
}
