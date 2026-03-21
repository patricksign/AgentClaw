package api

import (
	"crypto/subtle"
	"net/http"
	"os"

	"github.com/patricksign/AgentClaw/internal/agent"
	"github.com/patricksign/AgentClaw/internal/summarizer"
	"github.com/rs/zerolog/log"
)

// knownRoles is the canonical set of agent roles used when compress-all is requested.
var knownRoles = []string{
	"idea", "architect", "breakdown",
	"coding", "test", "review",
	"docs", "deploy", "notify",
}

// HandlerState registers the state management endpoints.
func (s *Server) HandlerState(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/state/compress", cors(s.compressState))
}

// SetSummarizer attaches a Summarizer to the server after construction.
func (s *Server) SetSummarizer(sum *summarizer.Summarizer) {
	s.summarizer = sum
}

func (s *Server) compressState(w http.ResponseWriter, r *http.Request) {
	if s.summarizer == nil {
		errJSON(w, http.StatusServiceUnavailable, "summarizer not configured")
		return
	}

	// Require the admin token when ADMIN_TOKEN env var is set.
	if adminToken := os.Getenv("ADMIN_TOKEN"); adminToken != "" {
		got := r.Header.Get("X-Admin-Token")
		// Constant-time comparison prevents timing side-channel attacks.
		if subtle.ConstantTimeCompare([]byte(got), []byte(adminToken)) != 1 {
			errJSON(w, http.StatusUnauthorized, "unauthorized")
			return
		}
	}

	var req struct {
		AgentID string `json:"agent_id"`
		Role    string `json:"role"`
	}
	if err := readJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	ctx := r.Context()
	var totalCost float64
	var totalLen int

	if req.AgentID == "" {
		// Compress all known roles.
		configs := make([]agent.Config, 0, len(knownRoles))
		for _, role := range knownRoles {
			configs = append(configs, agent.Config{ID: role, Role: role})
		}
		cost, err := s.summarizer.CompressAll(ctx, configs)
		if err != nil {
			log.Error().Err(err).Msg("compressState: CompressAll failed")
			errJSON(w, http.StatusInternalServerError, "internal summarizer error")
			return
		}
		totalCost = cost
	} else {
		// Use role from request; fall back to agent_id if role omitted.
		role := req.Role
		if role == "" {
			role = req.AgentID
		}
		cost, length, err := s.summarizer.CompressAgentHistory(ctx, req.AgentID, role)
		if err != nil {
			log.Error().Err(err).Str("agent_id", req.AgentID).Msg("compressState: CompressAgentHistory failed")
			errJSON(w, http.StatusInternalServerError, "internal summarizer error")
			return
		}
		totalCost = cost
		totalLen = length
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"cost_usd":       totalCost,
		"summary_length": totalLen,
	})
}
