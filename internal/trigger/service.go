// Package trigger implements the AgentClaw pipeline triggered by POST /api/trigger.
//
// Flow:
//  1. Fetch the Trello card by ticket_id — 404 if not found.
//  2. Idea Agent (opus)      — reads card title+desc, generates structured app concept.
//  3. Breakdown Agent (sonnet) — breaks concept into max-10 flat tasks.
//  4. Trello Writer          — creates "AgentClaw Tasks" checklist; adds one item per task.
//  5. Coding/Test/Docs Agents (parallel) — each picks up its task; marks checklist item complete on finish.
//  6. Telegram notification  — sent when all checklist items are marked complete.
//
// Environment variables required:
//
//	TRELLO_KEY, TRELLO_TOKEN — passed into trello.Client
//	TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID — Telegram notification
package trigger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/patricksign/agentclaw/internal/llm"
	"github.com/patricksign/agentclaw/internal/queue"
	"github.com/patricksign/agentclaw/internal/integrations/trello"
	"github.com/rs/zerolog/log"
)

// Service orchestrates the agent pipeline triggered by a Trello card.
type Service struct {
	trello         *trello.Client
	queue          *queue.Queue
	telegramToken  string
	telegramChatID string
	router         *llm.Router
}

// NewService creates a Service. trelloClient may be nil if Trello is not configured
// (the trigger will fail gracefully at runtime).
func NewService(
	tc *trello.Client,
	q *queue.Queue,
	telegramToken, telegramChatID string,
) *Service {
	return &Service{
		trello:         tc,
		queue:          q,
		telegramToken:  telegramToken,
		telegramChatID: telegramChatID,
		router:         llm.NewRouter(),
	}
}

// ─── Task description from breakdown ─────────────────────────────────────────

type breakdownTask struct {
	Title      string `json:"title"`
	AgentRole  string `json:"agent_role"` // coding | test | docs
	Complexity string `json:"complexity"` // S | M | L
}

// ─── Run ─────────────────────────────────────────────────────────────────────

