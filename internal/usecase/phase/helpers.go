package phase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/port"
	"github.com/rs/zerolog/log"
)

// saveCheckpoint persists a phase checkpoint if a CheckpointStore is available.
// Best-effort: errors are silently ignored because checkpoint loss is recoverable
// (the phase would simply re-run from scratch on next resume).
func saveCheckpoint(pctx PhaseContext, taskID string, phase domain.ExecutionPhase, stepIndex int, stepName string, accumulated map[string]string) {
	if pctx.CheckpointStore == nil {
		return
	}
	cp := &domain.PhaseCheckpoint{
		TaskID:         taskID,
		AgentID:        pctx.AgentCfg.ID,
		Phase:          phase,
		StepIndex:      stepIndex,
		StepName:       stepName,
		Accumulated:    accumulated,
		SuspendedModel: pctx.AgentCfg.Model,
	}
	_ = pctx.CheckpointStore.Save(cp)
}

// loadCheckpoint retrieves a previously saved checkpoint for a task.
// Returns nil if no checkpoint exists or if CheckpointStore is nil.
func loadCheckpoint(pctx PhaseContext, taskID string) *domain.PhaseCheckpoint {
	if pctx.CheckpointStore == nil {
		return nil
	}
	cp, err := pctx.CheckpointStore.Load(taskID)
	if err != nil {
		return nil
	}
	return cp
}

// deleteCheckpoint removes the checkpoint for a task after successful completion.
func deleteCheckpoint(pctx PhaseContext, taskID string) {
	if pctx.CheckpointStore == nil {
		return
	}
	_ = pctx.CheckpointStore.Delete(taskID)
}

// dispatchEvent fires an event via the notifier in a goroutine (non-blocking).
// Used for status updates that should not slow down the pipeline.
// Errors are logged but do not affect the caller.
func dispatchEvent(ctx context.Context, notifier port.Notifier, evt domain.Event) {
	if evt.OccurredAt.IsZero() {
		evt.OccurredAt = time.Now()
	}
	go func() {
		if err := notifier.Dispatch(ctx, evt); err != nil {
			log.Warn().Err(err).
				Str("event", string(evt.Type)).
				Str("task", evt.TaskID).
				Msg("async notification failed")
		}
	}()
}

// dispatchCritical fires an event synchronously and returns the error.
// Used for critical notifications (task failed, fallback exhausted, plan failed)
// where the caller must know if notification succeeded — if it fails, the caller
// should stop the flow and handle the notification failure.
func dispatchCritical(ctx context.Context, notifier port.Notifier, evt domain.Event) error {
	if evt.OccurredAt.IsZero() {
		evt.OccurredAt = time.Now()
	}
	if err := notifier.Dispatch(ctx, evt); err != nil {
		log.Error().Err(err).
			Str("event", string(evt.Type)).
			Str("task", evt.TaskID).
			Msg("critical notification failed")
		return fmt.Errorf("critical notification (%s) failed: %w", evt.Type, err)
	}
	return nil
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
