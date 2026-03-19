package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/patricksign/agentclaw/internal/agent"
	"github.com/patricksign/agentclaw/internal/api"
	"github.com/patricksign/agentclaw/internal/memory"
	"github.com/patricksign/agentclaw/internal/queue"
	"github.com/patricksign/agentclaw/internal/integrations/trello"
)

// maxTaskRetries is the fixed maximum number of retry attempts for a failed task.
// Must not be derived from task.Retries to avoid growing the limit on each failure.
const maxTaskRetries = 3

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
	log.Info().Msg("AgentClaw starting...")

	dbPath := getenv("DB_PATH", "./agentclaw.db")
	projectPath := getenv("PROJECT_PATH", "./memory/project.md")
	addr := getenv("ADDR", ":8080")

	// ─── Wire dependencies ───────────────────────────────────────────────────

	bus := agent.NewEventBus()

	mem, err := memory.New(dbPath, projectPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to init memory store")
	}
	log.Info().Str("db", dbPath).Msg("memory store ready")
	defer func() {
		if err := mem.Close(); err != nil {
			log.Error().Err(err).Msg("failed to close memory store")
		}
	}()

	// AgentFactory is injected into Pool so Restart() builds a fresh agent.
	factory := agent.AgentFactory(agent.NewBaseAgent)

	pool := agent.NewPool(bus, factory)
	q := queue.New()
	exec := agent.NewExecutor(pool, bus, mem)

	// ─── Spawn default agents ────────────────────────────────────────────────
	spawnDefaultAgents(pool)

	// ─── Start queue workers — one goroutine per role ────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// All 9 roles now have workers so tasks for every role are dequeued.
	roles := []string{
		"idea", "architect", "breakdown",
		"coding", "test", "review",
		"docs", "deploy", "notify",
	}
	for _, role := range roles {
		go runWorker(ctx, role, q, exec)
	}
	log.Info().Strs("roles", roles).Msg("queue workers started")

	// ─── Trello idea board poller ─────────────────────────────────────────────
	// When TRELLO_IDEA_BOARD_ID is set, a goroutine polls the board for new
	// cards and automatically submits them as idea + breakdown tasks.
	ideaBoardID := getenv("TRELLO_IDEA_BOARD_ID", "")
	doneListID := getenv("TRELLO_DONE_LIST_ID", "")
	if ideaBoardID != "" {
		trelloAPIKey := getenv("TRELLO_API_KEY", "")
		trelloToken := getenv("TRELLO_TOKEN", "")
		pollInterval := 30 * time.Second
		go pollTrelloIdeas(ctx, ideaBoardID, doneListID, trelloAPIKey, trelloToken, q, pollInterval)
		log.Info().Str("board", ideaBoardID).Msg("Trello idea board poller started")
	}

	// ─── Trello client for trigger endpoint ──────────────────────────────────
	trelloKey := getenv("TRELLO_KEY", getenv("TRELLO_API_KEY", ""))
	trelloToken := getenv("TRELLO_TOKEN", "")
	var triggerTrelloClient *trello.Client
	if trelloKey != "" && trelloToken != "" {
		triggerTrelloClient, err = trello.New(trelloKey, trelloToken)
		if err != nil {
			log.Warn().Err(err).Msg("trigger Trello client init failed — /api/trigger will be unavailable")
		}
	}

	// ─── HTTP + WebSocket API ─────────────────────────────────────────────────
	telegramToken := getenv("TELEGRAM_BOT_TOKEN", "")
	telegramChatID := getenv("TELEGRAM_CHAT_ID", "")
	srv := api.NewServer(pool, q, mem, bus, triggerTrelloClient, telegramToken, telegramChatID)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: srv.Handler(),
	}
	go func() {
		log.Info().Str("addr", addr).Msg("API server listening")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("server error")
		}
	}()

	// ─── Graceful shutdown ───────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("shutting down AgentClaw...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("HTTP shutdown error")
	}
	log.Info().Msg("AgentClaw stopped")
}

