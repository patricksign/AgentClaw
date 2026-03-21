package api

import (
	"net/http"

	"github.com/patricksign/AgentClaw/internal/llm"
)

func (s *Server) HandlerPricing(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/pricing", cors(s.handlePricing))
}

// GET /api/pricing — returns the loaded pricing table for frontend sync.
func (s *Server) handlePricing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, llm.GetPricingTable())
}
