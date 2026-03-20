package api

import (
	"net/http"

	"github.com/rs/zerolog/log"
)

// maxConcurrentPipelines limits how many trigger pipelines can run simultaneously.
// Each pipeline makes multiple LLM calls and can run for minutes.
const maxConcurrentPipelines = 5

// pipelineSem is a counting semaphore that limits concurrent pipeline executions.
var pipelineSem = make(chan struct{}, maxConcurrentPipelines)

func (s *Server) HandlerTrigger(mux *http.ServeMux) {
	// Trigger pipeline
	mux.HandleFunc("POST /api/trigger", cors(s.handleTrigger))
}

// ─── Trigger ─────────────────────────────────────────────────────────────────

// POST /api/trigger
// Body: {"workspace_id":"<board_id>","ticket_id":"<card_id_or_shortlink>"}
// Returns 202 Accepted immediately; the agent pipeline runs in the background.
func (s *Server) handleTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		WorkspaceID string `json:"workspace_id"`
		TicketID    string `json:"ticket_id"`
	}
	if err := readJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.WorkspaceID == "" || req.TicketID == "" {
		errJSON(w, http.StatusBadRequest, "workspace_id and ticket_id are required")
		return
	}

	if s.triggerSvc == nil || !s.triggerSvc.IsConfigured() {
		errJSON(w, http.StatusServiceUnavailable, "Trello integration not configured (TRELLO_KEY/TRELLO_TOKEN missing)")
		return
	}

	// Return 202 immediately; run pipeline in background.
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":       "accepted",
		"workspace_id": req.WorkspaceID,
		"ticket_id":    req.TicketID,
	})

	// Acquire semaphore slot — reject if at capacity.
	select {
	case pipelineSem <- struct{}{}:
	default:
		errJSON(w, http.StatusTooManyRequests, "too many concurrent pipelines, try again later")
		return
	}

	go func() {
		defer func() { <-pipelineSem }()
		if err := s.triggerSvc.Run(s.ctx, req.WorkspaceID, req.TicketID); err != nil {
			log.Error().Err(err).
				Str("workspace_id", req.WorkspaceID).
				Str("ticket_id", req.TicketID).
				Msg("trigger pipeline failed")
		}
	}()
}
