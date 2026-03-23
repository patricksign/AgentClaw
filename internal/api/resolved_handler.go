package api

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/patricksign/AgentClaw/common"
	"github.com/patricksign/AgentClaw/internal/state"
)

func (s *Server) HandlerResolved(c fiber.Router) {
	GET(c, "/state/resolved", s.getHandleResolved)
	GET(c, "/api/state/resolved/:id", s.handleResolvedItemById)
	PUT(c, "/api/state/resolved/:id/resolve", s.updateResolvedItemById)
}

// ─── Resolved error patterns ─────────────────────────────────────────────────

// GET /api/state/resolved
func (s *Server) getHandleResolved(c *fiber.Ctx) error {
	if s.resolved == nil {
		return common.ResponseApiOK(c, []state.ErrorPattern{}, nil)
	}
	patterns, err := s.resolved.LoadAll()
	if err != nil {
		return common.ResponseApiStatusCode(c, fiber.StatusInternalServerError, nil, err)
	}
	if patterns == nil {
		patterns = []state.ErrorPattern{}
	}
	return common.ResponseApiOK(c, patterns, nil)
}

// GET /api/state/resolved/:id
func (s *Server) handleResolvedItemById(c *fiber.Ctx) error {
	id := c.Params("id")
	if !isValidResolvedID(id) {
		return common.ResponseApiBadRequest(c, nil, errors.New("invalid pattern id"))
	}
	if s.resolved == nil {
		return common.ResponseApiStatusCode(c, fiber.StatusServiceUnavailable, nil, errors.New("resolved store not configured"))
	}
	detail, err := s.resolved.LoadDetail(id)
	if err != nil {
		return common.ResponseApiStatusCode(c, fiber.StatusNotFound, nil, err)
	}
	return common.ResponseApiOK(c, map[string]string{"id": id, "detail": detail}, nil)
}

// PUT /api/state/resolved/:id/resolve
func (s *Server) updateResolvedItemById(c *fiber.Ctx) error {
	id := c.Params("id")
	if !isValidResolvedID(id) {
		return common.ResponseApiBadRequest(c, nil, errors.New("invalid pattern id"))
	}
	if s.resolved == nil {
		return common.ResponseApiStatusCode(c, fiber.StatusServiceUnavailable, nil, errors.New("resolved store not configured"))
	}
	if err := s.resolved.MarkResolved(id); err != nil {
		return common.ResponseApiStatusCode(c, fiber.StatusNotFound, nil, err)
	}
	return common.ResponseApiOK(c, map[string]string{"status": "resolved"}, nil)
}

// isValidResolvedID reports whether id is a valid 6-hex-character pattern ID.
func isValidResolvedID(id string) bool {
	if len(id) != 6 {
		return false
	}
	for _, ch := range id {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return false
		}
	}
	return true
}
