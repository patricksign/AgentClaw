package api

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/patricksign/AgentClaw/common"
	"github.com/patricksign/AgentClaw/internal/domain"
)

// knownRoles is the canonical set of agent roles used when compress-all is requested.
var knownRoles = []string{
	"idea", "architect", "breakdown",
	"coding", "test", "review",
	"docs", "deploy", "notify",
}

// HandlerState registers the state management endpoints.
func (s *Server) HandlerState(c fiber.Router) {
	POST(c, "/state/compress", s.compressState)
}

func (s *Server) compressState(c *fiber.Ctx) error {
	sum := s.GetSummarizer()
	if sum == nil {
		return common.ResponseApiStatusCode(c, fiber.StatusServiceUnavailable, nil, errors.New("summarizer not configured"))
	}

	// Admin token required — deny by default when ADMIN_TOKEN is not set.
	if !requireAdminTokenFromReq(c) {
		return common.ResponseApiStatusCode(c, fiber.StatusUnauthorized, nil, errors.New("unauthorized"))
	}

	var req struct {
		AgentID string `json:"agent_id"`
		Role    string `json:"role"`
	}
	if err := c.BodyParser(&req); err != nil {
		return common.ResponseApiBadRequest(c, nil, errors.New("invalid JSON"))
	}

	// Use a dedicated context with timeout — NOT c.Context() which is recycled
	// after the handler returns (fasthttp pool). CompressAll can be long-running.
	ctx, cancel := context.WithTimeout(s.Context(), 10*time.Minute)
	defer cancel()

	var totalCost float64
	var totalLen int

	if req.AgentID == "" {
		configs := make([]domain.AgentConfig, 0, len(knownRoles))
		for _, role := range knownRoles {
			configs = append(configs, domain.AgentConfig{ID: role, Role: role})
		}
		cost, err := sum.CompressAll(ctx, configs)
		if err != nil {
			slog.Error("compressState: CompressAll failed", "err", err)
			return common.ResponseApiStatusCode(c, fiber.StatusInternalServerError, nil, errors.New("internal summarizer error"))
		}
		totalCost = cost
	} else {
		role := req.Role
		if role == "" {
			role = req.AgentID
		}
		cost, length, err := sum.CompressAgentHistory(ctx, req.AgentID, role)
		if err != nil {
			slog.Error("compressState: CompressAgentHistory failed", "err", err, "agent_id", req.AgentID)
			return common.ResponseApiStatusCode(c, fiber.StatusInternalServerError, nil, errors.New("internal summarizer error"))
		}
		totalCost = cost
		totalLen = length
	}

	return common.ResponseApiOK(c, map[string]any{
		"cost_usd":       totalCost,
		"summary_length": totalLen,
	}, nil)
}
