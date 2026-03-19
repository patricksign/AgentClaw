// Package pipeline implements the AgentClaw end-to-end orchestration pipeline.
//
// Triggered by POST /api/trigger with body:
//
//	{"workspace_id": "<board_id>", "ticket_id": "<card_id>"}
//
// The HTTP handler returns 202 Accepted immediately; Run() executes in a
// background goroutine.
//
// Pipeline steps:
//  1. Fetch Trello card by ticket_id.
//  2. Idea Agent (claude-opus-4-6) → structured app concept.
//  3. Append concept to Trello card description.
//  4. Breakdown Agent (claude-sonnet-4-6) → JSON task list (max 10).
//  5. EnsureChecklist + PopulateChecklist on the card.
//  6. For each task: call the role-appropriate LLM, mark checklist item,
//     send Telegram notification, optionally create GitHub PR.
//  7. Final Telegram notification (complete or partial summary).
//
// Required env vars:
//
//	TRELLO_KEY, TRELLO_TOKEN
//	ANTHROPIC_API_KEY (for opus / sonnet steps)
//	MINIMAX_API_KEY   (for coding tasks)
//	GLM_API_KEY       (for test / docs tasks)
//
// Optional:
//
//	GITHUB_TOKEN, GITHUB_OWNER, GITHUB_REPO  — skipped silently if absent
//	TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID     — skipped silently if absent
//	SLACK_WEBHOOK_URL                         — skipped silently if absent
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/patricksign/agentclaw/internal/integrations/github"
	"github.com/patricksign/agentclaw/internal/integrations/telegram"
	"github.com/patricksign/agentclaw/internal/integrations/trello"
	"github.com/patricksign/agentclaw/internal/llm"
	"github.com/rs/zerolog/log"
)

// Service orchestrates the AgentClaw agent pipeline.
type Service struct {
	trello   *trello.Client
	telegram *telegram.Client
	slack    *telegram.SlackClient
	github   *github.Client
	llm      *llm.Router
}

// NewService wires up all integration clients from environment variables.
// Clients that are not configured (missing env vars) are left nil and their
// pipeline steps are skipped silently.
func NewService(tc *trello.Client) *Service {
	s := &Service{
		trello: tc,
		llm:    llm.NewRouter(),
	}

	if tg, err := telegram.New(); err == nil {
		s.telegram = tg
	} else if telegram.IsEnvPresent() {
		log.Warn().Err(err).Msg("pipeline: Telegram env vars present but client init failed")
	}
	if sl, err := telegram.NewSlackClient(); err == nil {
		s.slack = sl
	} else if telegram.IsSlackEnvPresent() {
		log.Warn().Err(err).Msg("pipeline: Slack env vars present but client init failed")
	}
	if github.IsConfigured() {
		if gh, err := github.New(); err == nil {
			s.github = gh
		} else {
			log.Warn().Err(err).Msg("pipeline: GitHub env vars present but client init failed")
		}
	}
	return s
}

// IsConfigured reports whether the Trello client is present and configured.
func (s *Service) IsConfigured() bool {
	return s.trello != nil && s.trello.IsConfigured()
}

// ─── Task model ───────────────────────────────────────────────────────────────

type pipelineTask struct {
	Title      string `json:"title"`
	Role       string `json:"role"`       // coding | test | docs | review
	Complexity string `json:"complexity"` // S | M | L
}

// ─── Run ──────────────────────────────────────────────────────────────────────

