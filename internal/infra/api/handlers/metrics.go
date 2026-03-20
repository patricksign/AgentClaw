package handlers

import (
	"net/http"

	"github.com/patricksign/agentclaw/internal/port"
)

// MetricsHandlers provides HTTP endpoints for metrics and pricing.
type MetricsHandlers struct {
	mem    port.MemoryStore
	router port.LLMRouter
}

// NewMetricsHandlers creates metrics handlers.
func NewMetricsHandlers(mem port.MemoryStore, router port.LLMRouter) *MetricsHandlers {
	return &MetricsHandlers{mem: mem, router: router}
}

// Register mounts metrics routes on the given mux.
func (h *MetricsHandlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/metrics/llm", h.llmMetrics)
}

func (h *MetricsHandlers) llmMetrics(w http.ResponseWriter, r *http.Request) {
	stats := h.router.Stats()
	respondJSON(w, http.StatusOK, stats)
}
