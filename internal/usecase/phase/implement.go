package phase

import (
	"context"
	"fmt"
	"time"

	"github.com/patricksign/agentclaw/internal/domain"
	"github.com/patricksign/agentclaw/internal/port"
)

// ImplementPhase executes the approved plan by calling the agent's LLM.
type ImplementPhase struct{}

func (p *ImplementPhase) Run(ctx context.Context, pctx PhaseContext) domain.PhaseResult {
	task := pctx.Task

	// 1. Dispatch task started event.
	dispatchEvent(ctx, pctx.Notifier, domain.Event{
		Type:      domain.EventTaskStarted,
		Channel:   domain.StatusChannel,
		TaskID:    task.ID,
		AgentID:   pctx.AgentCfg.ID,
		AgentRole: pctx.AgentCfg.Role,
		Model:     pctx.AgentCfg.Model,
		Payload:   map[string]string{"message": fmt.Sprintf("Implementing: %s", task.Title)},
	})

	// 2. Build prompt from plan + memory.
	systemPrompt := pctx.Memory.AgentDoc
	if systemPrompt == "" {
		systemPrompt = fmt.Sprintf("You are a %s agent. Execute the implementation plan.", pctx.AgentCfg.Role)
	}
	userMsg := fmt.Sprintf("Task: %s\n\nDescription: %s\n\nImplementation Plan:\n%s\n\nProject context:\n%s",
		task.Title, task.Description, task.ImplementPlan, pctx.Memory.ProjectDoc)

	start := time.Now()

	// 3. Call LLM — use full context timeout (no additional override).
	resp, err := pctx.Router.Call(ctx, port.LLMRequest{
		Model:     pctx.AgentCfg.Model,
		System:    systemPrompt,
		Messages:  []port.LLMMessage{{Role: "user", Content: userMsg}},
		MaxTokens: 8192,
		TaskID:    task.ID,
	})
	if err != nil {
		return domain.PhaseResult{Err: fmt.Errorf("implement: LLM call: %w", err)}
	}

	durationMs := time.Since(start).Milliseconds()

	// 4. Set task output and mark done.
	task.Output = resp.Content
	task.Phase = domain.PhaseDone
	task.InputTokens += resp.InputTokens
	task.OutputTokens += resp.OutputTokens
	task.CostUSD += resp.CostUSD

	// 5. Save task.
	if err := pctx.TaskStore.SaveTask(task); err != nil {
		return domain.PhaseResult{Err: fmt.Errorf("implement: save task: %w", err)}
	}

	result := &domain.TaskResult{
		TaskID:       task.ID,
		Output:       resp.Content,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		CostUSD:      resp.CostUSD,
		DurationMs:   durationMs,
		ModelUsed:    resp.ModelUsed,
	}

	// 6. Dispatch task done event.
	dispatchEvent(ctx, pctx.Notifier, domain.Event{
		Type:      domain.EventTaskDone,
		Channel:   domain.StatusChannel,
		TaskID:    task.ID,
		AgentID:   pctx.AgentCfg.ID,
		AgentRole: pctx.AgentCfg.Role,
		Model:     resp.ModelUsed,
		Payload: map[string]string{
			"input_tokens":  fmt.Sprintf("%d", resp.InputTokens),
			"output_tokens": fmt.Sprintf("%d", resp.OutputTokens),
			"cost_usd":      fmt.Sprintf("%.6f", resp.CostUSD),
			"duration_ms":   fmt.Sprintf("%d", durationMs),
		},
	})

	return domain.PhaseResult{Done: true, TaskResult: result}
}
