package handlers

import (
	"encoding/json"
	"net/http"
	"time"
)

// ScratchpadIface abstracts the scratchpad for handler use.
type ScratchpadIface interface {
	ReadForContext() (string, error)
	AddEntry(entry ScratchpadEntry) error
}

// ScratchpadEntry matches state.ScratchpadEntry for handler decoupling.
type ScratchpadEntry struct {
	Kind      string    `json:"kind"` // in_progress|blocked|decision|handoff|warning
	AgentID   string    `json:"agent_id"`
	TaskID    string    `json:"task_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// ScratchpadHandlers provides HTTP endpoints for the shared scratchpad.
type ScratchpadHandlers struct {
	scratchpad ScratchpadIface
}

// NewScratchpadHandlers creates scratchpad handlers.
func NewScratchpadHandlers(scratchpad ScratchpadIface) *ScratchpadHandlers {
	return &ScratchpadHandlers{scratchpad: scratchpad}
}

// Register mounts scratchpad routes on the given mux.
func (h *ScratchpadHandlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/scratchpad", h.getScratchpad)
	mux.HandleFunc("POST /api/scratchpad", h.addEntry)
}

func (h *ScratchpadHandlers) getScratchpad(w http.ResponseWriter, r *http.Request) {
	if h.scratchpad == nil {
		respondJSON(w, http.StatusOK, map[string]string{"content": ""})
		return
	}
	content, err := h.scratchpad.ReadForContext()
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"content": content})
}

func (h *ScratchpadHandlers) addEntry(w http.ResponseWriter, r *http.Request) {
	if h.scratchpad == nil {
		respondError(w, http.StatusServiceUnavailable, "scratchpad not configured")
		return
	}
	limitBody(w, r)
	var entry ScratchpadEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	if err := h.scratchpad.AddEntry(entry); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusCreated, map[string]string{"status": "added"})
}
