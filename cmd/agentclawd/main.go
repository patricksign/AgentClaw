package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"log/slog"

	"github.com/robfig/cron/v3"

	"github.com/patricksign/AgentClaw/config"
	"github.com/patricksign/AgentClaw/internal/api"
	"github.com/patricksign/AgentClaw/internal/domain"
	infratask "github.com/patricksign/AgentClaw/internal/infra/task"
	"github.com/patricksign/AgentClaw/internal/integrations/github"
	"github.com/patricksign/AgentClaw/internal/integrations/pipeline"
	"github.com/patricksign/AgentClaw/internal/integrations/trello"
	"github.com/patricksign/AgentClaw/internal/llm"
	"github.com/patricksign/AgentClaw/internal/summarizer"
)

const maxTaskRetries = 3

var (
	configPath string
	env        string
)

func loadConfig() *config.Config {
	conf := &config.Config{}
	if err := config.LoadConfig(configPath, env, conf); err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}
	return conf
}

func main() {
	// Parse flags in main() — not init() — for testability (#49)
	flag.StringVar(&configPath, "config", "config", "path to config file")
	flag.StringVar(&env, "env", "", "env")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.Info("AgentClaw starting...")

	// ── Config ─────────────────────────────────────────────────────────────
	cfg := loadConfig()
	statePath := cfg.Paths.StatePath

	// ── Layer 4: Infrastructure ───────────────────────────────────────────
	infra, cleanupInfra := wireInfra(cfg)
	defer cleanupInfra()

	// ── Layer 2: Use Cases ────────────────────────────────────────────────
	_ = wireUseCase(infra)

	// ── Legacy Agent Layer ────────────────────────────────────────────────
	legacy := wireLegacy(infra, cfg)

	// ── Event-Driven: Domain Event Bus + Subscribers ──────────────────────
	subs := wireSubscribers(infra, legacy, legacy.q)

	// Dispatcher publishes events on the domain bus.
	dispatcher := infratask.NewQueueDispatcher(subs.domainBus)

	// Task result waiter (listens for task.done / task.failed on domain bus).
	waiter := infratask.NewEventWaiter(subs.domainBus)

	// ── Background Context ────────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── Queue Workers ─────────────────────────────────────────────────────
	var workerWg sync.WaitGroup
	roles := []string{
		"idea", "architect", "breakdown",
		"coding", "test", "review",
		"docs", "deploy", "notify",
	}
	for _, role := range roles {
		workerWg.Add(1)
		go func(r string) {
			defer workerWg.Done()
			runWorker(ctx, r, legacy.q, legacy.exec)
		}(role)
	}
	slog.Info("queue workers started", "roles", roles)

	// ── Trello Idea Board Poller ──────────────────────────────────────────
	if cfg.Trello.IdeaBoardID != "" {
		workerWg.Add(1) // track poller goroutine (#62)
		go func() {
			defer workerWg.Done()
			pollTrelloIdeas(ctx, cfg.Trello.IdeaBoardID, cfg.Trello.DoneListID, cfg.Trello.APIKey, cfg.Trello.Token, legacy.q, 30*time.Second)
		}()
		slog.Info("Trello idea board poller started", "board", cfg.Trello.IdeaBoardID)
	}

	// ── Trello Trigger Client ─────────────────────────────────────────────
	var triggerTrelloClient *trello.Client
	if cfg.Trello.APIKey != "" && cfg.Trello.Token != "" {
		var err error
		triggerTrelloClient, err = trello.New(cfg.Trello.APIKey, cfg.Trello.Token)
		if err != nil {
			slog.Warn("trigger Trello client init failed — /api/trigger will be unavailable", "err", err)
		}
	}

	// GitHub client for pipeline PR creation.
	var ghClient *github.Client
	if cfg.GitHub.Token != "" && cfg.GitHub.Owner != "" && cfg.GitHub.Repo != "" {
		if gh, err := github.New(); err == nil {
			ghClient = gh
		} else {
			slog.Warn("GitHub client init failed — pipeline PRs disabled", "err", err)
		}
	}

	// ── HTTP + WebSocket API ──────────────────────────────────────────────
	srv := api.NewServer(cfg, infra.redisClient,
		legacy.pool,
		legacy.q,
		legacy.exec,
		infra.coreMem,
		legacy.bus,
		triggerTrelloClient,
		cfg.Telegram.BotToken,
		cfg.Telegram.StatusChatID)

	// Pipeline service now uses port interfaces.
	pipelineSvc := pipeline.NewService(triggerTrelloClient, ghClient, dispatcher, waiter, subs.domainBus)
	srv.SetTriggerService(pipelineSvc)

	// ── Summarizer + Weekly Cron ──────────────────────────────────────────
	summarizerRouter := llm.NewRouterWithEnv(map[string]string{"ANTHROPIC_API_KEY": cfg.LLM.AnthropicAPIKey})
	sum := summarizer.New(infra.coreMem, infra.coreMem.AgentDoc(), summarizerRouter, statePath)
	srv.SetSummarizer(sum)

	// Start background goroutines AFTER all setters — avoids data race (#69)
	srv.StartBackground()

	cronScheduler := cron.New()
	summarizerConfigs := []domain.AgentConfig{
		{ID: "idea", Role: "idea"},
		{ID: "architect", Role: "architect"},
		{ID: "breakdown", Role: "breakdown"},
		{ID: "coding", Role: "coding"},
		{ID: "test", Role: "test"},
		{ID: "review", Role: "review"},
		{ID: "docs", Role: "docs"},
		{ID: "deploy", Role: "deploy"},
		{ID: "notify", Role: "notify"},
	}
	if _, err := cronScheduler.AddFunc("0 2 * * 0", func() {
		cronCtx, cronCancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cronCancel()
		if cost, cerr := sum.CompressAll(cronCtx, summarizerConfigs); cerr != nil {
			slog.Error("cron: CompressAll failed", "err", cerr)
		} else {
			slog.Info("cron: CompressAll completed", "cost_usd", cost)
		}
	}); err != nil {
		slog.Error("failed to schedule summarizer cron", "err", err)
	}
	cronScheduler.Start()
	defer cronScheduler.Stop()

	// Register all API routes on the Fiber app (with auth middleware)
	srv.RegisterRoutes(cfg)

	// Start HTTP listener — monitor for bind failure alongside shutdown signals (#52)
	listenErr := srv.Start()

	// ── Graceful Shutdown ─────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Wait for either a signal or a listen error (e.g., port already in use).
	select {
	case <-quit:
		// Normal shutdown requested
	case err := <-listenErr:
		if err != nil {
			slog.Error("HTTP server failed", "err", err)
			os.Exit(1)
		}
	}

	slog.Info("shutting down AgentClaw...")

	// 1. Cancel context — stops workers, pollers, and server goroutines.
	cancel()

	// 2. Stop WebSocket hub + wait for forwardEvents to exit (before EventBus stops).
	srv.Shutdown()
	slog.Info("WebSocket hub stopped")

	// 3. Stop HTTP server — no new requests.
	if err := srv.App().ShutdownWithTimeout(15 * time.Second); err != nil {
		slog.Error("HTTP shutdown error", "err", err)
	}
	slog.Info("HTTP server stopped")

	// 4. Wait for all queue workers and pollers to exit.
	workerWg.Wait()
	slog.Info("all queue workers stopped")

	// 5. Stop agents — no new events will be published.
	agentCtx, agentCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer agentCancel()
	legacy.pool.ShutdownAll(agentCtx)
	slog.Info("all agents stopped")

	// 6. Stop event-driven layer (proper ordering):
	//    a) Waiter first — signals pending waiters to unblock.
	//    b) Subscribers — unsubscribe handlers from bus.
	//    c) Domain bus — drain in-flight handler goroutines.
	waiter.Stop()
	slog.Info("event waiter stopped")

	stopSubscribers(subs)

	subs.domainBus.Stop()
	slog.Info("domain event bus drained")

	// 7. Infra cleanup (deferred cleanupInfra closes DB + Redis).
	slog.Info("AgentClaw stopped")
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// safeTruncate truncates s to maxLen runes (UTF-8 safe).
func safeTruncate(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxLen])
}

func newTrelloClient(apiKey, token string) (*trello.Client, error) {
	return trello.New(apiKey, token)
}
