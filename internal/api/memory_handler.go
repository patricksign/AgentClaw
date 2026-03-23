package api

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/patricksign/AgentClaw/common"
)

func (s *Server) HandlerMemory(c fiber.Router) {
	GET(c, "/memory/project", s.handleProjectMemoryGet)
	PUT(c, "/memory/project", s.handleProjectMemoryUpdate)
}

// ─── Memory ──────────────────────────────────────────────────────────────────

// GET /api/memory/project
func (s *Server) handleProjectMemoryGet(c *fiber.Ctx) error {
	doc := s.mem.ReadProjectDoc()
	return common.ResponseApiOK(c, map[string]string{"content": doc}, nil)
}

// PUT /api/memory/project
func (s *Server) handleProjectMemoryUpdate(c *fiber.Ctx) error {
	var req struct {
		Section string `json:"section"`
	}
	if err := c.BodyParser(&req); err != nil {
		return common.ResponseApiBadRequest(c, nil, errors.New("invalid JSON"))
	}
	if err := s.mem.AppendProjectDoc(req.Section); err != nil {
		return common.ResponseApiStatusCode(c, fiber.StatusInternalServerError, nil, errors.New("internal error"))
	}
	return common.ResponseApiOK(c, map[string]string{"status": "updated"}, nil)
}
