package phase

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/port"
	"github.com/patricksign/AgentClaw/internal/usecase/reasoning"
)

const understandSystem = `You are a senior engineer. Analyze the task and return ONLY compact JSON (no whitespace, no markdown fences):
{"understanding":"...","assumptions":["..."],"risks":["..."],"questions":["..."]}`

// UnderstandPhase analyses the task and extracts understanding, assumptions,
// risks, and clarification questions.
type UnderstandPhase struct{}

type understandOutput struct {
	Understanding string   `json:"understanding"`
	Assumptions   []string `json:"assumptions"`
	Risks         []string `json:"risks"`
	Questions     []string `json:"questions"`
}

func (p *UnderstandPhase) Run(ctx context.Context, pctx PhaseContext, checkpoint *domain.PhaseCheckpoint) domain.PhaseResult {
	task := pctx.Task

	// If we have a checkpoint with accumulated understanding, use it
	// instead of re-calling the LLM. This happens when a plan redirect
	// preserved the understanding context.
	if checkpoint != nil && checkpoint.Phase == domain.PhaseUnderstand {
		if prev := checkpoint.GetAccumulated("understanding"); prev != "" {
			task.Understanding = prev
		}
	}

	// 1. Dispatch phase transition event.
	dispatchEvent(ctx, pctx.Notifier, domain.Event{
		Type:      domain.EventPhaseTransition,
		Channel:   domain.StatusChannel,
		TaskID:    task.ID,
		AgentID:   pctx.AgentCfg.ID,
		AgentRole: pctx.AgentCfg.Role,
		Model:     pctx.AgentCfg.Model,
		Payload:   map[string]string{"message": "Starting understand phase"},
	})

	// 2. Build user message from task + memory context.
	userMsg := fmt.Sprintf("Task: %s\n\nDescription: %s\n\nProject context:\n%s\n\nAgent context:\n%s",
		task.Title, task.Description,
		truncate(pctx.Memory.ProjectDoc, 800*4),
		truncate(pctx.Memory.AgentDoc, 400*4),
	)

	// 3. Save checkpoint before LLM call — captures state if process crashes.
	saveCheckpoint(pctx, task.ID, domain.PhaseUnderstand, 0, "llm_call", map[string]string{
		"user_msg": truncate(userMsg, 2000),
	})

	// 4. Call LLM with 60s timeout.
	out, err := p.callWithRetry(ctx, pctx.Router, pctx.AgentCfg.Model, task.ID, userMsg, task.Complexity)
	if err != nil {
		return domain.PhaseResult{Err: fmt.Errorf("understand: %w", err)}
	}

	// 5. Set task fields.
	task.Understanding = out.Understanding
	task.Assumptions = out.Assumptions
	task.Risks = out.Risks

	if len(out.Questions) > 0 {
		now := time.Now()
		for i, q := range out.Questions {
			task.Questions = append(task.Questions, domain.Question{
				ID:        fmt.Sprintf("%s-q%d", task.ID, i),
				Text:      q,
				CreatedAt: now,
			})
		}
		task.Phase = domain.PhaseClarify
	} else {
		task.Phase = domain.PhasePlan
	}

	// 6. Save checkpoint with accumulated understanding for potential future resume.
	saveCheckpoint(pctx, task.ID, task.Phase, 0, "phase_transition", map[string]string{
		"understanding": task.Understanding,
	})

	// 7. Save task.
	if err := pctx.TaskStore.SaveTask(task); err != nil {
		return domain.PhaseResult{Err: fmt.Errorf("understand: save task: %w", err)}
	}

	// 8. Dispatch summary event.
	msg := fmt.Sprintf("Understood — moving to plan. Assumptions: %s", truncate(fmt.Sprint(task.Assumptions), 200))
	if len(task.Questions) > 0 {
		msg = fmt.Sprintf("Task paused — %d questions pending clarification", len(task.Questions))
	}
	dispatchEvent(ctx, pctx.Notifier, domain.Event{
		Type:      domain.EventPhaseTransition,
		Channel:   domain.StatusChannel,
		TaskID:    task.ID,
		AgentID:   pctx.AgentCfg.ID,
		AgentRole: pctx.AgentCfg.Role,
		Payload:   map[string]string{"message": msg},
	})

	return domain.PhaseResult{Done: true}
}

// callWithRetry calls the LLM once; on JSON parse failure retries once.
func (p *UnderstandPhase) callWithRetry(ctx context.Context, router port.LLMRouter, model, taskID, userMsg, complexity string) (*understandOutput, error) {
	for attempt := range 2 {
		callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		req := port.LLMRequest{
			Model:     model,
			System:    understandSystem,
			Messages:  []port.LLMMessage{{Role: "user", Content: userMsg}},
			MaxTokens: 2048,
			TaskID:    taskID,
		}
		req = reasoning.WithThinking(req, domain.PhaseUnderstand, complexity)
		if domain.SupportsPromptCache(model) {
			req.CacheControl = &port.LLMCacheControl{
				CacheSystem: true,
				TTL:         domain.CacheTTLForContent("system"),
			}
		}
		resp, err := router.Call(callCtx, req)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("LLM call: %w", err)
		}

		raw := stripMarkdownFences(resp.Content)
		var out understandOutput
		if jsonErr := json.Unmarshal([]byte(raw), &out); jsonErr != nil {
			if attempt == 0 {
				continue // retry once
			}
			return nil, fmt.Errorf("parse LLM JSON after retry: %w", jsonErr)
		}
		return &out, nil
	}
	return nil, fmt.Errorf("understand: exhausted retries")
}
