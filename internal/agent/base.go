package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/patricksign/agentclaw/internal/llm"
	"github.com/patricksign/agentclaw/internal/integrations/trello"
	"github.com/rs/zerolog/log"
)

// ─── BaseAgent ────────────────────────────────────────────────────────────────
// BaseAgent là implementation mặc định của Agent interface.
// Tất cả 10 agents trong spawnDefaultAgents đều dùng BaseAgent.
// Mỗi agent chỉ khác nhau ở Config (role, model, timeout) —
// logic Run() được điều chỉnh tự động theo role.

type BaseAgent struct {
	cfg          Config
	status       Status
	router       *llm.Router
	mu           sync.RWMutex
	trelloOnce   sync.Once
	trelloClient *trello.Client
}

// NewBaseAgent is the factory function called from main.go and Pool.Restart.
// If cfg.Env contains API key overrides they take precedence over OS env vars,
// allowing per-agent key configuration.
func NewBaseAgent(cfg Config) Agent {
	var router *llm.Router
	if len(cfg.Env) > 0 {
		router = llm.NewRouterWithEnv(cfg.Env)
	} else {
		router = llm.NewRouter()
	}
	return &BaseAgent{
		cfg:    cfg,
		status: StatusIdle,
		router: router,
	}
}

// ─── Agent interface implementation ─────────────────────────────────────────

func (a *BaseAgent) Config() *Config {
	return &a.cfg
}

func (a *BaseAgent) Status() Status {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.status
}

func (a *BaseAgent) setStatus(s Status) {
	a.mu.Lock()
	a.status = s
	a.mu.Unlock()
}

// Run thực thi task — đây là core logic của mọi agent
func (a *BaseAgent) Run(ctx context.Context, task *Task, mem MemoryContext) (*TaskResult, error) {
	a.setStatus(StatusRunning)
	defer a.setStatus(StatusIdle)

	start := time.Now()

	// 1. Build system prompt từ memory context
	system := a.buildSystemPrompt(mem)

	// 2. Build user message theo role
	userMsg := a.buildUserMessage(task)

	// 3. Gọi LLM
	req := llm.Request{
		Model:     a.cfg.Model,
		System:    system,
		Messages:  []llm.Message{{Role: "user", Content: userMsg}},
		MaxTokens: a.maxTokensForRole(),
		TaskID:    task.ID,
	}

	resp, err := a.router.Call(ctx, req)
	if err != nil {
		a.setStatus(StatusFailed)
		return nil, fmt.Errorf("agent %s llm call failed: %w", a.cfg.ID, err)
	}

	// 4. Parse artifacts from response.
	// For the breakdown role: parse the JSON ticket list and create Trello cards.
	artifacts := a.parseArtifacts(resp.Content, task)
	if a.cfg.Role == "breakdown" {
		trelloArtifacts := a.pushToTrello(ctx, resp.Content, task)
		artifacts = append(artifacts, trelloArtifacts...)
	}

	result := &TaskResult{
		TaskID:       task.ID,
		Output:       resp.Content,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		CostUSD:      resp.CostUSD,
		Artifacts:    artifacts,
		Meta: map[string]string{
			"model":       resp.ModelUsed,
			"duration_ms": fmt.Sprintf("%d", time.Since(start).Milliseconds()),
		},
	}

	log.Info().
		Str("agent", a.cfg.ID).
		Str("role", a.cfg.Role).
		Str("task", task.ID).
		Int64("input_tokens", resp.InputTokens).
		Int64("output_tokens", resp.OutputTokens).
		Float64("cost_usd", resp.CostUSD).
		Msg("task completed")

	return result, nil
}

func (a *BaseAgent) HealthCheck(_ context.Context) bool {
	return a.Status() != StatusFailed
}

func (a *BaseAgent) OnShutdown(_ context.Context) {
	a.setStatus(StatusTerminated)
	log.Info().Str("agent", a.cfg.ID).Msg("agent shutdown")
}

// ─── System prompt builder ────────────────────────────────────────────────────