// Run executes the full pipeline for the given Trello card.
// Intended to be called in a goroutine — it blocks until the pipeline finishes
// or ctx is cancelled.
func (s *Service) Run(ctx context.Context, boardID, ticketID string) error {
	logger := log.With().Str("ticket_id", ticketID).Str("board_id", boardID).Logger()

	if s.trello == nil {
		return fmt.Errorf("trigger: Trello client not configured")
	}

	// ── Step 0: Fetch card ────────────────────────────────────────────────────
	logger.Info().Msg("trigger: fetching Trello card")
	card, err := s.trello.GetCard(ctx, ticketID)
	if err != nil {
		return fmt.Errorf("trigger: card not found: %w", err)
	}
	logger.Info().Str("card_id", card.ID).Str("name", card.Name).Msg("trigger: card found")

	// ── Step 1: Idea Agent ────────────────────────────────────────────────────
	logger.Info().Msg("trigger: step 1 — idea agent (opus)")
	ideaOutput, ideaTokens, err := s.callLLM(ctx, "opus", ideaSystemPrompt(), buildIdeaMessage(card))
	if err != nil {
		return fmt.Errorf("trigger: idea agent: %w", err)
	}
	logger.Info().
		Str("card_id", card.ID).
		Str("agent", "idea").
		Int64("tokens", ideaTokens).
		Msg("trigger: idea agent complete")

	// ── Step 2: Breakdown Agent ───────────────────────────────────────────────
	logger.Info().Msg("trigger: step 2 — breakdown agent (sonnet)")
	breakdownOutput, breakdownTokens, err := s.callLLM(ctx, "sonnet", breakdownSystemPrompt(), buildBreakdownMessage(ideaOutput))
	if err != nil {
		return fmt.Errorf("trigger: breakdown agent: %w", err)
	}
	logger.Info().
		Str("card_id", card.ID).
		Str("agent", "breakdown").
		Int64("tokens", breakdownTokens).
		Msg("trigger: breakdown agent complete")

	tasks, err := parseBreakdownTasks(breakdownOutput)
	if err != nil {
		return fmt.Errorf("trigger: parse breakdown: %w", err)
	}
	logger.Info().Int("task_count", len(tasks)).Msg("trigger: breakdown tasks parsed")

	// ── Step 3: Trello Writer ─────────────────────────────────────────────────
	logger.Info().Msg("trigger: step 3 — creating Trello checklist")
	checklist, err := s.trello.CreateChecklist(ctx, card.ID, "AgentClaw Tasks")
	if err != nil {
		return fmt.Errorf("trigger: create checklist: %w", err)
	}
	logger.Info().Str("checklist_id", checklist.ID).Msg("trigger: checklist created")

	// Add one check item per task; collect item IDs for later completion.
	type checkEntry struct {
		task   breakdownTask
		itemID string
	}
	entries := make([]checkEntry, 0, len(tasks))
	for _, t := range tasks {
		label := fmt.Sprintf("[%s][%s] %s", t.AgentRole, t.Complexity, t.Title)
		item, err := s.trello.AddCheckItem(ctx, checklist.ID, label)
		if err != nil {
			logger.Error().Err(err).Str("title", t.Title).Msg("trigger: failed to add checklist item")
			continue
		}
		entries = append(entries, checkEntry{task: t, itemID: item.ID})
		logger.Info().Str("item_id", item.ID).Str("title", t.Title).Msg("trigger: checklist item added")
	}

	// ── Step 4: Coding/Test/Docs Agents in parallel ───────────────────────────
	logger.Info().Msg("trigger: step 4 — running coding/test/docs agents in parallel")

	var wg sync.WaitGroup
	completedCh := make(chan string, len(entries)) // emits itemIDs as they complete

	for _, e := range entries {
		wg.Add(1)
		go func(entry checkEntry) {
			defer wg.Done()
			agentID := entry.task.AgentRole + "-trigger-" + uuid.New().String()[:8]
			taskLogger := logger.With().
				Str("agent_id", agentID).
				Str("role", entry.task.AgentRole).
				Str("title", entry.task.Title).
				Logger()

			taskLogger.Info().Msg("trigger: agent starting task")
			_, tokens, err := s.callLLM(ctx, modelForRole(entry.task.AgentRole),
				roleSystemPrompt(entry.task.AgentRole),
				buildTaskMessage(entry.task, ideaOutput))
			if err != nil {
				taskLogger.Error().Err(err).Msg("trigger: agent task failed")
				return
			}
			taskLogger.Info().Int64("tokens", tokens).Msg("trigger: agent task complete")

			// Mark checklist item complete
			if cerr := s.trello.CompleteCheckItem(ctx, card.ID, entry.itemID); cerr != nil {
				taskLogger.Error().Err(cerr).Msg("trigger: failed to complete checklist item")
				return
			}
			taskLogger.Info().Str("item_id", entry.itemID).Msg("trigger: checklist item marked complete")
			completedCh <- entry.itemID
		}(e)
	}

	wg.Wait()
	close(completedCh)

	// ── Step 5: Telegram notification ─────────────────────────────────────────
	completed := 0
	for range completedCh {
		completed++
	}
	logger.Info().Int("completed", completed).Int("total", len(entries)).Msg("trigger: all agents done")

	msg := fmt.Sprintf(
		"✅ AgentClaw pipeline complete for card *%s*\n%d/%d tasks finished\n%s",
		escapeMarkdown(card.Name), completed, len(entries), card.ShortURL,
	)
	if err := s.sendTelegram(ctx, msg); err != nil {
		logger.Error().Err(err).Msg("trigger: telegram notification failed")
	} else {
		logger.Info().Msg("trigger: telegram notification sent")
	}

	return nil
}

// ─── LLM call ────────────────────────────────────────────────────────────────

