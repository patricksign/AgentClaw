package phase

import (
	"context"
	"strings"
	"time"

	"github.com/patricksign/agentclaw/internal/domain"
	"github.com/patricksign/agentclaw/internal/port"
)

// dispatchEvent is a convenience helper that fires an event via the notifier.
func dispatchEvent(ctx context.Context, notifier port.Notifier, evt domain.Event) {
	if evt.OccurredAt.IsZero() {
		evt.OccurredAt = time.Now()
	}
	// Best-effort dispatch; callers log but do not fail on notification errors.
	_ = notifier.Dispatch(ctx, evt)
}

// truncate returns the first n runes of s, appending "…" if truncated.
// Uses rune-based truncation to avoid splitting multi-byte UTF-8 characters.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// stripMarkdownFences removes ```json ... ``` wrappers from LLM output.
func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Remove opening fence line.
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		// Remove closing fence.
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
	}
	return strings.TrimSpace(s)
}
