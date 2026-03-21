package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/patricksign/AgentClaw/internal/agent"
	"github.com/patricksign/AgentClaw/internal/api"
	"github.com/patricksign/AgentClaw/internal/domain"
	infraintegrations "github.com/patricksign/AgentClaw/internal/infra/integrations"
	infrallm "github.com/patricksign/AgentClaw/internal/infra/llm"
	inframemory "github.com/patricksign/AgentClaw/internal/infra/memory"
	infranotification "github.com/patricksign/AgentClaw/internal/infra/notification"
	infrastate "github.com/patricksign/AgentClaw/internal/infra/state"
	"github.com/patricksign/AgentClaw/internal/integrations/telegram"
	"github.com/patricksign/AgentClaw/internal/integrations/trello"
	"github.com/patricksign/AgentClaw/internal/llm"
	"github.com/patricksign/AgentClaw/internal/memory"
	"github.com/patricksign/AgentClaw/internal/queue"
	"github.com/patricksign/AgentClaw/internal/state"
	"github.com/patricksign/AgentClaw/internal/summarizer"
	"github.com/patricksign/AgentClaw/internal/usecase/escalation"
	"github.com/patricksign/AgentClaw/internal/usecase/orchestrator"
	"github.com/patricksign/AgentClaw/internal/usecase/phase"
)

