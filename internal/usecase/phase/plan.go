package phase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/patricksign/agentclaw/internal/domain"
	"github.com/patricksign/agentclaw/internal/port"
)

const planSystem = `Write a detailed implementation plan. Return ONLY valid JSON:
{"plan":"...","files_to_change":[],"approach":"...","edge_cases":[],"test_cases":[]}`

const reviewSystem = `You are Opus, senior engineering supervisor. Review this plan.
If sound: respond APPROVED
If needs changes: respond REDIRECT: followed by specific guidance`

// PlanPhase generates an implementation plan and submits it for Opus review.
type PlanPhase struct{}

type planOutput struct {
	Plan          string   `json:"plan"`
	FilesToChange []string `json:"files_to_change"`
	Approach      string   `json:"approach"`
	EdgeCases     []string `json:"edge_cases"`
	TestCases     []string `json:"test_cases"`
}

func (p *PlanPhase) Run(ctx context.Context, pctx PhaseContext) domain.PhaseResult {
	task := pctx.Task

	// ── Step A: Agent generates plan ────────────────────────────────────────

	qaContext := p.buildQAContext(task)
	userMsg := fmt.Sprintf("Task: %s\n\nUnderstanding: %s\n\n%s", task.Title, task.Understanding, qaContext)

	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	resp, err := pctx.Router.Call(callCtx, port.LLMRequest{
		Model:     pctx.AgentCfg.Model,
		System:    planSystem,
		Messages:  []port.LLMMessage{{Role: "user", Content: userMsg}},
		MaxTokens: 4096,
		TaskID:    task.ID,
	})
	cancel()
	if err != nil {
		return domain.PhaseResult{Err: fmt.Errorf("plan: generate: %w", err)}
	}

	raw := stripMarkdownFences(resp.Content)
	var plan planOutput
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return domain.PhaseResult{Err: fmt.Errorf("plan: parse JSON: %w", err)}
	}

	task.ImplementPlan = plan.Plan

	dispatchEvent(ctx, pctx.Notifier, domain.Event{
		Type:      domain.EventPlanSubmitted,
		Channel:   domain.StatusChannel,
		TaskID:    task.ID,
		AgentID:   pctx.AgentCfg.ID,
		AgentRole: pctx.AgentCfg.Role,
		Model:     pctx.AgentCfg.Model,
		Payload:   map[string]string{"message": "Plan submitted for Opus review"},
	})

	// ── Step B: Opus reviews plan ───────────────────────────────────────────

	reviewMsg := fmt.Sprintf("Agent: %s\nTask: %s\n\nImplementation Plan:\n%s\n\nFiles to change: %s",
		pctx.AgentCfg.ID, task.Title, plan.Plan, strings.Join(plan.FilesToChange, ", "))

	reviewCtx, reviewCancel := context.WithTimeout(ctx, 60*time.Second)
	reviewResp, err := pctx.Router.Call(reviewCtx, port.LLMRequest{
		Model:     "opus",
		System:    reviewSystem,
		Messages:  []port.LLMMessage{{Role: "user", Content: reviewMsg}},
		MaxTokens: 2048,
		TaskID:    task.ID,
	})
	reviewCancel()
	if err != nil {
		return domain.PhaseResult{Err: fmt.Errorf("plan: opus review: %w", err)}
	}

	verdict := strings.TrimSpace(reviewResp.Content)

	// ── APPROVED ────────────────────────────────────────────────────────────

	if strings.HasPrefix(strings.ToUpper(verdict), "APPROVED") {
		task.PlanApprovedBy = "opus"
		task.Phase = domain.PhaseImplement
		if err := pctx.TaskStore.SaveTask(task); err != nil {
			return domain.PhaseResult{Err: fmt.Errorf("plan: save approved: %w", err)}
		}

		dispatchEvent(ctx, pctx.Notifier, domain.Event{
			Type:      domain.EventPlanApproved,
			Channel:   domain.HumanChannel,
			TaskID:    task.ID,
			AgentID:   pctx.AgentCfg.ID,
			AgentRole: pctx.AgentCfg.Role,
			Payload:   map[string]string{"plan": truncate(plan.Plan, 300)},
		})
		dispatchEvent(ctx, pctx.Notifier, domain.Event{
			Type:      domain.EventPhaseTransition,
			Channel:   domain.StatusChannel,
			TaskID:    task.ID,
			AgentID:   pctx.AgentCfg.ID,
			AgentRole: pctx.AgentCfg.Role,
			Payload:   map[string]string{"message": "Moving to implementation"},
		})

		return domain.PhaseResult{Done: true}
	}

	// ── REDIRECT ────────────────────────────────────────────────────────────

	task.RedirectCount++
	guidance := verdict
	if idx := strings.Index(strings.ToUpper(verdict), "REDIRECT:"); idx != -1 {
		guidance = strings.TrimSpace(verdict[idx+len("REDIRECT:"):])
	}

	if task.RedirectCount >= 3 {
		dispatchEvent(ctx, pctx.Notifier, domain.Event{
			Type:      domain.EventPlanFailed,
			Channel:   domain.HumanChannel,
			TaskID:    task.ID,
			AgentID:   pctx.AgentCfg.ID,
			AgentRole: pctx.AgentCfg.Role,
			Payload:   map[string]string{"message": "Plan rejected 3 times — manual intervention needed"},
		})
		return domain.PhaseResult{Err: fmt.Errorf("plan rejected 3 times")}
	}

	// Append guidance and restart from understand.
	task.Description += "\n\n---\nOpus guidance:\n" + guidance
	task.Phase = domain.PhaseUnderstand
	task.Understanding = ""
	task.Questions = nil
	task.ImplementPlan = ""
	if err := pctx.TaskStore.SaveTask(task); err != nil {
		return domain.PhaseResult{Err: fmt.Errorf("plan: save redirect: %w", err)}
	}

	dispatchEvent(ctx, pctx.Notifier, domain.Event{
		Type:      domain.EventPlanRedirected,
		Channel:   domain.HumanChannel,
		TaskID:    task.ID,
		AgentID:   pctx.AgentCfg.ID,
		AgentRole: pctx.AgentCfg.Role,
		Payload: map[string]string{
			"guidance": guidance,
			"attempt":  fmt.Sprintf("%d/3", task.RedirectCount),
		},
	})
	dispatchEvent(ctx, pctx.Notifier, domain.Event{
		Type:      domain.EventPhaseTransition,
		Channel:   domain.StatusChannel,
		TaskID:    task.ID,
		AgentID:   pctx.AgentCfg.ID,
		AgentRole: pctx.AgentCfg.Role,
		Payload:   map[string]string{"message": fmt.Sprintf("Plan rejected — restarting attempt %d/3", task.RedirectCount)},
	})

	return domain.PhaseResult{Restarted: true}
}

// buildQAContext formats resolved questions into a readable string.
func (p *PlanPhase) buildQAContext(task *domain.Task) string {
	var sb strings.Builder
	sb.WriteString("Resolved Q&A:\n")
	for _, q := range task.Questions {
		if q.Resolved {
			fmt.Fprintf(&sb, "Q: %s\nA (%s): %s\n\n", q.Text, q.AnsweredBy, q.Answer)
		}
	}
	return sb.String()
}
