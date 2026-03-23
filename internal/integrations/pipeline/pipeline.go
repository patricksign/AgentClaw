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
//  6. For each task: dispatch via event bus, wait for result,
//     mark checklist item, optionally create GitHub PR.
//  7. Final event (complete or partial summary).
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
	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/integrations/github"
	"github.com/patricksign/AgentClaw/internal/integrations/trello"
	"github.com/patricksign/AgentClaw/internal/port"
	"log/slog"
)

// Service orchestrates the AgentClaw agent pipeline.
// Uses port interfaces — no direct dependency on agent/, queue/, telegram/, slack/.
type Service struct {
	trello     *trello.Client
	github     *github.Client
	dispatcher port.TaskDispatcher
	waiter     port.TaskResultWaiter
	eventBus   port.DomainEventBus
}

// NewService wires up the pipeline with port-based dependencies.
// Trello and GitHub clients are external API integrations (acceptable direct deps).
func NewService(
	tc *trello.Client,
	gh *github.Client,
	dispatcher port.TaskDispatcher,
	waiter port.TaskResultWaiter,
	eventBus port.DomainEventBus,
) *Service {
	return &Service{
		trello:     tc,
		github:     gh,
		dispatcher: dispatcher,
		waiter:     waiter,
		eventBus:   eventBus,
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
// All task submission goes through port.TaskDispatcher; results via port.TaskResultWaiter.
// Notifications are event-driven: subscribers on DomainEventBus handle Telegram/Slack.
func (s *Service) Run(ctx context.Context, boardID, ticketID string) error {
	if s.trello == nil || !s.trello.IsConfigured() {
		return fmt.Errorf("pipeline: Trello client not configured")
	}

	// Publish pipeline start event.
	s.publishEvent(domain.EventPipelineStarted, "", map[string]string{
		"board_id":  boardID,
		"ticket_id": ticketID,
	})

	// ── Step 1: Fetch card ────────────────────────────────────────────────────
	slog.Info("pipeline: fetching Trello card", "ticket_id", ticketID, "board_id", boardID)
	card, err := s.trello.GetCard(ctx, ticketID)
	if err != nil {
		return fmt.Errorf("pipeline: card not found: %w", err)
	}
	slog.Info("pipeline: card found", "ticket_id", ticketID, "board_id", boardID, "card_id", card.ID, "name", card.Name)

	// ── Step 2: Idea Agent ────────────────────────────────────────────────────
	slog.Info("pipeline: step 2 — idea agent", "ticket_id", ticketID, "board_id", boardID)
	ideaTask := &domain.Task{
		ID:          "pipeline-idea-" + uuid.New().String()[:8],
		Title:       card.Name,
		Description: fmt.Sprintf("**App Idea:**\nTitle: %s\n\nDescription:\n%s", card.Name, card.Desc),
		AgentRole:   "idea",
		Complexity:  "M",
		Status:      "pending",
		CreatedAt:   time.Now(),
	}
	ideaOutput, err := s.dispatchAndWait(ctx, ideaTask)
	if err != nil {
		return fmt.Errorf("pipeline: idea agent: %w", err)
	}
	slog.Info("pipeline: idea agent complete", "ticket_id", ticketID, "board_id", boardID, "task_id", ideaTask.ID)

	// ── Step 3: Append concept to card description ────────────────────────────
	slog.Info("pipeline: step 3 — appending concept to card", "ticket_id", ticketID, "board_id", boardID)
	newDesc := card.Desc
	if newDesc != "" {
		newDesc += "\n\n---\n\n"
	}
	newDesc += "## AgentClaw Concept\n\n" + ideaOutput
	if err := s.trello.UpdateCardDescription(ctx, card.ID, newDesc); err != nil {
		slog.Warn("pipeline: failed to update card description — continuing", "err", err, "ticket_id", ticketID, "board_id", boardID)
	}

	// ── Step 4: Breakdown Agent ───────────────────────────────────────────────
	slog.Info("pipeline: step 4 — breakdown agent", "ticket_id", ticketID, "board_id", boardID)
	breakdownTask := &domain.Task{
		ID:          "pipeline-breakdown-" + uuid.New().String()[:8],
		Title:       "Breakdown: " + card.Name,
		Description: fmt.Sprintf("App concept:\n\n%s\n\nGenerate the task list now.", ideaOutput),
		AgentRole:   "breakdown",
		Complexity:  "M",
		Status:      "pending",
		CreatedAt:   time.Now(),
	}
	breakdownOutput, err := s.dispatchAndWait(ctx, breakdownTask)
	if err != nil {
		return fmt.Errorf("pipeline: breakdown agent: %w", err)
	}

	tasks, err := parseTaskList(breakdownOutput)
	if err != nil {
		return fmt.Errorf("pipeline: parse task list: %w", err)
	}
	slog.Info("pipeline: breakdown tasks parsed", "ticket_id", ticketID, "board_id", boardID, "tasks", len(tasks))

	// ── Step 5: Checklist ─────────────────────────────────────────────────────
	slog.Info("pipeline: step 5 — ensuring checklist", "ticket_id", ticketID, "board_id", boardID)
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
	slog.Info("pipeline: checklist populated", "ticket_id", ticketID, "board_id", boardID, "items", len(itemIDs))

	// ── Step 6: Execute tasks via event-driven dispatch ───────────────────────
	slog.Info("pipeline: step 6 — dispatching tasks", "ticket_id", ticketID, "board_id", boardID)
	type taskEntry struct {
		pt     pipelineTask
		dTask  *domain.Task
		itemID string
	}
	entries := make([]taskEntry, 0, len(tasks))
	for i, pt := range tasks {
		dTask := &domain.Task{
			ID:          fmt.Sprintf("pipeline-%s-%s", pt.Role, uuid.New().String()[:8]),
			Title:       pt.Title,
			Description: fmt.Sprintf("**Task:** %s\n**Complexity:** %s\n\n**App Context:**\n%s", pt.Title, pt.Complexity, ideaOutput),
			AgentRole:   pt.Role,
			Complexity:  pt.Complexity,
			Status:      "pending",
			CreatedAt:   time.Now(),
		}
		entries = append(entries, taskEntry{
			pt:     pt,
			dTask:  dTask,
			itemID: itemIDs[titles[i]],
		})
	}

	doneCount := 0
	for i, entry := range entries {
		// Short-circuit if context is already cancelled (e.g., shutdown).
		if ctx.Err() != nil {
			slog.Info("pipeline: context cancelled — stopping task dispatch", "err", ctx.Err(), "ticket_id", ticketID, "board_id", boardID)
			break
		}

		slog.Info("pipeline: dispatching task", "ticket_id", ticketID, "board_id", boardID, "task_id", entry.dTask.ID, "role", entry.pt.Role, "title", entry.pt.Title)

		taskStart := time.Now()

		// Publish task started event — subscribers handle notification.
		s.publishEvent(domain.EventTaskStarted, entry.dTask.ID, map[string]string{
			"title": entry.pt.Title,
			"role":  entry.pt.Role,
		})

		output, taskErr := s.dispatchAndWait(ctx, entry.dTask)
		if taskErr != nil {
			slog.Error("pipeline: task failed", "err", taskErr, "ticket_id", ticketID, "board_id", boardID, "task_id", entry.dTask.ID, "role", entry.pt.Role, "title", entry.pt.Title)
			s.publishEvent(domain.EventTaskFailed, entry.dTask.ID, map[string]string{
				"title":  entry.pt.Title,
				"role":   entry.pt.Role,
				"reason": taskErr.Error(),
			})
			continue
		}

		durationMs := time.Since(taskStart).Milliseconds()
		slog.Info("pipeline: task done", "ticket_id", ticketID, "board_id", boardID, "task_id", entry.dTask.ID, "role", entry.pt.Role, "title", entry.pt.Title, "duration_ms", durationMs)

		if entry.itemID != "" {
			if cerr := s.trello.SetCheckItemState(ctx, card.ID, entry.itemID, true); cerr != nil {
				slog.Warn("pipeline: failed to mark checklist item", "err", cerr, "ticket_id", ticketID, "board_id", boardID, "task_id", entry.dTask.ID)
			}
		}

		s.publishEvent(domain.EventTaskDone, entry.dTask.ID, map[string]string{
			"title":       entry.pt.Title,
			"role":        entry.pt.Role,
			"duration_ms": fmt.Sprintf("%d", durationMs),
		})
		doneCount++

		// GitHub PR for coding tasks.
		if entry.pt.Role == "coding" && s.github != nil {
			baseBranch := "main"
			prBody := fmt.Sprintf("## Task\n%s\n\n## Output\n```\n%s\n```", entry.pt.Title, truncate(output, 1000))
			pr, prErr := s.github.CreateFeaturePR(ctx, fmt.Sprintf("task-%d", i+1), entry.pt.Title, baseBranch, prBody)
			if prErr != nil {
				slog.Warn("pipeline: failed to create GitHub PR", "err", prErr, "ticket_id", ticketID, "board_id", boardID, "task_id", entry.dTask.ID)
			} else {
				slog.Info("pipeline: PR created", "ticket_id", ticketID, "board_id", boardID, "task_id", entry.dTask.ID, "pr_url", pr.HTMLURL, "pr_number", pr.Number)
				s.publishEvent(domain.EventPRCreated, entry.dTask.ID, map[string]string{
					"pr_url":    pr.HTMLURL,
					"pr_number": fmt.Sprintf("%d", pr.Number),
					"pr_title":  pr.Title,
					"branch":    fmt.Sprintf("task-%d", i+1),
				})
			}
		}
	}

	// ── Step 7: Final event ──────────────────────────────────────────────────
	total := len(tasks)
	slog.Info("pipeline: all tasks processed", "ticket_id", ticketID, "board_id", boardID, "done", doneCount, "total", total)

	if doneCount == total {
		s.publishEvent(domain.EventPipelineCompleted, "", map[string]string{
			"card_name": card.Name,
			"card_url":  card.ShortURL,
			"done":      fmt.Sprintf("%d", doneCount),
			"total":     fmt.Sprintf("%d", total),
		})
	} else {
		s.publishEvent(domain.EventPipelinePartial, "", map[string]string{
			"card_name": card.Name,
			"card_url":  card.ShortURL,
			"done":      fmt.Sprintf("%d", doneCount),
			"total":     fmt.Sprintf("%d", total),
		})
	}

	return nil
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

// preRegisterWaiter is an optional interface for race-free wait registration.
// If the waiter supports it, we register BEFORE dispatching so no events are lost.
type preRegisterWaiter interface {
	RegisterAndWait(ctx context.Context, taskID string) func() (*domain.TaskResult, error)
}

// dispatchAndWait registers a wait channel BEFORE dispatching to prevent the race
// where the task completes before the waiter is listening.
func (s *Service) dispatchAndWait(ctx context.Context, task *domain.Task) (string, error) {
	// Register the waiter synchronously BEFORE dispatch.
	// This guarantees the wait channel exists when the event arrives.
	var waitFn func() (*domain.TaskResult, error)
	if prw, ok := s.waiter.(preRegisterWaiter); ok {
		waitFn = prw.RegisterAndWait(ctx, task.ID)
	}

	// Dispatch the task.
	if err := s.dispatcher.Dispatch(ctx, task); err != nil {
		return "", fmt.Errorf("dispatch: %w", err)
	}

	// Wait for the result.
	var result *domain.TaskResult
	var err error
	if waitFn != nil {
		result, err = waitFn()
	} else {
		// Fallback for waiter implementations without pre-registration.
		result, err = s.waiter.WaitForResult(ctx, task.ID)
	}
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return result.Output, nil
}

// publishEvent is a convenience wrapper for bus.Publish.
func (s *Service) publishEvent(evtType domain.EventType, taskID string, payload map[string]string) {
	if s.eventBus == nil {
		return
	}
	s.eventBus.Publish(domain.Event{
		Type:       evtType,
		Channel:    domain.StatusChannel,
		TaskID:     taskID,
		Payload:    payload,
		OccurredAt: time.Now(),
	})
}

// ─── Task list parser ─────────────────────────────────────────────────────────

// parseTaskList extracts the JSON array from LLM output, stripping any
// markdown fences before parsing. Caps the result at 10 items.
func parseTaskList(llmOutput string) ([]pipelineTask, error) {
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
