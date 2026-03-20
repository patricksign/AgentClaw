package phase

import (
	"context"
	"fmt"
	"time"

	"github.com/patricksign/agentclaw/internal/domain"
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
func (r *Runner) Run(ctx context.Context, pctx PhaseContext) (*domain.TaskResult, error) {
	task := pctx.Task

	// Initialize phase for new tasks.
	if task.Phase == "" {
		task.Phase = domain.PhaseUnderstand
		task.PhaseStartedAt = time.Now()
	}

	for iteration := range maxPhaseIterations {
		_ = iteration // consumed by range

		switch task.Phase {
		case domain.PhaseUnderstand:
			result := r.understand.Run(ctx, pctx)
			if result.Err != nil {
				return nil, result.Err
			}

		case domain.PhaseClarify:
			result := r.clarify.Run(ctx, pctx)
			if result.Err != nil {
				return nil, result.Err
			}
			if result.Suspended {
				return nil, nil // task parked, waiting for human
			}

		case domain.PhasePlan:
			result := r.plan.Run(ctx, pctx)
			if result.Err != nil {
				return nil, result.Err
			}
			if result.Restarted {
				continue // loop back to understand
			}
			if result.Suspended {
				return nil, nil
			}

		case domain.PhaseImplement:
			result := r.implement.Run(ctx, pctx)
			if result.Err != nil {
				return nil, result.Err
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
