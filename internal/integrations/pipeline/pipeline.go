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

	"github.com/google/uuid"
	"github.com/patricksign/AgentClaw/internal/agent"
	"github.com/patricksign/AgentClaw/internal/integrations/github"
	"github.com/patricksign/AgentClaw/internal/integrations/slack"
	"github.com/patricksign/AgentClaw/internal/integrations/telegram"
	"github.com/patricksign/AgentClaw/internal/integrations/trello"
	"github.com/patricksign/AgentClaw/internal/queue"
	"github.com/rs/zerolog/log"
)

// Service orchestrates the AgentClaw agent pipeline.
type Service struct {
	trello   *trello.Client
	telegram *telegram.Client
	slack    *slack.SlackClient
	github   *github.Client
	// exec, q, and bus route tasks through the shared Pool/Queue/Executor
	// instead of calling the LLM directly. All three must be non-nil for
	// the queue-backed path to activate; if any is nil the service falls
	// back to the direct-LLM path so it remains usable in tests.
	exec *agent.Executor
	q    *queue.Queue
	bus  *agent.EventBus
}

// NewService wires up all integration clients from environment variables.
// Clients that are not configured (missing env vars) are left nil and their
// pipeline steps are skipped silently.
//
// exec, q, and bus must be the same instances used by the main queue workers
// so that tasks submitted here are executed by the Pool agents and tracked in
// the shared memory store.
func NewService(tc *trello.Client, exec *agent.Executor, q *queue.Queue, bus *agent.EventBus) *Service {
	s := &Service{
		trello: tc,
		exec:   exec,
		q:      q,
		bus:    bus,
	}

	if tg, err := telegram.New(); err == nil {
		s.telegram = tg
	} else if telegram.IsEnvPresent() {
		log.Warn().Err(err).Msg("pipeline: Telegram env vars present but client init failed")
	}
	if sl, err := slack.NewSlackClient(); err == nil {
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

// submitAndWait submits a task to the shared queue, waits for it to complete
// (via EventBus), and returns the LLM output captured in TaskResult.Output.
//
// This replaces the previous direct s.callLLM() calls so that every pipeline
// task is executed by Pool agents, tracked in the memory store, and visible on
// the WebSocket dashboard — exactly like tasks submitted via POST /api/tasks.
func (s *Service) submitAndWait(ctx context.Context, t *agent.Task) (string, error) {
	subID := "pipeline-waiter-" + uuid.New().String()[:8]
	ch, unsub := s.bus.Subscribe(subID)
	defer unsub()

	s.q.Push(t)
	_ = s.exec // ensure exec is wired (Push alone triggers the worker)

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case evt, ok := <-ch:
			if !ok {
				return "", fmt.Errorf("pipeline: event bus closed before task %s completed", t.ID)
			}
			if evt.TaskID != t.ID {
				continue
			}
			switch evt.Type {
			case agent.EvtTaskDone:
				if result, ok := evt.Payload.(*agent.TaskResult); ok && result != nil {
					return result.Output, nil
				}
				return "", nil
			case agent.EvtTaskFailed:
				reason := fmt.Sprintf("%v", evt.Payload)
				return "", fmt.Errorf("task %s failed: %s", t.ID, reason)
			}
		}
	}
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
// All LLM calls are routed through the shared Pool/Queue/Executor so that
// every task is memory-tracked, token-logged, and visible on the dashboard.
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

	// ── Step 2: Idea Agent ────────────────────────────────────────────────────
	logger.Info().Msg("pipeline: step 2 — idea agent")
	ideaTask := &agent.Task{
		ID:          "pipeline-idea-" + uuid.New().String()[:8],
		Title:       card.Name,
		Description: fmt.Sprintf("**App Idea:**\nTitle: %s\n\nDescription:\n%s", card.Name, card.Desc),
		AgentRole:   "idea",
		Complexity:  "M",
		Priority:    agent.PriorityHigh,
		Status:      agent.TaskPending,
		CreatedAt:   time.Now(),
		Meta:        map[string]string{"source": "pipeline", "trello_card_id": card.ID},
	}
	ideaOutput, err := s.submitAndWait(ctx, ideaTask)
	if err != nil {
		return fmt.Errorf("pipeline: idea agent: %w", err)
	}
	logger.Info().Str("task_id", ideaTask.ID).Msg("pipeline: idea agent complete")

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

	// ── Step 4: Breakdown Agent ───────────────────────────────────────────────
	logger.Info().Msg("pipeline: step 4 — breakdown agent")
	breakdownTask := &agent.Task{
		ID:          "pipeline-breakdown-" + uuid.New().String()[:8],
		Title:       "Breakdown: " + card.Name,
		Description: fmt.Sprintf("App concept:\n\n%s\n\nGenerate the task list now.", ideaOutput),
		AgentRole:   "breakdown",
		Complexity:  "M",
		Priority:    agent.PriorityHigh,
		Status:      agent.TaskPending,
		CreatedAt:   time.Now(),
		Meta:        map[string]string{"source": "pipeline", "trello_card_id": card.ID},
	}
	breakdownOutput, err := s.submitAndWait(ctx, breakdownTask)
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

	// ── Step 6: Execute tasks via Pool/Queue ──────────────────────────────────
	logger.Info().Msg("pipeline: step 6 — submitting tasks to queue")
	type taskEntry struct {
		pt     pipelineTask
		qTask  *agent.Task
		itemID string
	}
	entries := make([]taskEntry, 0, len(tasks))
	for i, pt := range tasks {
		qTask := &agent.Task{
			ID:          fmt.Sprintf("pipeline-%s-%s", pt.Role, uuid.New().String()[:8]),
			Title:       pt.Title,
			Description: fmt.Sprintf("**Task:** %s\n**Complexity:** %s\n\n**App Context:**\n%s", pt.Title, pt.Complexity, ideaOutput),
			AgentRole:   pt.Role,
			Complexity:  pt.Complexity,
			Priority:    agent.PriorityNormal,
			Status:      agent.TaskPending,
			CreatedAt:   time.Now(),
			Meta:        map[string]string{"source": "pipeline", "trello_card_id": card.ID},
		}
		entries = append(entries, taskEntry{
			pt:     pt,
			qTask:  qTask,
			itemID: itemIDs[titles[i]],
		})
	}

	doneCount := 0
	for i, entry := range entries {
		taskLogger := logger.With().
			Str("task_id", entry.qTask.ID).
			Str("role", entry.pt.Role).
			Str("title", entry.pt.Title).
			Logger()
		taskLogger.Info().Msg("pipeline: submitting task")

		taskStart := time.Now()
		s.notifyStarted(ctx, entry.pt)

		output, taskErr := s.submitAndWait(ctx, entry.qTask)
		if taskErr != nil {
			taskLogger.Error().Err(taskErr).Msg("pipeline: task failed")
			s.notifyFailed(ctx, entry.pt, taskErr.Error())
			continue
		}

		durationMs := time.Since(taskStart).Milliseconds()
		taskLogger.Info().Int64("duration_ms", durationMs).Msg("pipeline: task done")

		if entry.itemID != "" {
			if cerr := s.trello.SetCheckItemState(ctx, card.ID, entry.itemID, true); cerr != nil {
				taskLogger.Warn().Err(cerr).Msg("pipeline: failed to mark checklist item")
			}
		}

		s.notifyDone(ctx, entry.pt, durationMs)
		doneCount++

		// GitHub PR for coding tasks
		if entry.pt.Role == "coding" && s.github != nil {
			baseBranch := "main"
			prBody := fmt.Sprintf("## Task\n%s\n\n## Output\n```\n%s\n```", entry.pt.Title, truncate(output, 1000))
			pr, prErr := s.github.CreateFeaturePR(ctx, fmt.Sprintf("task-%d", i+1), entry.pt.Title, baseBranch, prBody)
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

// ─── Notification helpers ─────────────────────────────────────────────────────

func (s *Service) notifyStarted(ctx context.Context, t pipelineTask) {
	if s.telegram == nil {
		return
	}
	_ = s.telegram.NotifyTaskStarted(ctx, "pipeline", t.Role+"-"+t.Title, t.Title, t.Role)
}

func (s *Service) notifyDone(ctx context.Context, t pipelineTask, durationMs int64) {
	if s.telegram == nil {
		return
	}
	_ = s.telegram.NotifyTaskDone(ctx, "pipeline", t.Role, t.Title, 0, 0, 0, durationMs)
}

func (s *Service) notifyFailed(ctx context.Context, t pipelineTask, reason string) {
	if s.telegram == nil {
		return
	}
	_ = s.telegram.NotifyTaskFailed(ctx, "pipeline", t.Role, t.Title, reason)
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