// Run executes the full pipeline for the given Trello card.
// Intended to be called in a goroutine.
func (s *Service) Run(ctx context.Context, boardID, ticketID string) error {
	logger := log.With().Str("ticket_id", ticketID).Str("board_id", boardID).Logger()

	if s.trello == nil || !s.trello.IsConfigured() {
		return fmt.Errorf("pipeline: Trello client not configured")
	}

	// ── Step 1: Fetch card ────────────────────────────────────────────────────
	logger.Info().Msg("pipeline: fetching Trello card")
	card, err := s.trello.GetCard(ctx, ticketID)
	if err != nil {
		return fmt.Errorf("pipeline: card not found: %w", err)
	}
	logger.Info().Str("card_id", card.ID).Str("name", card.Name).Msg("pipeline: card found")

	// ── Step 2: Idea Agent (opus) ─────────────────────────────────────────────
	logger.Info().Msg("pipeline: step 2 — idea agent (claude-opus-4-6)")
	ideaOutput, _, err := s.callLLM(ctx, "opus", ideaSystemPrompt(),
		fmt.Sprintf("**App Idea:**\nTitle: %s\n\nDescription:\n%s", card.Name, card.Desc))
	if err != nil {
		return fmt.Errorf("pipeline: idea agent: %w", err)
	}

	// ── Step 3: Append concept to card description ────────────────────────────
	logger.Info().Msg("pipeline: step 3 — appending concept to card")
	newDesc := card.Desc
	if newDesc != "" {
		newDesc += "\n\n---\n\n"
	}
	newDesc += "## AgentClaw Concept\n\n" + ideaOutput
	if err := s.trello.UpdateCardDescription(ctx, card.ID, newDesc); err != nil {
		logger.Warn().Err(err).Msg("pipeline: failed to update card description — continuing")
	}

	// ── Step 4: Breakdown Agent (sonnet) ──────────────────────────────────────
	logger.Info().Msg("pipeline: step 4 — breakdown agent (claude-sonnet-4-6)")
	breakdownOutput, _, err := s.callLLM(ctx, "sonnet", breakdownSystemPrompt(),
		fmt.Sprintf("App concept:\n\n%s\n\nGenerate the task list now.", ideaOutput))
	if err != nil {
		return fmt.Errorf("pipeline: breakdown agent: %w", err)
	}

	tasks, err := parseTaskList(breakdownOutput)
	if err != nil {
		return fmt.Errorf("pipeline: parse task list: %w", err)
	}
	logger.Info().Int("tasks", len(tasks)).Msg("pipeline: breakdown tasks parsed")

	// ── Step 5: Checklist ─────────────────────────────────────────────────────
	logger.Info().Msg("pipeline: step 5 — ensuring checklist")
	checklist, err := s.trello.EnsureChecklist(ctx, card.ID)
	if err != nil {
		return fmt.Errorf("pipeline: ensure checklist: %w", err)
	}

	titles := make([]string, len(tasks))
	for i, t := range tasks {
		titles[i] = fmt.Sprintf("[%s][%s] %s", t.Role, t.Complexity, t.Title)
	}
	itemIDs, err := s.trello.PopulateChecklist(ctx, checklist.ID, titles)
	if err != nil {
		return fmt.Errorf("pipeline: populate checklist: %w", err)
	}
	logger.Info().Int("items", len(itemIDs)).Msg("pipeline: checklist populated")

	// ── Step 6: Execute tasks ─────────────────────────────────────────────────
	logger.Info().Msg("pipeline: step 6 — executing tasks")
	doneCount := 0
	for i, task := range tasks {
		itemLabel := titles[i]
		itemID := itemIDs[itemLabel]

		taskLogger := logger.With().
			Str("role", task.Role).
			Str("title", task.Title).
			Str("item_id", itemID).
			Logger()
		taskLogger.Info().Msg("pipeline: starting task")

		taskStart := time.Now()
		s.notifyStarted(ctx, task)

		output, resp, taskErr := s.callLLM(ctx, modelForRole(task.Role),
			roleSystemPrompt(task.Role),
			fmt.Sprintf("**Task:** %s\n**Complexity:** %s\n\n**App Context:**\n%s",
				task.Title, task.Complexity, ideaOutput))

		if taskErr != nil {
			taskLogger.Error().Err(taskErr).Msg("pipeline: task failed")
			s.notifyFailed(ctx, task, taskErr.Error())
			continue
		}

		durationMs := time.Since(taskStart).Milliseconds()
		taskLogger.Info().Int64("duration_ms", durationMs).Msg("pipeline: task done")

		// Mark checklist item complete
		if itemID != "" {
			if cerr := s.trello.SetCheckItemState(ctx, card.ID, itemID, true); cerr != nil {
				taskLogger.Warn().Err(cerr).Msg("pipeline: failed to mark checklist item")
			}
		}

		s.notifyDone(ctx, task, resp, durationMs)
		doneCount++

		// GitHub PR for coding tasks
		if task.Role == "coding" && s.github != nil {
			baseBranch := "main"
			prBody := fmt.Sprintf("## Task\n%s\n\n## Output\n```\n%s\n```", task.Title, truncate(output, 1000))
			pr, prErr := s.github.CreateFeaturePR(ctx, fmt.Sprintf("task-%d", i+1), task.Title, baseBranch, prBody)
			if prErr != nil {
				taskLogger.Warn().Err(prErr).Msg("pipeline: failed to create GitHub PR")
			} else {
				taskLogger.Info().Str("pr_url", pr.HTMLURL).Int("pr_number", pr.Number).Msg("pipeline: PR created")
				if s.telegram != nil {
					_ = s.telegram.NotifyPRCreated(ctx, fmt.Sprintf("task-%d", i+1), pr.Title, pr.HTMLURL, pr.Number)
				}
			}
		}
	}

	// ── Step 7: Final notification ────────────────────────────────────────────
	total := len(tasks)
	logger.Info().Int("done", doneCount).Int("total", total).Msg("pipeline: all tasks processed")

	if s.telegram != nil {
		if doneCount == total {
			_ = s.telegram.NotifyChecklistComplete(ctx, card.Name, card.ShortURL, doneCount)
		} else {
			_ = s.telegram.SendRaw(ctx, fmt.Sprintf(
				"⚠️ <b>Pipeline Partial</b>\nCard: <a href=\"%s\">%s</a>\n%d/%d tasks succeeded",
				card.ShortURL, card.Name, doneCount, total,
			))
		}
	}

	return nil
}

