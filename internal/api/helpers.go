package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"

	"github.com/rs/zerolog/log"
)

// ─── Helpers ─────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error().Err(err).Msg("writeJSON encode failed")
	}
}

// maxBodyBytes is the maximum accepted request body size (1 MiB).
const maxBodyBytes = 1 << 20

func readJSON(r *http.Request, v any) error {
	limited := io.LimitReader(r.Body, maxBodyBytes)
	return json.NewDecoder(limited).Decode(v)
}

func errJSON(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// corsAllowMethods and corsAllowHeaders are pre-computed constants
// to avoid repeated string construction on every request.
const (
	corsAllowMethods = "GET,POST,PATCH,PUT,DELETE,OPTIONS"
	corsAllowHeaders = "Content-Type"
)

func cors(next http.HandlerFunc) http.HandlerFunc {
	// Capture once at middleware creation time — not on every request.
	allowedOrigin := os.Getenv("CORS_ORIGIN")

	return func(w http.ResponseWriter, r *http.Request) {
		origin := allowedOrigin
		if origin == "" {
			// No CORS_ORIGIN configured — restrict to same-origin only.
			// Never use wildcard "*" as it allows any website to exfiltrate API data.
			reqOrigin := r.Header.Get("Origin")
			if reqOrigin == "" || reqOrigin == "http://"+r.Host || reqOrigin == "https://"+r.Host {
				origin = reqOrigin
			} else {
				errJSON(w, http.StatusForbidden, "CORS: origin not allowed, set CORS_ORIGIN env var")
				return
			}
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		w.Header().Set("Access-Control-Allow-Methods", corsAllowMethods)
		w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}