const maxTaskRetries = 3

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
	log.Info().Msg("AgentClaw starting...")

	// ── Config ─────────────────────────────────────────────────────────────
	addr := getenv("ADDR", ":8080")
	dbPath := getenv("DB_PATH", "./AgentClaw.db")
	projectPath := getenv("PROJECT_PATH", "./memory/project.md")
	statePath := getenv("STATE_PATH", "./state")
	pricingPath := getenv("PRICING_PATH", "./pricing/agent-pricing.json")
	agentsConfigPath := getenv("AGENTS_CONFIG", "./config/agents.json")

	// ── Layer 4: Infrastructure — LLM ──────────────────────────────────────
	if err := llm.LoadPricing(pricingPath); err != nil {
		log.Warn().Err(err).Str("path", pricingPath).Msg("failed to load pricing — cost tracking will report $0")
	} else {
		log.Info().Str("path", pricingPath).Msg("model pricing loaded")
	}

	// Core LLM router (used by existing agent layer)
	coreLLMRouter := llm.NewRouter()

	// Infra LLM router adapter (satisfies port.LLMRouter)
	llmRouter := infrallm.NewRouter(coreLLMRouter)

	// ── Layer 4: Infrastructure — State ────────────────────────────────────
	stateStore, err := infrastate.NewStateStore(statePath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to init state store")
	}

	// ── Layer 4: Infrastructure — Memory ───────────────────────────────────
	coreMem, err := memory.NewWithState(dbPath, projectPath, statePath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to init memory store")
	}
	defer func() {
		if err := coreMem.Close(); err != nil {
			log.Error().Err(err).Msg("failed to close memory store")
		}
	}()
	log.Info().Str("db", dbPath).Msg("memory store ready")

	// SQLite-backed checkpoint store (atomic, queryable, co-located with tasks).
	checkpointStore := inframemory.NewSQLiteCheckpointStore(coreMem)
	log.Info().Msg("checkpoint store ready (SQLite)")

	// Infra memory adapter (satisfies port.MemoryStore).
	// TODO: wire into use case layer once agent migration is complete.
	_ = inframemory.NewStore(coreMem)

	// Seed default scope manifests.
	if ss := coreMem.Scope(); ss != nil {
		if err := initDefaultScopes(ss); err != nil {
			log.Warn().Err(err).Msg("failed to seed default scope manifests")
		}
	}

	// ── Layer 4: Infrastructure — Telegram & Notifications ─────────────────
	dualTG := telegram.NewDualChannelClient()
	if dualTG != nil && dualTG.IsConfigured() {
		log.Info().Msg("dual-channel Telegram client ready")
	} else {
		log.Info().Msg("dual-channel Telegram not configured — pre-exec notifications disabled")
	}

	// Infra notifier (satisfies port.Notifier)
	notifier := infranotification.NewTelegramDispatcher(dualTG)

	// Wire fallback notifications: llm.Router → Telegram via goroutine.
	coreLLMRouter.SetFallbackNotifier(func(evt llm.FallbackEvent) {
		var evtType domain.EventType
		ch := domain.StatusChannel
		msg := fmt.Sprintf("%s → %s", evt.FromModel, evt.ToModel)
		if evt.Exhausted {
			evtType = domain.EventFallbackExhausted
			ch = domain.HumanChannel
			msg = fmt.Sprintf("All models failed. Last error: %s", evt.Err)
		} else {
			evtType = domain.EventFallbackTriggered
		}
		go func() {
			_ = notifier.Dispatch(context.Background(), domain.Event{
				Type:    evtType,
				Channel: ch,
				TaskID:  evt.TaskID,
				Payload: map[string]string{
					"message":    msg,
					"from_model": evt.FromModel,
					"to_model":   evt.ToModel,
				},
				OccurredAt: time.Now(),
			})
		}()
	})

	// Reply store + adapter (satisfies escalation.HumanAsker)
	replyStore := agent.NewReplyStore()
	replyAdapter := infraintegrations.NewReplyAdapter(replyStore, dualTG)

	// ── Layer 2: Use Cases — Escalation ────────────────────────────────────
	var escalCache *escalation.Cache
	if rs := coreMem.Resolved(); rs != nil {
		// Wrap the state.ResolvedStore to satisfy escalation.ResolvedQuerier.
		escalCache = escalation.NewCache(&resolvedQuerierAdapter{rs: rs})
	} else {
		escalCache = escalation.NewCache(nil)
	}

	escalChain := escalation.NewChain(llmRouter, notifier, escalCache, replyAdapter, checkpointStore)

	// ── Layer 2: Use Cases — Phase Runner ──────────────────────────────────
	phaseRunner := phase.NewRunner()

	// ── Layer 2: Use Cases — Orchestrators ─────────────────────────────────
	hierarchical := orchestrator.NewHierarchicalOrchestrator(llmRouter, notifier, nil)
	parallel := orchestrator.NewParallelOrchestrator(phaseRunner, llmRouter, notifier, escalChain, nil, stateStore)
	loopOrch := orchestrator.NewLoopOrchestrator(phaseRunner, llmRouter, notifier, 3)
	// TODO: wire orchRouter into the task dispatch path once agent migration is complete.
	_ = orchestrator.NewOrchestratorRouter(hierarchical, parallel, loopOrch, phaseRunner, llmRouter, notifier)

	// ── Agent Pool (existing layer — not yet migrated to ports) ────────────
	bus := agent.NewEventBus()
	factory := agent.AgentFactory(agent.NewBaseAgent)
	pool := agent.NewPool(bus, factory)
	q := queue.New()
	exec := agent.NewExecutor(pool, bus, coreMem)

	exec.SetReplyStore(replyStore)
	exec.SetQueue(q)

	// ── Spawn Agents ───────────────────────────────────────────────────────
	spawnAgentsFromConfig(pool, agentsConfigPath)

	preExecDeps := agent.PreExecutorDeps{
		Telegram:   dualTG,
		ReplyStore: replyStore,
		SaveTask:   coreMem.SaveTask,
	}
	injectPreExecDeps(pool, preExecDeps)

	// ── Post-run Hooks (Trello) ────────────────────────────────────────────
	trelloListID := getenv("TRELLO_LIST_ID", "")
	if trelloListID != "" {
		hookAPIKey := getenv("TRELLO_API_KEY", "")
		hookToken := getenv("TRELLO_TOKEN", "")
		if hookAPIKey != "" && hookToken != "" {
			hookClient, herr := trello.New(hookAPIKey, hookToken)
			if herr != nil {
				log.Warn().Err(herr).Msg("Trello hook client init failed — breakdown cards will not be created")
			} else {
				exec.AddPostRunHook(agent.TrelloBreakdownHook(hookClient, trelloListID))
				log.Info().Msg("Trello breakdown hook registered")
			}
		}
	}

	// ── Recover Suspended Tasks ────────────────────────────────────────────
	recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := exec.RecoverSuspendedTasks(recoverCtx, dualTG); err != nil {
		log.Warn().Err(err).Msg("RecoverSuspendedTasks failed — some clarify-phase tasks may need manual resume")
	}
	recoverCancel()

	// ── Background Context ─────────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── Queue Workers ──────────────────────────────────────────────────────
	roles := []string{
		"idea", "architect", "breakdown",
		"coding", "test", "review",
		"docs", "deploy", "notify",
	}
	for _, role := range roles {
		go runWorker(ctx, role, q, exec)
	}
	log.Info().Strs("roles", roles).Msg("queue workers started")

	// ── Trello Idea Board Poller ───────────────────────────────────────────
	ideaBoardID := getenv("TRELLO_IDEA_BOARD_ID", "")
	doneListID := getenv("TRELLO_DONE_LIST_ID", "")
	if ideaBoardID != "" {
		trelloAPIKey := getenv("TRELLO_API_KEY", "")
		trelloToken := getenv("TRELLO_TOKEN", "")
		go pollTrelloIdeas(ctx, ideaBoardID, doneListID, trelloAPIKey, trelloToken, q, 30*time.Second)
		log.Info().Str("board", ideaBoardID).Msg("Trello idea board poller started")
	}

	// ── Trello Trigger Client ──────────────────────────────────────────────
	trelloKey := getenv("TRELLO_KEY", getenv("TRELLO_API_KEY", ""))
	trelloToken := getenv("TRELLO_TOKEN", "")
	var triggerTrelloClient *trello.Client
	if trelloKey != "" && trelloToken != "" {
		triggerTrelloClient, err = trello.New(trelloKey, trelloToken)
		if err != nil {
			log.Warn().Err(err).Msg("trigger Trello client init failed — /api/trigger will be unavailable")
		}
	}

	// ── HTTP + WebSocket API ───────────────────────────────────────────────
	telegramToken := getenv("TELEGRAM_BOT_TOKEN", "")
	telegramChatID := getenv("TELEGRAM_CHAT_ID", "")
	srv := api.NewServer(pool, q, exec, coreMem, bus, triggerTrelloClient, telegramToken, telegramChatID)

	// ── Summarizer + Weekly Cron ───────────────────────────────────────────
	anthropicKey := getenv("ANTHROPIC_API_KEY", "")
	summarizerRouter := llm.NewRouterWithEnv(map[string]string{"ANTHROPIC_API_KEY": anthropicKey})
	sum := summarizer.New(coreMem, coreMem.AgentDoc(), summarizerRouter, statePath)
	srv.SetSummarizer(sum)

	cronScheduler := cron.New()
	summarizerConfigs := []agent.Config{
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
			log.Error().Err(cerr).Msg("cron: CompressAll failed")
		} else {
			log.Info().Float64("cost_usd", cost).Msg("cron: CompressAll completed")
		}
	}); err != nil {
		log.Error().Err(err).Msg("failed to schedule summarizer cron")
	}
	cronScheduler.Start()
	defer cronScheduler.Stop()

	// ── HTTP Server ────────────────────────────────────────────────────────
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second, // prevent Slowloris
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB
	}
	go func() {
		log.Info().Str("addr", addr).Msg("API server listening")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("server error")
		}
	}()

	// ── Graceful Shutdown ──────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("shutting down AgentClaw...")

	cancel()

	httpCtx, httpCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer httpCancel()
	if err := httpServer.Shutdown(httpCtx); err != nil {
		log.Error().Err(err).Msg("HTTP shutdown error")
	}
	log.Info().Msg("HTTP server stopped")

	srv.Shutdown()
	log.Info().Msg("WebSocket hub stopped")

	agentCtx, agentCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer agentCancel()
	pool.ShutdownAll(agentCtx)
	log.Info().Msg("all agents stopped")

	log.Info().Msg("AgentClaw stopped")
}

