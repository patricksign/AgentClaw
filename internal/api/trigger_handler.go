package api

import (
	"errors"
	"log/slog"

	"github.com/gofiber/fiber/v2"
	"github.com/patricksign/AgentClaw/common"
)

func (s *Server) HandlerTrigger(c fiber.Router) {
	POST(c, "/trigger", s.handleTrigger)
}

// ─── Trigger ─────────────────────────────────────────────────────────────────

// POST /api/trigger
func (s *Server) handleTrigger(c *fiber.Ctx) error {
	var req struct {
		WorkspaceID string `json:"workspace_id"`
		TicketID    string `json:"ticket_id"`
	}
	if err := c.BodyParser(&req); err != nil {
		return common.ResponseApiBadRequest(c, nil, errors.New("invalid JSON"))
	}
	if req.WorkspaceID == "" || req.TicketID == "" {
		return common.ResponseApiBadRequest(c, nil, errors.New("workspace_id and ticket_id are required"))
	}

	svc := s.GetTriggerService()
	if svc == nil || !svc.IsConfigured() {
		return common.ResponseApiStatusCode(c, fiber.StatusServiceUnavailable, nil, errors.New("Trello integration not configured (TRELLO_KEY/TRELLO_TOKEN missing)"))
	}

	// Acquire semaphore slot — reject if at capacity.
	select {
	case s.pipelineSem <- struct{}{}:
	default:
		return common.ResponseApiStatusCode(c, fiber.StatusTooManyRequests, nil, errors.New("too many concurrent pipelines, try again later"))
	}

	// Track goroutine in wg so Shutdown() waits for in-flight pipelines (#62).
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.pipelineSem }()
		if err := svc.Run(s.Context(), req.WorkspaceID, req.TicketID); err != nil {
			slog.Error("trigger pipeline failed", "err", err, "workspace_id", req.WorkspaceID, "ticket_id", req.TicketID)
		}
	}()

	return common.ResponseApiStatusCode(c, fiber.StatusAccepted, map[string]string{
		"status":       "accepted",
		"workspace_id": req.WorkspaceID,
		"ticket_id":    req.TicketID,
	}, nil)
}
