package phase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/port"
	"github.com/patricksign/AgentClaw/internal/usecase/reasoning"
)

const planSystem = `Write a detailed implementation plan. Return ONLY compact JSON (no whitespace, no markdown fences):
{"plan":"...","files_to_change":["..."]}`

const reviewSystem = `You are Opus, senior engineering supervisor. Review this plan.
If sound: respond APPROVED
If needs changes: respond REDIRECT: followed by specific guidance`

// PlanPhase generates an implementation plan and submits it for Opus review.
type PlanPhase struct{}

type planOutput struct {
	Plan          string   `json:"plan"`
	FilesToChange []string `json:"files_to_change"`
}

func (p *PlanPhase) Run(ctx context.Context, pctx PhaseContext, checkpoint *domain.PhaseCheckpoint) domain.PhaseResult {
	task := pctx.Task

	// ── Step A: Agent generates plan ────────────────────────────────────────

	qaContext := p.buildQAContext(task)

	// If checkpoint has previous plan attempt context (from a redirect),
	// include it so the model improves on the previous attempt.
	prevGuidance := ""
	if checkpoint != nil && checkpoint.Phase == domain.PhasePlan {
		if g := checkpoint.GetAccumulated("opus_guidance"); g != "" {
			prevGuidance = g
		}
	}

	userMsg := fmt.Sprintf("Task: %s\n\nUnderstanding: %s\n\n%s", task.Title, task.Understanding, qaContext)
	if prevGuidance != "" {
		userMsg += fmt.Sprintf("\n\n--- Previous plan was rejected. Opus guidance:\n%s\n\nImprove your plan based on this feedback.", prevGuidance)
	}

	// Save checkpoint before plan generation LLM call.
	saveCheckpoint(pctx, task.ID, domain.PhasePlan, 0, "generate_plan", map[string]string{
		"understanding": task.Understanding,
		"qa_context":    truncate(qaContext, 1000),
	})

	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	planReq := port.LLMRequest{
		Model:     pctx.AgentCfg.Model,
		System:    planSystem,
		Messages:  []port.LLMMessage{{Role: "user", Content: userMsg}},
		MaxTokens: 4096,
		TaskID:    task.ID,
	}
	if domain.SupportsPromptCache(pctx.AgentCfg.Model) {
		planReq.CacheControl = &port.LLMCacheControl{
			CacheSystem: true,
			TTL:         domain.CacheTTLForContent("system"),
		}
	}
	planReq = reasoning.WithThinking(planReq, domain.PhasePlan, task.Complexity)
	resp, err := pctx.Router.Call(callCtx, planReq)
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
		Payload:   map[string]string{"message": "Plan submitted for review"},
	})

	// ── Step B: Supervisor reviews plan ─────────────────────────────────────

	supervisorModel := domain.SupervisorModel(pctx.AgentCfg.Model)

	reviewMsg := fmt.Sprintf("Agent: %s\nTask: %s\n\nImplementation Plan:\n%s\n\nFiles to change: %s",
		pctx.AgentCfg.ID, task.Title, plan.Plan, strings.Join(plan.FilesToChange, ", "))

	// Save checkpoint before review LLM call.
	saveCheckpoint(pctx, task.ID, domain.PhasePlan, 1, "review_plan", map[string]string{
		"understanding": task.Understanding,
		"plan_draft":    truncate(plan.Plan, 2000),
	})

	reviewCtx, reviewCancel := context.WithTimeout(ctx, 60*time.Second)
	reviewReq := port.LLMRequest{
		Model:     supervisorModel,
		System:    reviewSystem,
		Messages:  []port.LLMMessage{{Role: "user", Content: reviewMsg}},
		MaxTokens: 2048,
		TaskID:    task.ID,
	}
	if domain.SupportsPromptCache(supervisorModel) {
		reviewReq.CacheControl = &port.LLMCacheControl{
			CacheSystem: true,
			TTL:         domain.CacheTTLForContent("system"),
		}
	}
	reviewReq = reasoning.WithThinking(reviewReq, domain.PhasePlan, task.Complexity)
	reviewResp, err := pctx.Router.Call(reviewCtx, reviewReq)
	reviewCancel()
	if err != nil {
		return domain.PhaseResult{Err: fmt.Errorf("plan: %s review: %w", supervisorModel, err)}
	}

	verdict := strings.TrimSpace(reviewResp.Content)

	// ── APPROVED ────────────────────────────────────────────────────────────

	if strings.HasPrefix(strings.ToUpper(verdict), "APPROVED") {
		task.PlanApprovedBy = supervisorModel
		task.Phase = domain.PhaseImplement
		if err := pctx.TaskStore.SaveTask(task); err != nil {
			return domain.PhaseResult{Err: fmt.Errorf("plan: save approved: %w", err)}
		}

		// Clear checkpoint — plan approved.
		deleteCheckpoint(pctx, task.ID)

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
		_ = dispatchCritical(ctx, pctx.Notifier, domain.Event{
			Type:      domain.EventPlanFailed,
			Channel:   domain.HumanChannel,
			TaskID:    task.ID,
			AgentID:   pctx.AgentCfg.ID,
			AgentRole: pctx.AgentCfg.Role,
			Payload:   map[string]string{"message": "Plan rejected 3 times — manual intervention needed"},
		})
		return domain.PhaseResult{Err: fmt.Errorf("plan rejected 3 times")}
	}

	// KEY FIX: Preserve understanding and questions — only regenerate the plan.
	// Previously this wiped task.Understanding, task.Questions, and task.ImplementPlan,
	// forcing a full restart from PhaseUnderstand. Now we stay in PhasePlan and
	// save the guidance in a checkpoint so the next plan attempt improves on it.
	task.ImplementPlan = "" // only clear the plan, not understanding
	task.Phase = domain.PhasePlan

	// Save checkpoint with Opus guidance so next iteration uses it.
	saveCheckpoint(pctx, task.ID, domain.PhasePlan, 0, "redirect", map[string]string{
		"understanding":  task.Understanding,
		"opus_guidance":  guidance,
		"redirect_count": fmt.Sprintf("%d", task.RedirectCount),
	})

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
		Payload:   map[string]string{"message": fmt.Sprintf("Plan rejected — improving attempt %d/3", task.RedirectCount)},
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