// ── resolvedQuerierAdapter bridges state.ResolvedStore → escalation.ResolvedQuerier ──

type resolvedQuerierAdapter struct {
	rs *state.ResolvedStore
}

func (a *resolvedQuerierAdapter) Search(question, role string) ([]escalation.CachedAnswer, error) {
	matches, err := a.rs.Search(question, role)
	if err != nil {
		return nil, err
	}
	results := make([]escalation.CachedAnswer, len(matches))
	for i, m := range matches {
		results[i] = escalation.CachedAnswer{
			Summary:         m.ResolutionSummary,
			OccurrenceCount: m.OccurrenceCount,
		}
	}
	return results, nil
}

func (a *resolvedQuerierAdapter) Save(question, answer, role string) error {
	return a.rs.Save(state.ErrorPattern{
		ErrorPattern:      question,
		ResolutionSummary: answer,
		AgentRoles:        []string{role},
	}, answer)
}

// ── Worker ──────────────────────────────────────────────────────────────────

func runWorker(ctx context.Context, role string, q *queue.Queue, exec *agent.Executor) {
	log.Info().Str("role", role).Msg("worker started")
	for {
		task, err := q.Pop(ctx, role)
		if err != nil {
			return
		}
		if err := exec.Execute(ctx, task); err != nil {
			// Do not retry if the parent context was cancelled (graceful shutdown).
			// Retrying would re-queue a task that will immediately fail again.
			if ctx.Err() != nil {
				log.Info().Str("task", task.ID).Str("role", role).Msg("task interrupted by shutdown — not retrying")
				return
			}
			log.Error().Err(err).Str("task", task.ID).Str("role", role).Msg("execute error")
			q.MarkFailed(task, maxTaskRetries)
		} else {
			q.MarkDone(task.ID)
		}
	}
}