// buildSystemPrompt inject toàn bộ memory context vào system prompt
// Đây là cơ chế "không bao giờ quên" của agents
func (a *BaseAgent) buildSystemPrompt(mem MemoryContext) string {
	var sb strings.Builder

	// Role identity
	sb.WriteString(a.roleIdentity())
	sb.WriteString("\n\n")

	// Tầng 1: Project memory
	if mem.ProjectDoc != "" {
		sb.WriteString("---\n## PROJECT CONTEXT\n\n")
		sb.WriteString(mem.ProjectDoc)
		sb.WriteString("\n\n")
	}

	// Tầng 2: Recent task history
	if len(mem.RecentTasks) > 0 {
		sb.WriteString("---\n## RECENT WORK (same role)\n\n")
		for _, t := range mem.RecentTasks {
			sb.WriteString(fmt.Sprintf("- [%s] %s: %s\n", t.Status, t.ID, t.Title))
		}
		sb.WriteString("\n")
	}

	// Tầng 3: Relevant code context (RAG)
	if len(mem.RelevantCode) > 0 {
		sb.WriteString("---\n## RELEVANT CODE CONTEXT\n\n")
		for _, c := range mem.RelevantCode {
			sb.WriteString(c)
			sb.WriteString("\n\n")
		}
	}

	// ADRs
	if len(mem.ADRs) > 0 {
		sb.WriteString("---\n## ARCHITECTURE DECISIONS\n\n")
		for _, adr := range mem.ADRs {
			sb.WriteString("- " + adr + "\n")
		}
		sb.WriteString("\n")
	}

	// Tầng 4: Known error patterns (ResolvedStore RAG)
	if mem.Resolved != nil {
		// Build query from the most recent task in context, falling back to role name.
		query := a.cfg.Role
		if len(mem.RecentTasks) > 0 {
			last := mem.RecentTasks[len(mem.RecentTasks)-1]
			query = last.Title + " " + last.Description
		}
		if matches, _ := mem.Resolved.Search(query, a.cfg.Role); len(matches) > 0 {
			sb.WriteString("---\n## KNOWN ERROR PATTERNS — avoid these\n\n")
			for _, m := range matches {
				sb.WriteString(fmt.Sprintf("- **%s** (seen %d times)\n  Fix: %s\n\n",
					m.ErrorPattern, m.OccurrenceCount, m.ResolutionSummary))
			}
		}
	}

	sb.WriteString("---\n## OUTPUT\n\nBe concise and actionable. ")
	sb.WriteString(a.roleOutputInstruction())

	return sb.String()
}

// roleIdentity trả về identity prompt theo role
func (a *BaseAgent) roleIdentity() string {
	identities := map[string]string{
		"idea": `You are an expert product strategist and app ideation agent.
Your job: analyze briefs and generate concrete, buildable app concepts with clear value propositions.
Focus on: user problems, core features, technical feasibility, and competitive differentiation.`,

		"architect": `You are a senior software architect specializing in Go and mobile (Flutter/React Native).
Your job: translate app concepts into system designs with clear component boundaries.
Output: Mermaid diagrams, ERDs, API contracts, and architecture decision records (ADRs).`,

		"breakdown": `You are a technical project manager and sprint planner.
Your job: decompose app concepts and architecture docs into actionable Trello tickets and GitHub issues.
Each ticket must have: clear title, description, acceptance criteria, story points, and dependencies.`,

		"coding": `You are an expert Go and Flutter/React Native engineer.
Your job: implement features from ticket descriptions, following project conventions strictly.
Always: write idiomatic code, handle errors properly, add inline comments for non-obvious logic.`,

		"test": `You are a Go testing expert specializing in table-driven tests and integration tests.
Your job: write comprehensive tests for the code produced by coding agents.
Focus: edge cases, error paths, concurrency safety, and meaningful test names.`,

		"review": `You are a senior code reviewer with expertise in Go, security, and system design.
Your job: review pull requests for correctness, security, performance, and idiomatic Go.
Be specific: cite line numbers, explain why an issue matters, suggest concrete fixes.`,

		"docs": `You are a technical writer specializing in Go project documentation.
Your job: generate clear, accurate documentation from code and ticket descriptions.
Output: README sections, godoc comments, API docs, and usage examples.`,

		"deploy": `You are a DevOps engineer specializing in Go service deployment.
Your job: execute deployment steps to dev/staging environments after PR merge.
Always verify: build passes, health check responds, rollback plan exists.`,

		"notify": `You are a notification agent.
Your job: send concise, informative updates to Telegram or Slack about task completions, failures, and deployments.
Keep messages short, include relevant links, use emoji sparingly for clarity.`,
	}

	if identity, ok := identities[a.cfg.Role]; ok {
		return identity
	}
	return fmt.Sprintf("You are a %s agent. Complete the assigned task accurately and concisely.", a.cfg.Role)
}

// roleOutputInstruction trả về output format instruction theo role
func (a *BaseAgent) roleOutputInstruction() string {
	instructions := map[string]string{
		"idea":      "Return a structured app concept with: overview, target users, core features (max 5), tech stack recommendation, and risks.",
		"architect": "Return Mermaid diagrams and a bullet-point architecture summary. Mark each ADR clearly.",
		"breakdown": "Return a JSON array of tickets: [{title, description, acceptance_criteria, story_points, depends_on}]",
		"coding":    "Return only the implementation code with file paths as comments. No explanation outside code blocks.",
		"test":      "Return only test code. Use table-driven tests. Include at least one test for the error path.",
		"review":    "Return a JSON review: {approved: bool, comments: [{file, line, severity, message, suggestion}]}",
		"docs":      "Return markdown documentation ready to be committed to the repo.",
		"deploy":    "Return deployment status: {success: bool, url: string, logs: string}",
		"notify":    "Return the notification message text only. Max 3 lines.",
	}

	if inst, ok := instructions[a.cfg.Role]; ok {
		return inst
	}
	return "Return your output in a clear, structured format."
}

// ─── User message builder ─────────────────────────────────────────────────────

