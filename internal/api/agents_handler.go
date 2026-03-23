package api

import (
	"crypto/subtle"
	"errors"
	"log/slog"
	"os"
	"sort"

	"github.com/gofiber/fiber/v2"
	"github.com/patricksign/AgentClaw/common"
	"github.com/patricksign/AgentClaw/internal/adapter"
)

func (s *Server) HandlerAgent(c fiber.Router) {
	GET(c, "/agents", s.handleAgents)
	POST(c, "/agents/:id/restart", s.handleRestartAgent)
	POST(c, "/agents/:id/kill", s.handleKillAgent)
}

// ─── Agents ──────────────────────────────────────────────────────────────────

// GET /api/agents — list all agents + status
func (s *Server) handleAgents(r *fiber.Ctx) error {
	statuses := s.pool.StatusAll()
	type agentInfo struct {
		ID     string         `json:"id"`
		Status adapter.Status `json:"status"`
	}
	out := make([]agentInfo, 0, len(statuses))
	for id, st := range statuses {
		out = append(out, agentInfo{ID: id, Status: st})
	}
	// Sort by ID for deterministic API response.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return common.ResponseApiStatusCode(r, fiber.StatusOK, out, nil)
}

// requireAdminTokenFromReq checks the X-Admin-Token header against the ADMIN_TOKEN env var.
// Returns false (denied) when ADMIN_TOKEN is not set — secure by default.
func requireAdminTokenFromReq(r *fiber.Ctx) bool {
	adminToken := os.Getenv("ADMIN_TOKEN")
	if adminToken == "" {
		slog.Warn("ADMIN_TOKEN not set — admin endpoint denied by default")
		return false
	}
	got := r.Get("X-Admin-Token")
	if subtle.ConstantTimeCompare([]byte(got), []byte(adminToken)) != 1 {
		slog.Warn("unauthorized admin request — invalid X-Admin-Token")
		return false
	}
	return true
}

// POST /api/agents/{id}/restart — restart agent (requires admin token)
func (s *Server) handleRestartAgent(r *fiber.Ctx) error {
	if !requireAdminTokenFromReq(r) {
		return common.ResponseApiStatusCode(r, fiber.StatusUnauthorized, nil, errors.New("unauthorized"))
	}
	id := r.Params("id")
	if id == "" {
		return common.ResponseApiBadRequest(r, nil, errors.New("missing agent id"))
	}
	if err := s.pool.Restart(id); err != nil {
		return common.ResponseApiBadRequest(r, nil, err)
	}
	return common.ResponseApiOK(r, map[string]string{"status": "restarted"}, nil)
}

// POST /api/agents/{id}/kill — kill agent (requires admin token)
func (s *Server) handleKillAgent(r *fiber.Ctx) error {
	if !requireAdminTokenFromReq(r) {
		return common.ResponseApiStatusCode(r, fiber.StatusUnauthorized, nil, errors.New("unauthorized"))
	}
	id := r.Params("id")
	if id == "" {
		return common.ResponseApiBadRequest(r, nil, errors.New("missing agent id"))
	}
	if err := s.pool.Kill(id); err != nil {
		return common.ResponseApiBadRequest(r, nil, err)
	}
	return common.ResponseApiOK(r, map[string]string{"status": "killed"}, nil)
}
