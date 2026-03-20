package orchestrator

import (
	"context"
	"fmt"
	"slices"

	"github.com/patricksign/agentclaw/internal/domain"
	"github.com/patricksign/agentclaw/internal/port"
	"github.com/patricksign/agentclaw/internal/usecase/phase"
)

// OrchestratorRouter selects the appropriate orchestration pattern for a task.
type OrchestratorRouter struct {
	hierarchical *HierarchicalOrchestrator
	parallel     *ParallelOrchestrator
	loop         *LoopOrchestrator
	runner       *phase.Runner
	llmRouter    port.LLMRouter
	notifier     port.Notifier
}

// NewOrchestratorRouter creates the router with all orchestrator variants.
func NewOrchestratorRouter(
	hierarchical *HierarchicalOrchestrator,
	parallel *ParallelOrchestrator,
	loop *LoopOrchestrator,
	runner *phase.Runner,
	llmRouter port.LLMRouter,
	notifier port.Notifier,
) *OrchestratorRouter {
	return &OrchestratorRouter{
		hierarchical: hierarchical,
		parallel:     parallel,
		loop:         loop,
		runner:       runner,
		llmRouter:    llmRouter,
		notifier:     notifier,
	}
}

// Route analyses the task and delegates to the appropriate orchestrator.
func (r *OrchestratorRouter) Route(
	ctx context.Context,
	task *domain.Task,
	mem port.MemoryContext,
	pctx phase.PhaseContext,
) ([]*domain.TaskResult, error) {

	// Hierarchical: idea or architect roles need breakdown.
	if task.AgentRole == "idea" || task.AgentRole == "architect" {
		return r.hierarchical.Run(ctx, task, mem, r.parallel)
	}

	// Loop: large tasks or tasks explicitly tagged "loop".
	if slices.Contains(task.Tags, "loop") || task.Complexity == "L" {
		result, err := r.loop.Run(ctx, task, mem, pctx)
		if err != nil {
			return nil, err
		}
		return []*domain.TaskResult{result}, nil
	}

	// Default: single task through the phase runner.
	result, err := r.runner.Run(ctx, pctx)
	if err != nil {
		return nil, err
	}
	if result == nil {
		// Task suspended (waiting for human).
		return nil, nil
	}
	return []*domain.TaskResult{result}, nil
}

// RouteInfo returns the orchestration pattern that would be selected for a task.
// Useful for logging and debugging without actually executing.
func (r *OrchestratorRouter) RouteInfo(task *domain.Task) string {
	if task.AgentRole == "idea" || task.AgentRole == "architect" {
		return "hierarchical"
	}
	if slices.Contains(task.Tags, "loop") || task.Complexity == "L" {
		return "loop"
	}
	return fmt.Sprintf("single-phase (role=%s)", task.AgentRole)
}
