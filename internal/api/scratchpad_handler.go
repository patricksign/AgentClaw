package api

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/patricksign/AgentClaw/common"
	"github.com/patricksign/AgentClaw/internal/state"
)

func (s *Server) HandlerScratchpad(c fiber.Router) {
	GET(c, "/scratchpad", s.getScratchpad)
	POST(c, "/scratchpad", s.createScratchpad)
}

// ─── Scratchpad ───────────────────────────────────────────────────────────────

// GET /api/scratchpad
func (s *Server) getScratchpad(c *fiber.Ctx) error {
	if s.scratchpad == nil {
		return common.ResponseApiStatusCode(c, fiber.StatusServiceUnavailable, nil, errors.New("scratchpad not configured"))
	}
	entries, err := s.scratchpad.Read()
	if err != nil {
		return common.ResponseApiStatusCode(c, fiber.StatusInternalServerError, nil, err)
	}
	if entries == nil {
		entries = []state.ScratchpadEntry{}
	}
	md, merr := s.scratchpad.ReadForContext()
	if merr != nil {
		md = ""
	}
	return common.ResponseApiOK(c, map[string]any{
		"markdown": md,
		"entries":  entries,
	}, nil)
}

// POST /api/scratchpad
func (s *Server) createScratchpad(c *fiber.Ctx) error {
	if s.scratchpad == nil {
		return common.ResponseApiStatusCode(c, fiber.StatusServiceUnavailable, nil, errors.New("scratchpad not configured"))
	}
	var req struct {
		AgentID string               `json:"agent_id"`
		Kind    state.ScratchpadKind `json:"kind"`
		Message string               `json:"message"`
		TaskID  string               `json:"task_id"`
	}
	if err := c.BodyParser(&req); err != nil {
		return common.ResponseApiBadRequest(c, nil, errors.New("invalid JSON"))
	}
	if req.AgentID == "" || req.Message == "" {
		return common.ResponseApiBadRequest(c, nil, errors.New("agent_id and message required"))
	}
	switch req.Kind {
	case state.KindInProgress, state.KindBlocked, state.KindDecision,
		state.KindHandoff, state.KindWarning:
		// valid
	default:
		return common.ResponseApiBadRequest(c, nil, errors.New("kind must be one of: in_progress, blocked, decision, handoff, warning"))
	}
	entry := state.ScratchpadEntry{
		AgentID:   req.AgentID,
		Kind:      req.Kind,
		Message:   req.Message,
		TaskID:    req.TaskID,
		Timestamp: time.Now(),
	}
	if err := s.scratchpad.AddEntry(entry); err != nil {
		return common.ResponseApiStatusCode(c, fiber.StatusInternalServerError, nil, err)
	}
	return common.ResponseApiStatusCode(c, fiber.StatusCreated, entry, nil)
}
