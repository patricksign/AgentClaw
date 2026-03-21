package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/port"
	"github.com/patricksign/AgentClaw/internal/usecase/phase"
)

const reviewSystem = `Review this implementation output for quality, correctness,
and completeness. Respond APPROVED or IMPROVE: <guidance>`

const defaultMaxLoops = 3

// LoopOrchestrator implements the implement→review→improve cycle.
// Opus reviews each iteration until approval or max loops exhausted.
type LoopOrchestrator struct {
	runner   *phase.Runner
	router   port.LLMRouter
	notifier port.Notifier
	maxLoops int
}

// NewLoopOrchestrator creates a loop orchestrator.
// If maxLoops <= 0, defaults to 3.
func NewLoopOrchestrator(
	runner *phase.Runner,
	router port.LLMRouter,
	notifier port.Notifier,
	maxLoops int,
) *LoopOrchestrator {
	if maxLoops <= 0 {
		maxLoops = defaultMaxLoops
	}
	return &LoopOrchestrator{
		runner:   runner,
		router:   router,
		notifier: notifier,
		maxLoops: maxLoops,
	}
}

// Run executes the implement→review loop up to maxLoops times.
func (l *LoopOrchestrator) Run(
	ctx context.Context,
	task *domain.Task,
	mem port.MemoryContext,
	pctx phase.PhaseContext,
) (*domain.TaskResult, error) {

	for attempt := 1; attempt <= l.maxLoops; attempt++ {
		// Step 1 — Run implement phase.
		result, err := l.runner.Run(ctx, pctx)
		if err != nil {
			return nil, fmt.Errorf("loop attempt %d: %w", attempt, err)
		}

		if result == nil {
			return nil, fmt.Errorf("loop attempt %d: implement returned nil result", attempt)
		}

		// Step 2 — Opus quality review.
		reviewMsg := fmt.Sprintf("Task: %s\n\nOutput (first 2000 chars):\n%s\n\nImplementation Plan:\n%s",
			task.Title, truncate(result.Output, 2000*4), task.ImplementPlan)

		reviewCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		reviewResp, err := l.router.Call(reviewCtx, port.LLMRequest{
			Model:     domain.ModelOpus,
			System:    reviewSystem,
			Messages:  []port.LLMMessage{{Role: "user", Content: reviewMsg}},
			MaxTokens: 2048,
			TaskID:    task.ID,
		})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("loop attempt %d: %s review: %w", attempt, domain.ModelOpus, err)
		}

		verdict := strings.TrimSpace(reviewResp.Content)

		// APPROVED — done.
		if strings.HasPrefix(strings.ToUpper(verdict), "APPROVED") {
			_ = l.notifier.Dispatch(ctx, domain.Event{
				Type:       domain.EventTaskDone,
				Channel:    domain.StatusChannel,
				TaskID:     task.ID,
				Payload:    map[string]string{"message": fmt.Sprintf("Loop approved on attempt %d", attempt)},
				OccurredAt: time.Now(),
			})
			return result, nil
		}

		// IMPROVE — extract guidance and loop.
		guidance := verdict
		if idx := strings.Index(strings.ToUpper(verdict), "IMPROVE:"); idx != -1 {
			guidance = strings.TrimSpace(verdict[idx+len("IMPROVE:"):])
		}

		task.Description += "\n\n---\nOpus feedback (attempt " + fmt.Sprintf("%d", attempt) + "):\n" + guidance
		task.Phase = domain.PhaseImplement
		task.ImplementPlan += "\n\nOpus feedback (attempt " + fmt.Sprintf("%d):\n", attempt) + guidance

		if pctx.TaskStore != nil {
			if err := pctx.TaskStore.SaveTask(task); err != nil {
				return nil, fmt.Errorf("loop attempt %d: save task: %w", attempt, err)
			}
		}

		_ = l.notifier.Dispatch(ctx, domain.Event{
			Type:       domain.EventPhaseTransition,
			Channel:    domain.StatusChannel,
			TaskID:     task.ID,
			Payload:    map[string]string{"message": fmt.Sprintf("Output needs improvement — attempt %d/%d", attempt, l.maxLoops)},
			OccurredAt: time.Now(),
		})
	}

	return nil, fmt.Errorf("output not approved after %d attempts", l.maxLoops)
}
