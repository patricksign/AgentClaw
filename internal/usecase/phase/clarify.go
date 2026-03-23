package phase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/port"
)

// ClarifyPhase resolves unresolved questions via the escalation chain.
type ClarifyPhase struct{}

func (p *ClarifyPhase) Run(ctx context.Context, pctx PhaseContext) domain.PhaseResult {
	task := pctx.Task

	for i := range task.Questions {
		q := &task.Questions[i]
		if q.Resolved {
			continue
		}

		// Save checkpoint before each escalation attempt — captures which
		// question we're on so resume skips already-resolved ones.
		saveCheckpoint(pctx, task.ID, domain.PhaseClarify, i, "escalate_question", map[string]string{
			"question_id":   q.ID,
			"question_text": q.Text,
			"understanding": task.Understanding,
		})

		result, err := pctx.Escalator.Resolve(ctx, port.EscalatorRequest{
			Question:    q.Text,
			TaskContext: task.Understanding,
			AgentModel:  pctx.AgentCfg.Model,
			AgentRole:   pctx.AgentCfg.Role,
			TaskID:      task.ID,
			QuestionID:  q.ID,
		})
		if err != nil {
			return domain.PhaseResult{Err: fmt.Errorf("clarify: escalate question %s: %w", q.ID, err)}
		}

		if result.NeedsHuman {
			// Update state: blocked waiting for human.
			if err := pctx.StateStore.WriteState(pctx.AgentCfg.ID, port.AgentState{
				AgentID:   pctx.AgentCfg.ID,
				Role:      pctx.AgentCfg.Role,
				Model:     pctx.AgentCfg.Model,
				Status:    "blocked",
				TaskID:    task.ID,
				TaskTitle: task.Title,
				Blockers:  q.Text,
				TimeStuck: time.Since(task.PhaseStartedAt).String(),
				UpdatedAt: time.Now(),
			}); err != nil {
				slog.Warn("clarify: state write failed", "err", err, "agent", pctx.AgentCfg.ID)
			}

			dispatchEvent(ctx, pctx.Notifier, domain.Event{
				Type:      domain.EventQuestionAsked,
				Channel:   domain.StatusChannel,
				TaskID:    task.ID,
				AgentID:   pctx.AgentCfg.ID,
				AgentRole: pctx.AgentCfg.Role,
				Payload:   map[string]string{"message": "Waiting for human — task suspended"},
			})

			return domain.PhaseResult{Suspended: true}
		}

		if result.Resolved {
			q.Answer = result.Answer
			q.AnsweredBy = result.AnsweredBy
			q.Resolved = true

			if err := pctx.TaskStore.SaveTask(task); err != nil {
				return domain.PhaseResult{Err: fmt.Errorf("clarify: save task: %w", err)}
			}

			dispatchEvent(ctx, pctx.Notifier, domain.Event{
				Type:      domain.EventQuestionAnswered,
				Channel:   domain.StatusChannel,
				TaskID:    task.ID,
				AgentID:   pctx.AgentCfg.ID,
				AgentRole: pctx.AgentCfg.Role,
				Payload: map[string]string{
					"message":     fmt.Sprintf("Question resolved by %s — Q: %s", result.AnsweredBy, truncate(q.Text, 80)),
					"answered_by": result.AnsweredBy,
				},
			})
		}
	}

	// All questions resolved — advance to plan phase.
	task.Phase = domain.PhasePlan
	if err := pctx.TaskStore.SaveTask(task); err != nil {
		return domain.PhaseResult{Err: fmt.Errorf("clarify: save task: %w", err)}
	}

	if err := pctx.StateStore.WriteState(pctx.AgentCfg.ID, port.AgentState{
		AgentID:   pctx.AgentCfg.ID,
		Role:      pctx.AgentCfg.Role,
		Model:     pctx.AgentCfg.Model,
		Status:    "running",
		TaskID:    task.ID,
		TaskTitle: task.Title,
		Progress:  "moving to plan",
		UpdatedAt: time.Now(),
	}); err != nil {
		slog.Warn("clarify: state write failed", "err", err, "agent", pctx.AgentCfg.ID)
	}

	return domain.PhaseResult{Done: true}
}
