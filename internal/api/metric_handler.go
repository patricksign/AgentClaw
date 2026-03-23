package api

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/patricksign/AgentClaw/common"
)

func (s *Server) HandlerMetric(r fiber.Router) {
	GET(r, "/metrics/today", s.handleMetricsToday)
	GET(r, "/metrics/period", s.handleMetricsPeriod)
}

// ─── Metrics ─────────────────────────────────────────────────────────────────

// GET /api/metrics/today
func (s *Server) handleMetricsToday(c *fiber.Ctx) error {

	today := time.Now().Format("2006-01-02")
	stats, err := s.mem.StatsForPeriod(today)
	if err != nil {
		return common.ResponseApi(c, nil, err)
	}
	return common.ResponseApi(c, stats, nil)
}

// GET /api/metrics/period?from=2026-01-01&to=2026-03-31
func (s *Server) handleMetricsPeriod(c *fiber.Ctx) error { // clean-arch: Port for metrics

	from := c.Query("from")
	to := c.Query("to")
	if from == "" {
		from = time.Now().AddDate(0, -1, 0).Format("2006-01-02")
	}
	if to == "" {
		to = time.Now().Format("2006-01-02")
	}
	stats, err := s.mem.StatsForRange(from, to)
	if err != nil {
		return common.ResponseApi(c, nil, err)
	}
	return common.ResponseApi(c, stats, nil)
}
