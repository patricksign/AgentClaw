package api

import (
	"net/http"

	"github.com/patricksign/AgentClaw/internal/agent"
)

func (s *Server) HandlerAgent(mux *http.ServeMux) {
	// Agents
	mux.HandleFunc("GET /api/agents", cors(s.handleAgents))
	mux.HandleFunc("POST /api/agents/{id}/restart", cors(s.handleRestartAgent))
	mux.HandleFunc("POST /api/agents/{id}/kill", cors(s.handleKillAgent))
}

// ─── Agents ──────────────────────────────────────────────────────────────────

// GET /api/agents — list all agents + status
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	// Method already enforced by mux pattern "GET /api/agents".
	statuses := s.pool.StatusAll()
	type agentInfo struct {
		ID     string       `json:"id"`
		Status agent.Status `json:"status"`
	}
	out := make([]agentInfo, 0, len(statuses))
	for id, st := range statuses {
		out = append(out, agentInfo{ID: id, Status: st})
	}
	writeJSON(w, 200, out)
}

// POST /api/agents/{id}/restart — restart agent
func (s *Server) handleRestartAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		errJSON(w, http.StatusBadRequest, "missing agent id")
		return
	}
	if err := s.pool.Restart(id); err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarted"})
}

// POST /api/agents/{id}/kill — kill agent
func (s *Server) handleKillAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		errJSON(w, http.StatusBadRequest, "missing agent id")
		return
	}
	if err := s.pool.Kill(id); err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "killed"})
}