// callLLM calls the Anthropic API directly (no agent pool) with the given model.
// Returns (content, totalTokens, error).
func (s *Service) callLLM(ctx context.Context, model, system, userMsg string) (string, int64, error) {
	req := llm.Request{
		Model:     model,
		System:    system,
		Messages:  []llm.Message{{Role: "user", Content: userMsg}},
		MaxTokens: 4096,
		TaskID:    "trigger-" + model + "-" + fmt.Sprintf("%d", time.Now().UnixNano()),
	}
	resp, err := s.router.Call(ctx, req)
	if err != nil {
		return "", 0, err
	}
	return resp.Content, resp.InputTokens + resp.OutputTokens, nil
}

// ─── Prompts ──────────────────────────────────────────────────────────────────

func ideaSystemPrompt() string {
	return `You are an expert product strategist and app ideation agent.
Analyze the given app brief and generate a structured app concept with:
- Overview (2–3 sentences)
- Core features (max 5, bullet points)
- Recommended tech stack
Be concise and actionable.`
}

func buildIdeaMessage(card *trello.BoardCard) string {
	return fmt.Sprintf("**App Idea:**\nTitle: %s\n\nDescription:\n%s", card.Name, card.Desc)
}

func breakdownSystemPrompt() string {
	return `You are a technical project manager and sprint planner.
Break down the given app concept into a flat list of up to 10 tasks.
Return ONLY a valid JSON array. Each element must have:
  "title"      — short task title
  "agent_role" — one of: coding, test, docs
  "complexity" — one of: S, M, L

Example:
[
  {"title":"Set up project scaffolding","agent_role":"coding","complexity":"S"},
  {"title":"Write unit tests for auth","agent_role":"test","complexity":"M"}
]`
}

func buildBreakdownMessage(ideaOutput string) string {
	return fmt.Sprintf("App concept:\n\n%s\n\nGenerate the task list now.", ideaOutput)
}

func roleSystemPrompt(role string) string {
	prompts := map[string]string{
		"coding": `You are an expert Go/Flutter engineer. Implement the feature described in the task.
Return implementation code with file paths as comments. No explanation outside code blocks.`,
		"test": `You are a Go testing expert. Write comprehensive table-driven tests for the described task.
Return only test code.`,
		"docs": `You are a technical writer. Generate clear markdown documentation for the described task.
Return only markdown.`,
	}
	if p, ok := prompts[role]; ok {
		return p
	}
	return "Complete the assigned task accurately."
}

func buildTaskMessage(t breakdownTask, ideaContext string) string {
	return fmt.Sprintf("**Task:** %s\n**Complexity:** %s\n\n**App Context:**\n%s",
		t.Title, t.Complexity, ideaContext)
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

// ─── Breakdown parser ─────────────────────────────────────────────────────────

func parseBreakdownTasks(llmOutput string) ([]breakdownTask, error) {
	start := strings.Index(llmOutput, "[")
	end := strings.LastIndex(llmOutput, "]")
	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no JSON array found in breakdown output")
	}
	raw := llmOutput[start : end+1]

	var tasks []breakdownTask
	if err := json.Unmarshal([]byte(raw), &tasks); err != nil {
		return nil, fmt.Errorf("parse breakdown JSON: %w", err)
	}
	// Cap at 10
	if len(tasks) > 10 {
		tasks = tasks[:10]
	}
	return tasks, nil
}

// ─── Telegram ─────────────────────────────────────────────────────────────────

const telegramAPIBase = "https://api.telegram.org"

func (s *Service) sendTelegram(ctx context.Context, text string) error {
	if s.telegramToken == "" || s.telegramChatID == "" {
		log.Debug().Msg("trigger: Telegram not configured — skipping notification")
		return nil
	}

	params := url.Values{}
	params.Set("chat_id", s.telegramChatID)
	params.Set("text", text)
	params.Set("parse_mode", "Markdown")

	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", telegramAPIBase, s.telegramToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		bytes.NewBufferString(params.Encode()))
	if err != nil {
		return fmt.Errorf("telegram: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: HTTP: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram: API %d: %s", resp.StatusCode, raw)
	}
	return nil
}

func escapeMarkdown(s string) string {
	replacer := strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"`", "\\`",
	)
	return replacer.Replace(s)
}