// ── Agent Spawning ──────────────────────────────────────────────────────────

func spawnAgentsFromConfig(pool *agent.Pool, configPath string) {
	configs, err := agent.LoadConfigs(configPath)
	if err != nil {
		log.Warn().Err(err).Str("path", configPath).Msg("agent config file not found — using hardcoded defaults")
		configs = defaultAgentConfigs()
	}

	for i := range configs {
		if configs[i].Env == nil {
			configs[i].Env = make(map[string]string)
		}
		for _, key := range configs[i].EnvKeys {
			if v := os.Getenv(key); v != "" {
				configs[i].Env[key] = v
			}
		}
	}

	for _, cfg := range configs {
		a := agent.NewBaseAgent(cfg)
		if err := pool.Spawn(a); err != nil {
			log.Error().Err(err).Str("agent", cfg.ID).Msg("spawn failed")
		}
	}
	log.Info().Int("count", len(configs)).Str("source", configPath).Msg("agents spawned")
}

func defaultAgentConfigs() []agent.Config {
	anthropicEnv := map[string]string{"ANTHROPIC_API_KEY": os.Getenv("ANTHROPIC_API_KEY")}
	workerEnv := map[string]string{
		"MINIMAX_API_KEY": os.Getenv("MINIMAX_API_KEY"),
		"KIMI_API_KEY":    os.Getenv("KIMI_API_KEY"),
		"GLM_API_KEY":     os.Getenv("GLM_API_KEY"),
	}
	glmEnv := map[string]string{"GLM_API_KEY": os.Getenv("GLM_API_KEY")}

	return []agent.Config{
		// Orchestration — Opus
		{ID: "idea-agent-01", Name: "Idea Agent", Role: "idea", Model: "opus", MaxRetries: maxTaskRetries, TimeoutSecs: 120, Env: anthropicEnv},
		{ID: "architect-01", Name: "Architect", Role: "architect", Model: "opus", MaxRetries: maxTaskRetries, TimeoutSecs: 180, Env: anthropicEnv},
		{ID: "breakdown-01", Name: "Breakdown", Role: "breakdown", Model: "opus", MaxRetries: maxTaskRetries, TimeoutSecs: 120, Env: anthropicEnv},
		// Implementation — MiniMax (fallback: Kimi → GLM-5)
		{ID: "coding-agent-01", Name: "Coder A", Role: "coding", Model: "minimax", MaxRetries: maxTaskRetries, TimeoutSecs: 600, Env: workerEnv},
		{ID: "coding-agent-02", Name: "Coder B", Role: "coding", Model: "minimax", MaxRetries: maxTaskRetries, TimeoutSecs: 600, Env: workerEnv},
		// Coordination — Sonnet (test, review)
		{ID: "test-agent-01", Name: "Tester", Role: "test", Model: "sonnet", MaxRetries: maxTaskRetries, TimeoutSecs: 300, Env: anthropicEnv},
		{ID: "review-agent-01", Name: "Reviewer", Role: "review", Model: "sonnet", MaxRetries: maxTaskRetries, TimeoutSecs: 300, Env: anthropicEnv},
		// Docs — MiniMax (fallback: Kimi → GLM-5)
		{ID: "docs-agent-01", Name: "Docs Writer", Role: "docs", Model: "minimax", MaxRetries: maxTaskRetries, TimeoutSecs: 120, Env: workerEnv},
		// Infrastructure — GLM-flash
		{ID: "deploy-agent-01", Name: "Deployer", Role: "deploy", Model: "glm-flash", MaxRetries: maxTaskRetries, TimeoutSecs: 180, Env: glmEnv},
		{ID: "notify-agent-01", Name: "Notifier", Role: "notify", Model: "glm-flash", MaxRetries: maxTaskRetries, TimeoutSecs: 30, Env: glmEnv},
	}
}

