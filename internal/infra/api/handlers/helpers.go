package handlers

import (
	"encoding/json"
	"io"
	"net/http"
)

// maxRequestBody is the maximum allowed request body size (1 MiB).
const maxRequestBody = 1 << 20

// respondJSON writes a JSON response with the given status code.
// Marshals to a buffer first to catch encoding errors.
func respondJSON(w http.ResponseWriter, status int, data any) {
	buf, err := json.Marshal(data)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"internal encoding error"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(buf)
}

// respondError writes a JSON error response.
func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]string{"error": message})
}

// limitBody wraps r.Body with http.MaxBytesReader to prevent OOM from large payloads.
func limitBody(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
}
