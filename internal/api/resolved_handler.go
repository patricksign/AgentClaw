package api

import (
	"net/http"

	"github.com/patricksign/agentclaw/internal/state"
)

func (s *Server) HandlerResolved(mux *http.ServeMux) {

	// Resolved error pattern store
	mux.HandleFunc("GET /api/state/resolved", cors(s.getHandleResolved))
	mux.HandleFunc("GET /api/state/resolved/{id}", cors(s.handleResolvedItemById))
	mux.HandleFunc("PATCH /api/state/resolved/{id}/resolve", cors(s.updateResolvedItemById))
}

// ─── Resolved error patterns ─────────────────────────────────────────────────

// GET /api/state/resolved
// Returns all ErrorPattern entries sorted by occurrence_count desc.
func (s *Server) getHandleResolved(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.resolved == nil {
		writeJSON(w, http.StatusOK, []state.ErrorPattern{})
		return
	}
	patterns, err := s.resolved.LoadAll()
	if err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if patterns == nil {
		patterns = []state.ErrorPattern{}
	}
	writeJSON(w, http.StatusOK, patterns)
}

// handleResolvedItem handles:
//
//	GET   /api/state/resolved/:id          — return full detail file content
//	PATCH /api/state/resolved/:id/resolve  — mark as resolved
func (s *Server) handleResolvedItemById(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isValidResolvedID(id) {
		errJSON(w, http.StatusBadRequest, "invalid pattern id")
		return
	}
	if s.resolved == nil {
		errJSON(w, http.StatusServiceUnavailable, "resolved store not configured")
		return
	}
	detail, err := s.resolved.LoadDetail(id)
	if err != nil {
		errJSON(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "detail": detail})
}

// handleResolvedItem handles:
//
//	GET   /api/state/resolved/:id          — return full detail file content
//	PATCH /api/state/resolved/:id/resolve  — mark as resolved
func (s *Server) updateResolvedItemById(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isValidResolvedID(id) {
		errJSON(w, http.StatusBadRequest, "invalid pattern id")
		return
	}
	if s.resolved == nil {
		errJSON(w, http.StatusServiceUnavailable, "resolved store not configured")
		return
	}
	if err := s.resolved.MarkResolved(id); err != nil {
		errJSON(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

// isValidResolvedID reports whether id is a valid 6-hex-character pattern ID.
func isValidResolvedID(id string) bool {
	if len(id) != 6 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
