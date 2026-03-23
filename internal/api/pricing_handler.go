package api

import (
	"github.com/gofiber/fiber/v2"
	"github.com/patricksign/AgentClaw/common"
	"github.com/patricksign/AgentClaw/internal/llm"
)

func (s *Server) HandlerPricing(c fiber.Router) {
	GET(c, "/pricing", s.handlePricing)
}

// GET /api/pricing
func (s *Server) handlePricing(c *fiber.Ctx) error {
	return common.ResponseApiOK(c, llm.GetPricingTable(), nil)
}
