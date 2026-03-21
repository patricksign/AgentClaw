package phase

import (
	"context"
	"fmt"
	"time"

	"github.com/patricksign/AgentClaw/internal/domain"
)

const maxPhaseIterations = 4

// Runner orchestrates the full phase loop for a task: understand → clarify → plan → implement.
type Runner struct {
	understand *UnderstandPhase
	clarify    *ClarifyPhase
	plan       *PlanPhase
	implement  *ImplementPhase
}

// NewRunner creates a Runner with all phase handlers.
func NewRunner() *Runner {
	return &Runner{
		understand: &UnderstandPhase{},
		clarify:    &ClarifyPhase{},
		plan:       &PlanPhase{},
		implement:  &ImplementPhase{},
	}
}

// Run drives the task through phases until completion, suspension, or error.
// Returns nil TaskResult and nil error when the task is suspended (waiting for human).
//
// On suspend: saves a PhaseCheckpoint so the task resumes from the exact step.
// On resume: loads the checkpoint and continues from where it left off.
// On completion: deletes the checkpoint.
func (r *Runner) Run(ctx context.Context, pctx PhaseContext) (*domain.TaskResult, error) {
	task := pctx.Task

	// Initialize phase for new tasks.
	if task.Phase == "" {
		task.Phase = domain.PhaseUnderstand
		task.PhaseStartedAt = time.Now()
	}

	// Try to restore checkpoint — if the task was previously suspended,
	// this gives each phase access to accumulated context.
	var checkpoint *domain.PhaseCheckpoint
	if pctx.CheckpointStore != nil {
		cp, err := pctx.CheckpointStore.Load(task.ID)
		if err == nil && cp != nil {
			checkpoint = cp
		}
	}

	for iteration := range maxPhaseIterations {
		_ = iteration

		switch task.Phase {
		case domain.PhaseUnderstand:
			result := r.understand.Run(ctx, pctx, checkpoint)
			if result.Err != nil {
				return nil, result.Err
			}
			// Checkpoint consumed or not applicable — clear for next phase.
			checkpoint = nil

		case domain.PhaseClarify:
			result := r.clarify.Run(ctx, pctx)
			if result.Err != nil {
				return nil, result.Err
			}
			if result.Suspended {
				return nil, nil // task parked, waiting for human
			}

		case domain.PhasePlan:
			result := r.plan.Run(ctx, pctx, checkpoint)
			if result.Err != nil {
				return nil, result.Err
			}
			checkpoint = nil
			if result.Restarted {
				continue // loop back to plan with guidance (NOT full restart)
			}
			if result.Suspended {
				return nil, nil
			}

		case domain.PhaseImplement:
			result := r.implement.Run(ctx, pctx)
			if result.Err != nil {
				return nil, result.Err
			}

			// Task complete — clean up checkpoint.
			if pctx.CheckpointStore != nil {
				_ = pctx.CheckpointStore.Delete(task.ID)
			}

			return result.TaskResult, nil

		case domain.PhaseDone:
			return nil, fmt.Errorf("task already done")

		default:
			return nil, fmt.Errorf("unknown phase: %s", task.Phase)
		}
	}

	return nil, fmt.Errorf("phase loop exceeded max iterations (%d)", maxPhaseIterations)
}