// runWorker polls the queue for a specific role and executes tasks.
// Runs for the lifetime of ctx.
func runWorker(ctx context.Context, role string, q *queue.Queue, exec *agent.Executor) {
	log.Info().Str("role", role).Msg("worker started")
	for {
		task, err := q.Pop(ctx, role)
		if err != nil {
			// context cancelled — clean shutdown
			return
		}
		if err := exec.Execute(ctx, task); err != nil {
			log.Error().Err(err).Str("task", task.ID).Str("role", role).Msg("execute error")
			q.MarkFailed(task, maxTaskRetries)
		} else {
			q.MarkDone(task.ID)
		}
	}
}

// spawnDefaultAgents initialises the default agent team.
// API keys are read from environment variables here and stored in each
// agent's Config.Env. To use a different key for a specific agent,
// replace getenv(...) with a literal string or a different env var name.
func spawnDefaultAgents(pool *agent.Pool) {
	anthropicKey := getenv("ANTHROPIC_API_KEY", "")
	minimaxKey := getenv("MINIMAX_API_KEY", "")
	glmKey := getenv("GLM_API_KEY", "")

	anthropicEnv := map[string]string{"ANTHROPIC_API_KEY": anthropicKey}
	minimaxEnv := map[string]string{"MINIMAX_API_KEY": minimaxKey}
	glmEnv := map[string]string{"GLM_API_KEY": glmKey}

	// Trello integration — breakdown agent creates cards automatically.
	// Set these env vars to enable:
	//   TRELLO_API_KEY  — your Trello Power-Up key (https://trello.com/app-key)
	//   TRELLO_TOKEN    — your Trello user token
	//   TRELLO_LIST_ID  — the list (column) ID to push cards into
	// If any are missing, card creation is silently skipped.
	breakdownEnv := map[string]string{
		"ANTHROPIC_API_KEY": anthropicKey,
		"TRELLO_API_KEY":    getenv("TRELLO_API_KEY", ""),
		"TRELLO_TOKEN":      getenv("TRELLO_TOKEN", ""),
		"TRELLO_LIST_ID":    getenv("TRELLO_LIST_ID", ""),
	}

	defaults := []agent.Config{
		{ID: "idea-agent-01", Name: "Idea Agent", Role: "idea", Model: "opus", MaxRetries: maxTaskRetries, TimeoutSecs: 120, Env: anthropicEnv},
		{ID: "architect-01", Name: "Architect", Role: "architect", Model: "opus", MaxRetries: maxTaskRetries, TimeoutSecs: 180, Env: anthropicEnv},
		{ID: "breakdown-01", Name: "Breakdown", Role: "breakdown", Model: "sonnet", MaxRetries: maxTaskRetries, TimeoutSecs: 120, Env: breakdownEnv},
		{ID: "coding-agent-01", Name: "Coder A", Role: "coding", Model: "minimax", MaxRetries: maxTaskRetries, TimeoutSecs: 600, Env: minimaxEnv},
		{ID: "coding-agent-02", Name: "Coder B", Role: "coding", Model: "minimax", MaxRetries: maxTaskRetries, TimeoutSecs: 600, Env: minimaxEnv},
		{ID: "test-agent-01", Name: "Tester", Role: "test", Model: "glm5", MaxRetries: maxTaskRetries, TimeoutSecs: 300, Env: glmEnv},
		{ID: "review-agent-01", Name: "Reviewer", Role: "review", Model: "opus", MaxRetries: maxTaskRetries, TimeoutSecs: 300, Env: anthropicEnv},
		{ID: "docs-agent-01", Name: "Docs Writer", Role: "docs", Model: "glm-flash", MaxRetries: maxTaskRetries, TimeoutSecs: 120, Env: glmEnv},
		{ID: "deploy-agent-01", Name: "Deployer", Role: "deploy", Model: "glm-flash", MaxRetries: maxTaskRetries, TimeoutSecs: 180, Env: glmEnv},
		{ID: "notify-agent-01", Name: "Notifier", Role: "notify", Model: "glm-flash", MaxRetries: maxTaskRetries, TimeoutSecs: 30, Env: glmEnv},
	}

	for _, cfg := range defaults {
		a := agent.NewBaseAgent(cfg)
		if err := pool.Spawn(a); err != nil {
			log.Error().Err(err).Str("agent", cfg.ID).Msg("spawn failed")
		}
	}
	log.Info().Int("count", len(defaults)).Msg("default agents spawned")
}

