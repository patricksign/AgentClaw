package api

import (
	"net/http"
	"time"

	"github.com/patricksign/AgentClaw/internal/state"
)

func (s *Server) HandlerScratchpad(mux *http.ServeMux) {
	// Scratchpad
	mux.HandleFunc("GET /api/scratchpad", cors(s.getScratchpad))
	mux.HandleFunc("POST /api/scratchpad", cors(s.createScratchpad))
}

// ─── Scratchpad ───────────────────────────────────────────────────────────────

// GET  /api/scratchpad — returns {markdown, entries}
func (s *Server) getScratchpad(w http.ResponseWriter, r *http.Request) {
	if s.scratchpad == nil {
		errJSON(w, http.StatusServiceUnavailable, "scratchpad not configured")
		return
	}

	switch r.Method {
	case http.MethodGet:
		entries, err := s.scratchpad.Read()
		if err != nil {
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		if entries == nil {
			entries = []state.ScratchpadEntry{}
		}
		// Read raw markdown from file for human consumption.
		md, merr := s.scratchpad.ReadForContext()
		if merr != nil {
			md = ""
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"markdown": md,
			"entries":  entries,
		})
	default:
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ─── Scratchpad ───────────────────────────────────────────────────────────────

// POST /api/scratchpad — adds a manual entry {agent_id, kind, message, task_id}
func (s *Server) createScratchpad(w http.ResponseWriter, r *http.Request) {
	if s.scratchpad == nil {
		errJSON(w, http.StatusServiceUnavailable, "scratchpad not configured")
		return
	}

	switch r.Method {
	case http.MethodPost:
		var req struct {
			AgentID string               `json:"agent_id"`
			Kind    state.ScratchpadKind `json:"kind"`
			Message string               `json:"message"`
			TaskID  string               `json:"task_id"`
		}
		if err := readJSON(r, &req); err != nil {
			errJSON(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if req.AgentID == "" || req.Message == "" {
			errJSON(w, http.StatusBadRequest, "agent_id and message required")
			return
		}
		switch req.Kind {
		case state.KindInProgress, state.KindBlocked, state.KindDecision,
			state.KindHandoff, state.KindWarning:
			// valid
		default:
			errJSON(w, http.StatusBadRequest, "kind must be one of: in_progress, blocked, decision, handoff, warning")
			return
		}
		entry := state.ScratchpadEntry{
			AgentID:   req.AgentID,
			Kind:      req.Kind,
			Message:   req.Message,
			TaskID:    req.TaskID,
			Timestamp: time.Now(),
		}
		if err := s.scratchpad.AddEntry(entry); err != nil {
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, entry)

	default:
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
