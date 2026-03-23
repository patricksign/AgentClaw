package api

import (
	"github.com/gofiber/fiber/v2"
	"github.com/patricksign/AgentClaw/common"
)

func (s *Server) HandlerHealth(c fiber.Router) {
	GET(c, "/healthz", s.handleHealthz)
}

// GET /healthz
func (s *Server) handleHealthz(c *fiber.Ctx) error {
	return common.ResponseApiOK(c, map[string]string{"status": "ok"}, nil)
}
