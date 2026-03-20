package handlers

import (
	"context"
	"encoding/json"
	"net/http"
)

// PipelineIface abstracts the pipeline service.
type PipelineIface interface {
	Run(ctx context.Context, boardID, ticketID string) error
	IsConfigured() bool
}

// TriggerHandlers provides HTTP endpoints for pipeline triggers.
type TriggerHandlers struct {
	pipeline PipelineIface
	ctx      context.Context // server-scoped context for background goroutines
}

// NewTriggerHandlers creates trigger handlers.
func NewTriggerHandlers(pipeline PipelineIface, serverCtx context.Context) *TriggerHandlers {
	return &TriggerHandlers{pipeline: pipeline, ctx: serverCtx}
}

// Register mounts trigger routes on the given mux.
func (h *TriggerHandlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/trigger", h.trigger)
}

func (h *TriggerHandlers) trigger(w http.ResponseWriter, r *http.Request) {
	if h.pipeline == nil || !h.pipeline.IsConfigured() {
		respondError(w, http.StatusServiceUnavailable, "pipeline not configured")
		return
	}

	limitBody(w, r)
	var body struct {
		WorkspaceID string `json:"workspace_id"`
		TicketID    string `json:"ticket_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.TicketID == "" {
		respondError(w, http.StatusBadRequest, "ticket_id is required")
		return
	}

	go h.pipeline.Run(h.ctx, body.WorkspaceID, body.TicketID)

	respondJSON(w, http.StatusAccepted, map[string]string{
		"status":    "accepted",
		"ticket_id": body.TicketID,
	})
}
