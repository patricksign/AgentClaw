package summarizer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/patricksign/AgentClaw/internal/agent"
	"github.com/patricksign/AgentClaw/internal/llm"
	"github.com/rs/zerolog/log"
)

const (
	minTasksRequired = 10
	recentTasksLimit = 50
	summaryMaxTokens = 500
	summaryModel     = "sonnet"
	systemPrompt     = "Summarize this agent's recent work into a 400-token memory document. Focus on: patterns used, pitfalls encountered, decisions made, modules touched. Write as reference material in present tense."
)

// TaskStore is the subset of memory.Store used by Summarizer.
type TaskStore interface {
	RecentByRole(role string, n int) ([]*agent.Task, error)
	LogTokenUsage(taskID, agentID, model string, in, out int64, cost float64, durationMs int64) error
}

// DocStore is the subset of state.AgentDocStore used by Summarizer.
type DocStore interface {
	Append(agentID, section string) error
}

// LLMRouter is the subset of llm.Router used by Summarizer.
type LLMRouter interface {
	Call(ctx context.Context, req llm.Request) (*llm.Response, error)
}

// Summarizer compresses agent task history into memory documents.
type Summarizer struct {
	store    TaskStore
	docs     DocStore
	router   LLMRouter
	stateDir string // base state dir, archives go to stateDir/old/
}

// New creates a Summarizer. stateDir is the base state directory (e.g. "./state").
func New(store TaskStore, docs DocStore, router LLMRouter, stateDir string) *Summarizer {
	return &Summarizer{
		store:    store,
		docs:     docs,
		router:   router,
		stateDir: stateDir,
	}
}

// CompressAgentHistory summarizes the last 50 done/failed tasks for the given
// agent role and appends the result to the agent's memory doc.
// Returns (costUSD, summaryLength, error).
// Returns (0, 0, nil) if fewer than minTasksRequired tasks exist.
func (s *Summarizer) CompressAgentHistory(ctx context.Context, agentID, role string) (float64, int, error) {
	tasks, err := s.store.RecentByRole(role, recentTasksLimit)
	if err != nil {
		return 0, 0, fmt.Errorf("summarizer: load tasks for %s: %w", role, err)
	}
	if len(tasks) < minTasksRequired {
		log.Debug().
			Str("agent", agentID).
			Int("tasks", len(tasks)).
			Msg("summarizer: not enough tasks, skipping")
		return 0, 0, nil
	}

	userMsg := buildTaskListMessage(agentID, role, tasks)

	resp, err := s.router.Call(ctx, llm.Request{
		Model:     summaryModel,
		System:    systemPrompt,
		Messages:  []llm.Message{{Role: "user", Content: userMsg}},
		MaxTokens: summaryMaxTokens,
		TaskID:    "summarizer-" + agentID,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("summarizer: llm call for %s: %w", agentID, err)
	}

	summary := resp.Content

	if err := s.docs.Append(agentID, summary); err != nil {
		return 0, 0, fmt.Errorf("summarizer: append doc for %s: %w", agentID, err)
	}

	if err := s.archiveTasks(agentID, tasks, userMsg); err != nil {
		// Non-fatal: log and continue.
		log.Warn().Err(err).Str("agent", agentID).Msg("summarizer: archive failed")
	}

	if err := s.store.LogTokenUsage(
		"summarizer-"+agentID, agentID, summaryModel,
		resp.InputTokens, resp.OutputTokens, resp.CostUSD, resp.DurationMs,
	); err != nil {
		log.Warn().Err(err).Str("agent", agentID).Msg("summarizer: log tokens failed")
	}

	log.Info().
		Str("agent", agentID).
		Int64("input_tokens", resp.InputTokens).
		Int64("output_tokens", resp.OutputTokens).
		Float64("cost_usd", resp.CostUSD).
		Int("summary_length", len(summary)).
		Msg("summarizer: compressed agent history")

	return resp.CostUSD, len(summary), nil
}

// CompressAll runs CompressAgentHistory for each agent config sequentially.
// Returns the total cost and any first error encountered.
func (s *Summarizer) CompressAll(ctx context.Context, agents []agent.Config) (float64, error) {
	var totalCost float64
	for _, cfg := range agents {
		cost, _, err := s.CompressAgentHistory(ctx, cfg.ID, cfg.Role)
		if err != nil {
			log.Error().Err(err).Str("agent", cfg.ID).Msg("summarizer: CompressAll error")
			return totalCost, fmt.Errorf("summarizer: compress %s: %w", cfg.ID, err)
		}
		totalCost += cost
	}
	log.Info().Float64("total_cost_usd", totalCost).Msg("summarizer: CompressAll done")
	return totalCost, nil
}

// buildTaskListMessage formats the task list as a user message for the LLM.
func buildTaskListMessage(agentID, role string, tasks []*agent.Task) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Agent: %s (role: %s)\nRecent completed tasks (%d):\n\n", agentID, role, len(tasks)))
	for _, t := range tasks {
		t.Lock()
		sb.WriteString(fmt.Sprintf("- [%s] %s: %s (cost: $%.6f)\n", t.Status, t.ID, t.Title, t.CostUSD))
		t.Unlock()
	}
	return sb.String()
}

// archiveTasks writes the raw task list to stateDir/old/summary-<agentID>-<date>.md.
func (s *Summarizer) archiveTasks(agentID string, tasks []*agent.Task, content string) error {
	dir := filepath.Join(s.stateDir, "old")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("summarizer: mkdir %s: %w", dir, err)
	}
	date := time.Now().Format("2006-01-02")
	path := filepath.Join(dir, fmt.Sprintf("summary-%s-%s.md", agentID, date))
	header := fmt.Sprintf("# Archive: %s — %s\n\nTask count: %d\n\n", agentID, date, len(tasks))
	data := []byte(header + content)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("summarizer: write archive %s: %w", path, err)
	}
	return nil
}