// ── Scope Defaults ──────────────────────────────────────────────────────────

func initDefaultScopes(ss *state.ScopeStore) error {
	manifests := []state.ScopeManifest{
		{AgentID: "idea", Owns: []string{"memory/project.md (app concept section)"}, MustNotTouch: []string{"internal/", "cmd/", "go.mod"}, InterfacesWith: map[string]string{"architect": "passes structured app concept for system design", "breakdown": "concept is used as breakdown input"}, CurrentFocus: "generate concrete, buildable app concepts from Trello briefs"},
		{AgentID: "architect", Owns: []string{"memory/project.md (ADR section)", "docs/architecture/"}, DependsOn: []string{"idea"}, MustNotTouch: []string{"internal/", "cmd/", "go.mod"}, InterfacesWith: map[string]string{"idea": "receives app concept", "breakdown": "passes Mermaid diagrams and ADRs for ticket decomposition"}, CurrentFocus: "produce Mermaid diagrams, ERDs, API contracts, and ADRs"},
		{AgentID: "breakdown", Owns: []string{"Trello checklists and ticket JSON"}, DependsOn: []string{"idea", "architect"}, MustNotTouch: []string{"internal/", "cmd/", "go.mod", "docs/architecture/"}, InterfacesWith: map[string]string{"architect": "receives system design docs", "coding": "tickets are consumed by coding agents", "test": "tickets describe acceptance criteria for test agents"}, CurrentFocus: "decompose app concept into actionable Trello tickets (max 10)"},
		{AgentID: "coding", Owns: []string{"internal/", "cmd/", "vendor/"}, DependsOn: []string{"breakdown"}, MustNotTouch: []string{"docs/", "memory/project.md", "static/"}, InterfacesWith: map[string]string{"breakdown": "receives implementation tickets", "test": "produces code that test agents verify", "review": "produces PRs that review agents inspect"}, CurrentFocus: "implement features from tickets in idiomatic Go"},
		{AgentID: "test", Owns: []string{"*_test.go files", "testdata/"}, DependsOn: []string{"coding"}, MustNotTouch: []string{"internal/ (non-test files)", "cmd/", "docs/", "memory/project.md"}, InterfacesWith: map[string]string{"coding": "receives implementation code to write tests for", "review": "test results inform the review decision"}, CurrentFocus: "write table-driven tests covering edge cases and error paths"},
		{AgentID: "review", Owns: []string{"GitHub PR review comments"}, DependsOn: []string{"coding", "test"}, MustNotTouch: []string{"internal/", "cmd/", "docs/", "memory/project.md"}, InterfacesWith: map[string]string{"coding": "reviews PRs opened by coding agents", "test": "incorporates test results into review decision", "deploy": "approved PRs are handed to deploy agent"}, CurrentFocus: "review PRs for correctness, security, performance, and idiomatic Go"},
		{AgentID: "docs", Owns: []string{"docs/", "*.md files (except memory/project.md)"}, DependsOn: []string{"coding"}, MustNotTouch: []string{"internal/", "cmd/", "go.mod", "memory/project.md"}, InterfacesWith: map[string]string{"coding": "documents the code produced by coding agents", "review": "documentation may be reviewed alongside code"}, CurrentFocus: "generate README sections, godoc comments, API docs, and usage examples"},
		{AgentID: "deploy", Owns: []string{"Dockerfile", "docker-compose.yml", "Makefile (deploy targets)"}, DependsOn: []string{"review"}, MustNotTouch: []string{"internal/", "cmd/", "docs/", "memory/project.md"}, InterfacesWith: map[string]string{"review": "deploys after approved PR merge", "notify": "signals deploy result to notify agent"}, CurrentFocus: "execute deployment steps and verify health check"},
		{AgentID: "notify", Owns: []string{"Telegram/Slack notification messages"}, DependsOn: []string{"deploy"}, MustNotTouch: []string{"internal/", "cmd/", "docs/", "memory/project.md"}, InterfacesWith: map[string]string{"deploy": "receives deploy result to notify about", "review": "notifies on PR review outcomes"}, CurrentFocus: "send concise pipeline completion notifications to Telegram and Slack"},
	}

	for _, m := range manifests {
		if err := ss.Write(m); err != nil {
			return fmt.Errorf("initDefaultScopes: %w", err)
		}
	}
	return nil
}

