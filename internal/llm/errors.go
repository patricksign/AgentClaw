package llm

import (
	"errors"
	"fmt"
)

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("%s %d: %s", e.Provider, e.StatusCode, e.Body)
}

// isPermanentError returns true for 4xx HTTP errors (auth, bad request, etc.)
// which should not trigger a fallback. Uses typed httpStatusError when available,
// falls back to string matching for wrapped errors.
func isPermanentError(err error) bool {
	var httpErr *httpStatusError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode >= 400 && httpErr.StatusCode < 500
	}
	return false
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// truncateErrorBody caps the API error response body at 200 bytes.
func truncateErrorBody(raw []byte) string {
	const maxErrBytes = 200
	if len(raw) <= maxErrBytes {
		return string(raw)
	}
	return string(raw[:maxErrBytes]) + "...(truncated)"
}
