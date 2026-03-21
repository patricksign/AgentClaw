package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/port"
	"github.com/patricksign/AgentClaw/internal/usecase/phase"
)

// defaultMaxConcurrency limits how many tasks run simultaneously to avoid
// overwhelming upstream API rate limits and excessive memory usage.
const defaultMaxConcurrency = 4

// ParallelOrchestrator runs multiple tasks concurrently via errgroup
// with a configurable concurrency limit. Supports partial failure:
// completed results are returned even when some tasks fail.
type ParallelOrchestrator struct {
	runner         *phase.Runner
	router         port.LLMRouter
	notifier       port.Notifier
	escalator      port.Escalator
	taskStore      port.TaskStore
	statStore      port.StateStore
	maxConcurrency int
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
		runner:         runner,
		router:         router,
		notifier:       notifier,
		escalator:      escalator,
		taskStore:      taskStore,
		statStore:      stateStore,
		maxConcurrency: defaultMaxConcurrency,
	}
}

// SetMaxConcurrency overrides the default concurrency limit.
// Values <= 0 are treated as unlimited.
func (p *ParallelOrchestrator) SetMaxConcurrency(n int) {
	p.maxConcurrency = n
}

// Run executes all tasks in parallel using errgroup and collects results.
// Unlike a simple errgroup, this does NOT cancel remaining tasks on first error.
// Instead, it collects all errors and returns partial results alongside a
// combined error describing which tasks failed.
func (p *ParallelOrchestrator) Run(
	ctx context.Context,
	tasks []*domain.Task,
	mem port.MemoryContext,
) ([]*domain.TaskResult, error) {

	// Dispatch parallel started event.
	_ = p.notifier.Dispatch(ctx, domain.Event{
		Type:       domain.EventParallelStarted,
		Channel:    domain.StatusChannel,
		Payload:    map[string]string{"message": fmt.Sprintf("Running %d tasks in parallel (max %d concurrent)", len(tasks), p.maxConcurrency)},
		OccurredAt: time.Now(),
	})

	// Use errgroup with concurrency limit but WITHOUT context cancellation
	// on first error — we want partial results.
	group, gctx := errgroup.WithContext(ctx)
	if p.maxConcurrency > 0 {
		group.SetLimit(p.maxConcurrency)
	}

	results := make([]*domain.TaskResult, len(tasks))
	var mu sync.Mutex
	var errs []string

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
				// Record the error but do NOT return it — returning error
				// from errgroup.Go cancels the group context for all goroutines.
				mu.Lock()
				errs = append(errs, fmt.Sprintf("task %s (%s): %s", task.ID, task.Title, err))
				mu.Unlock()
				return nil // intentionally nil to avoid cancelling siblings
			}
			results[i] = result
			return nil
		})
	}

	// group.Wait always returns nil since goroutines never return errors.
	_ = group.Wait()

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
			"message": fmt.Sprintf("%d/%d tasks completed, %d failed", completed, len(tasks), len(errs)),
		},
		OccurredAt: time.Now(),
	})

	// Return partial results with combined error.
	var combinedErr error
	if len(errs) > 0 {
		combinedErr = fmt.Errorf("parallel: %d/%d tasks failed:\n  %s", len(errs), len(tasks), strings.Join(errs, "\n  "))
	}

	return results, combinedErr
}

// modelForTask returns a default model based on task complexity.
func (p *ParallelOrchestrator) modelForTask(task *domain.Task) string {
	return domain.WorkerModelForComplexity(task.Complexity)
}