// ── Trello Poller ───────────────────────────────────────────────────────────

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
				if doneListID != "" && card.IDList == doneListID {
					continue
				}

				log.Info().Str("card", card.ID).Str("title", card.Name).Msg("trello poller: new idea card found")

				ideaID := "T-trello-idea-" + safeTruncate(card.ID, 8)
				ideaTask := &agent.Task{
					ID:          ideaID,
					Title:       card.Name,
					Description: card.Desc,
					AgentRole:   "idea",
					Priority:    agent.PriorityNormal,
					Status:      agent.TaskPending,
					CreatedAt:   time.Now(),
					Meta:        map[string]string{"trello_card_id": card.ID, "trello_card_url": card.ShortURL, "source": "trello_poller"},
				}

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
					Meta:        map[string]string{"trello_card_id": card.ID, "trello_card_url": card.ShortURL, "source": "trello_poller"},
				}

				q.Push(ideaTask)
				q.Push(breakdownTask)

				if doneListID != "" {
					if err := client.MoveCard(ctx, card.ID, doneListID); err != nil {
						log.Warn().Err(err).Str("card", card.ID).Msg("trello poller: failed to move card to done list")
					}
				}
			}
		}
	}
}

// ── Helpers ─────────────────────────────────────────────────────────────────

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

func injectPreExecDeps(pool *agent.Pool, deps agent.PreExecutorDeps) {
	for _, a := range pool.All() {
		if ba, ok := a.(*agent.BaseAgent); ok {
			ba.SetPreExecutorDeps(deps)
		}
	}
	log.Info().Msg("pre-execution deps injected into all agents")
}
