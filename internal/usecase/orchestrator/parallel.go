package orchestrator

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/patricksign/agentclaw/internal/domain"
	"github.com/patricksign/agentclaw/internal/port"
	"github.com/patricksign/agentclaw/internal/usecase/phase"
)

// ParallelOrchestrator runs multiple tasks concurrently via errgroup.
type ParallelOrchestrator struct {
	runner    *phase.Runner
	router    port.LLMRouter
	notifier  port.Notifier
	escalator port.Escalator
	taskStore port.TaskStore
	statStore port.StateStore
}

// NewParallelOrchestrator creates a parallel orchestrator with all required dependencies.
func NewParallelOrchestrator(
	runner *phase.Runner,
	router port.LLMRouter,
	notifier port.Notifier,
	escalator port.Escalator,
	taskStore port.TaskStore,
	stateStore port.StateStore,
) *ParallelOrchestrator {
	return &ParallelOrchestrator{
		runner:    runner,
		router:    router,
		notifier:  notifier,
		escalator: escalator,
		taskStore: taskStore,
		statStore: stateStore,
	}
}

// Run executes all tasks in parallel using errgroup and collects results.
func (p *ParallelOrchestrator) Run(
	ctx context.Context,
	tasks []*domain.Task,
	mem port.MemoryContext,
) ([]*domain.TaskResult, error) {

	// Dispatch parallel started event.
	_ = p.notifier.Dispatch(ctx, domain.Event{
		Type:       domain.EventParallelStarted,
		Channel:    domain.StatusChannel,
		Payload:    map[string]string{"message": fmt.Sprintf("Running %d tasks in parallel", len(tasks))},
		OccurredAt: time.Now(),
	})

	group, gctx := errgroup.WithContext(ctx)
	results := make([]*domain.TaskResult, len(tasks))

	for i, task := range tasks {
		group.Go(func() error {
			pctx := phase.PhaseContext{
				Task: task,
				AgentCfg: domain.AgentConfig{
					ID:    task.AgentID,
					Role:  task.AgentRole,
					Model: p.modelForTask(task),
				},
				Memory:     mem,
				Router:     p.router,
				Notifier:   p.notifier,
				Escalator:  p.escalator,
				TaskStore:  p.taskStore,
				StateStore: p.statStore,
			}
			result, err := p.runner.Run(gctx, pctx)
			if err != nil {
				return fmt.Errorf("task %s (%s): %w", task.ID, task.Title, err)
			}
			results[i] = result
			return nil
		})
	}

	err := group.Wait()

	// Count completed tasks.
	completed := 0
	for _, r := range results {
		if r != nil {
			completed++
		}
	}

	_ = p.notifier.Dispatch(ctx, domain.Event{
		Type:    domain.EventParallelDone,
		Channel: domain.StatusChannel,
		Payload: map[string]string{
			"message": fmt.Sprintf("%d/%d tasks completed", completed, len(tasks)),
		},
		OccurredAt: time.Now(),
	})

	return results, err
}

// modelForTask returns a default model based on task complexity and role.
func (p *ParallelOrchestrator) modelForTask(task *domain.Task) string {
	switch task.Complexity {
	case "S":
		return "glm-flash"
	case "M":
		return "minimax"
	case "L":
		return "sonnet"
	default:
		return "minimax"
	}
}