// ─── LLM helpers ─────────────────────────────────────────────────────────────

type callResult struct {
	inputTokens  int64
	outputTokens int64
	costUSD      float64
}

func (s *Service) callLLM(ctx context.Context, model, system, userMsg string) (string, *callResult, error) {
	req := llm.Request{
		Model:     model,
		System:    system,
		Messages:  []llm.Message{{Role: "user", Content: userMsg}},
		MaxTokens: maxTokensForModel(model),
		TaskID:    "pipeline-" + model + "-" + fmt.Sprintf("%d", time.Now().UnixNano()),
	}
	resp, err := s.llm.Call(ctx, req)
	if err != nil {
		return "", nil, err
	}
	return resp.Content, &callResult{
		inputTokens:  resp.InputTokens,
		outputTokens: resp.OutputTokens,
		costUSD:      resp.CostUSD,
	}, nil
}

func maxTokensForModel(model string) int {
	switch model {
	case "opus":
		return 4096
	case "sonnet":
		return 4096
	case "minimax":
		return 8192
	case "glm5", "glm-flash":
		return 4096
	default:
		return 2048
	}
}

func modelForRole(role string) string {
	switch role {
	case "coding":
		return "minimax"
	case "test":
		return "glm5"
	case "docs":
		return "glm-flash"
	case "review":
		return "sonnet"
	default:
		return "sonnet"
	}
}

// ─── Notification helpers ─────────────────────────────────────────────────────

func (s *Service) notifyStarted(ctx context.Context, t pipelineTask) {
	if s.telegram == nil {
		return
	}
	_ = s.telegram.NotifyTaskStarted(ctx, "pipeline", t.Role+"-"+t.Title, t.Title, modelForRole(t.Role))
}

func (s *Service) notifyDone(ctx context.Context, t pipelineTask, resp *callResult, durationMs int64) {
	if s.telegram == nil {
		return
	}
	_ = s.telegram.NotifyTaskDone(ctx, "pipeline", t.Role, t.Title,
		resp.inputTokens, resp.outputTokens, resp.costUSD, durationMs)
}

func (s *Service) notifyFailed(ctx context.Context, t pipelineTask, reason string) {
	if s.telegram == nil {
		return
	}
	_ = s.telegram.NotifyTaskFailed(ctx, "pipeline", t.Role, t.Title, reason)
}

// ─── Prompts ──────────────────────────────────────────────────────────────────

func ideaSystemPrompt() string {
	return `You are an expert product strategist and app ideation agent.
Analyze the given app brief and generate a structured app concept with:
- Overview (2–3 sentences)
- Target users
- Core features (max 5, bullet points)
- Recommended tech stack
- Risks
Be concise and actionable.`
}

func breakdownSystemPrompt() string {
	return `You are a technical project manager and sprint planner.
Break down the given app concept into a flat list of up to 10 tasks.
Return ONLY a valid JSON array. Each element must have:
  "title"      — short task title
  "role"        — one of: coding, test, docs, review
  "complexity"  — one of: S, M, L

Example:
[
  {"title":"Set up project scaffolding","role":"coding","complexity":"S"},
  {"title":"Write unit tests for auth","role":"test","complexity":"M"}
]`
}

func roleSystemPrompt(role string) string {
	prompts := map[string]string{
		"coding": `You are an expert Go/Flutter engineer. Implement the feature described in the task.
Return implementation code with file paths as comments. No explanation outside code blocks.`,
		"test": `You are a Go testing expert. Write comprehensive table-driven tests for the described task.
Return only test code.`,
		"docs": `You are a technical writer. Generate clear markdown documentation for the described task.
Return only markdown.`,
		"review": `You are a senior code reviewer. Review the described task implementation for correctness,
security, and idiomatic style. Return a JSON review: {"approved": bool, "comments": [{"severity": "...", "message": "..."}]}`,
	}
	if p, ok := prompts[role]; ok {
		return p
	}
	return "Complete the assigned task accurately."
}

// ─── Task list parser ─────────────────────────────────────────────────────────

// parseTaskList extracts the JSON array from LLM output, stripping any
// markdown fences before parsing. Caps the result at 10 items.
func parseTaskList(llmOutput string) ([]pipelineTask, error) {
	// Strip markdown fences
	llmOutput = strings.ReplaceAll(llmOutput, "```json", "")
	llmOutput = strings.ReplaceAll(llmOutput, "```", "")

	start := strings.Index(llmOutput, "[")
	end := strings.LastIndex(llmOutput, "]")
	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no JSON array found in breakdown output")
	}
	raw := llmOutput[start : end+1]

	var tasks []pipelineTask
	if err := json.Unmarshal([]byte(raw), &tasks); err != nil {
		return nil, fmt.Errorf("parse task list JSON: %w", err)
	}
	if len(tasks) > 10 {
		tasks = tasks[:10]
	}
	return tasks, nil
}

// truncate limits a string to maxLen runes (for PR body snippets).
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