func (a *BaseAgent) buildUserMessage(task *Task) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Task ID:** %s\n", task.ID))
	sb.WriteString(fmt.Sprintf("**Title:** %s\n", task.Title))

	if task.Description != "" {
		sb.WriteString(fmt.Sprintf("\n**Description:**\n%s\n", task.Description))
	}

	if len(task.Tags) > 0 {
		sb.WriteString(fmt.Sprintf("\n**Tags:** %s\n", strings.Join(task.Tags, ", ")))
	}

	if len(task.Meta) > 0 {
		sb.WriteString("\n**Additional context:**\n")
		for k, v := range task.Meta {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", k, v))
		}
	}

	return sb.String()
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// maxTokensForRole — agents khác nhau cần output size khác nhau
func (a *BaseAgent) maxTokensForRole() int {
	limits := map[string]int{
		"idea":      2048,
		"architect": 4096,
		"breakdown": 4096,
		"coding":    8192,
		"test":      4096,
		"review":    2048,
		"docs":      4096,
		"deploy":    512,
		"notify":    256,
	}
	if limit, ok := limits[a.cfg.Role]; ok {
		return limit
	}
	return 2048
}

// parseArtifacts tìm artifacts trong LLM response
// Đơn giản hiện tại — có thể mở rộng sau với structured output
func (a *BaseAgent) parseArtifacts(content string, task *Task) []Artifact {
	var artifacts []Artifact

	// PR URL pattern
	if strings.Contains(content, "github.com") && strings.Contains(content, "/pull/") {
		artifacts = append(artifacts, Artifact{
			Kind: ArtifactPR,
			URL:  extractFirstURL(content, "github.com"),
			Meta: map[string]string{"task_id": task.ID},
		})
	}

	// Trello card URL
	if strings.Contains(content, "trello.com/c/") {
		artifacts = append(artifacts, Artifact{
			Kind: ArtifactTrello,
			URL:  extractFirstURL(content, "trello.com"),
			Meta: map[string]string{"task_id": task.ID},
		})
	}

	return artifacts
}

// trelloClientOrNil returns the cached Trello client, initialising it exactly
// once via sync.Once. Returns nil if credentials are missing or init fails.
func (a *BaseAgent) trelloClientOrNil() *trello.Client {
	a.trelloOnce.Do(func() {
		apiKey := a.cfg.Env["TRELLO_API_KEY"]
		token := a.cfg.Env["TRELLO_TOKEN"]
		if apiKey == "" || token == "" {
			log.Debug().Str("agent", a.cfg.ID).
				Msg("Trello not configured (TRELLO_API_KEY/TRELLO_TOKEN missing) — skipping client init")
			return
		}
		c, err := trello.New(apiKey, token)
		if err != nil {
			log.Error().Err(err).Str("agent", a.cfg.ID).Msg("trello client init failed")
			return
		}
		a.trelloClient = c
	})
	return a.trelloClient
}

// pushToTrello parses the breakdown agent's JSON ticket output and creates
// one Trello card per ticket. Returns an Artifact for each card created.
// Requires TRELLO_API_KEY, TRELLO_TOKEN, TRELLO_LIST_ID in cfg.Env.
// Failures are logged and skipped — the task result is not aborted.
func (a *BaseAgent) pushToTrello(ctx context.Context, llmOutput string, task *Task) []Artifact {
	listID := a.cfg.Env["TRELLO_LIST_ID"]
	if listID == "" {
		log.Debug().Str("agent", a.cfg.ID).
			Msg("Trello not configured (TRELLO_LIST_ID missing) — skipping card creation")
		return nil
	}

	client := a.trelloClientOrNil()
	if client == nil {
		return nil
	}

	tickets, err := trello.ParseTickets(llmOutput)
	if err != nil {
		log.Error().Err(err).Str("agent", a.cfg.ID).Str("task", task.ID).
			Msg("failed to parse tickets from LLM output")
		return nil
	}

	log.Info().Str("agent", a.cfg.ID).Int("tickets", len(tickets)).
		Msg("creating Trello cards")

	var artifacts []Artifact
	for _, ticket := range tickets {
		card, err := client.CreateCard(ctx, trello.Card{
			Name:        ticket.Title,
			Description: trello.FormatCardDescription(ticket),
			ListID:      listID,
		})
		if err != nil {
			log.Error().Err(err).Str("title", ticket.Title).Msg("trello card creation failed")
			continue
		}
		log.Info().Str("card", card.ID).Str("url", card.ShortURL).
			Str("title", card.Name).Msg("trello card created")
		artifacts = append(artifacts, Artifact{
			Kind: ArtifactTrello,
			URL:  card.ShortURL,
			Meta: map[string]string{
				"task_id":    task.ID,
				"card_id":    card.ID,
				"card_title": card.Name,
				"list_id":    listID,
			},
		})
	}
	return artifacts
}

func extractFirstURL(content, domain string) string {
	idx := strings.Index(content, "https://"+domain)
	if idx == -1 {
		idx = strings.Index(content, "http://"+domain)
	}
	if idx == -1 {
		return ""
	}
	end := strings.IndexAny(content[idx:], " \n\t\")")
	if end == -1 {
		return content[idx:]
	}
	return content[idx : idx+end]
}