// pollTrelloIdeas polls an idea Trello board on interval, picks up every open
// card that has NOT yet been moved to doneListID, submits an "idea" task and a
// dependent "breakdown" task into the queue, then moves the card to doneListID
// so it is not re-processed on the next poll.
//
// Required env vars (passed as parameters):
//
//	TRELLO_IDEA_BOARD_ID  — board to read ideas from
//	TRELLO_DONE_LIST_ID   — list to move processed cards to (optional but recommended)
//	TRELLO_API_KEY / TRELLO_TOKEN — Trello credentials
func pollTrelloIdeas(
	ctx context.Context,
	boardID, doneListID, apiKey, token string,
	q *queue.Queue,
	interval time.Duration,
) {
	client, err := trello.New(apiKey, token)
	if err != nil {
		log.Error().Err(err).Msg("trello poller: client init failed — poller exiting")
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cards, err := client.GetBoardCards(ctx, boardID)
			if err != nil {
				log.Error().Err(err).Str("board", boardID).Msg("trello poller: failed to fetch cards")
				continue
			}

			for _, card := range cards {
				// Skip cards already in the done/processing list.
				if doneListID != "" && card.IDList == doneListID {
					continue
				}

				log.Info().Str("card", card.ID).Str("title", card.Name).Msg("trello poller: new idea card found")

				// Build idea task — executed by idea-agent-01 (opus).
				ideaID := "T-trello-idea-" + safeTruncate(card.ID, 8)
				ideaTask := &agent.Task{
					ID:          ideaID,
					Title:       card.Name,
					Description: card.Desc,
					AgentRole:   "idea",
					Priority:    agent.PriorityNormal,
					Status:      agent.TaskPending,
					CreatedAt:   time.Now(),
					Meta: map[string]string{
						"trello_card_id":  card.ID,
						"trello_card_url": card.ShortURL,
						"source":          "trello_poller",
					},
				}

				// Build breakdown task — depends on idea task completing first.
				breakdownID := "T-trello-breakdown-" + safeTruncate(card.ID, 8)
				breakdownTask := &agent.Task{
					ID:          breakdownID,
					Title:       "Breakdown: " + card.Name,
					Description: card.Desc,
					AgentRole:   "breakdown",
					Priority:    agent.PriorityNormal,
					Status:      agent.TaskPending,
					DependsOn:   []string{ideaID},
					CreatedAt:   time.Now(),
					Meta: map[string]string{
						"trello_card_id":  card.ID,
						"trello_card_url": card.ShortURL,
						"source":          "trello_poller",
					},
				}

				q.Push(ideaTask)
				q.Push(breakdownTask)

				log.Info().
					Str("idea_task", ideaID).
					Str("breakdown_task", breakdownID).
					Str("card", card.Name).
					Msg("trello poller: submitted idea + breakdown tasks")

				// Move card to done/processing list so it is not re-processed.
				if doneListID != "" {
					if err := client.MoveCard(ctx, card.ID, doneListID); err != nil {
						log.Warn().Err(err).Str("card", card.ID).Msg("trello poller: failed to move card to done list")
					}
				}
			}
		}
	}
}

// safeTruncate returns s[:maxLen] if len(s) >= maxLen, otherwise returns s as-is.
func safeTruncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
