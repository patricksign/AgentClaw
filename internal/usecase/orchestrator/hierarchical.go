package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/port"
)

const breakdownSystem = `Break down this task into subtasks for specialist agents.
Return ONLY compact JSON array (no whitespace, no markdown fences):
[{"title":"...","role":"coding|test|docs|review","complexity":"S|M|L","model":"minimax|glm5|glm-flash|haiku|sonnet"}]
Max 8 subtasks.`

const assignSystem = `Review these subtasks and optimize assignment.
Confirm or reassign models based on complexity.
Return same JSON format with updated model fields.`

// subtaskDef is the JSON shape returned by the LLM for task breakdown.
type subtaskDef struct {
	Title      string `json:"title"`
	Role       string `json:"role"`
	Complexity string `json:"complexity"`
	Model      string `json:"model"`
}

// HierarchicalOrchestrator implements the Opus→Sonnet→execute pattern:
// Opus breaks down → Sonnet assigns → GLM/MiniMax executes in parallel.
type HierarchicalOrchestrator struct {
	router    port.LLMRouter
	notifier  port.Notifier
	taskStore port.TaskStore
}

// NewHierarchicalOrchestrator creates a hierarchical orchestrator.
func NewHierarchicalOrchestrator(
	router port.LLMRouter,
	notifier port.Notifier,
	taskStore port.TaskStore,
) *HierarchicalOrchestrator {
	return &HierarchicalOrchestrator{
		router:    router,
		notifier:  notifier,
		taskStore: taskStore,
	}
}

// Run breaks a parent task into subtasks via Opus+Sonnet, then executes them in parallel.
func (h *HierarchicalOrchestrator) Run(
	ctx context.Context,
	parentTask *domain.Task,
	mem port.MemoryContext,
	parallelOrch *ParallelOrchestrator,
) ([]*domain.TaskResult, error) {

	// ── Step 1: Opus breakdown ──────────────────────────────────────────────

	userMsg := fmt.Sprintf("Task: %s\n\nDescription: %s\n\nProject context:\n%s",
		parentTask.Title, parentTask.Description, truncate(mem.ProjectDoc, 600*4))

	breakdownCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	resp, err := h.router.Call(breakdownCtx, port.LLMRequest{
		Model:     domain.ModelOpus,
		System:    breakdownSystem,
		Messages:  []port.LLMMessage{{Role: "user", Content: userMsg}},
		MaxTokens: 4096,
		TaskID:    parentTask.ID,
	})
	cancel()
	if err != nil {
		return nil, fmt.Errorf("hierarchical: %s breakdown: %w", domain.ModelOpus, err)
	}

	raw := stripMarkdownFences(resp.Content)
	var subtaskDefs []subtaskDef
	if err := json.Unmarshal([]byte(raw), &subtaskDefs); err != nil {
		return nil, fmt.Errorf("hierarchical: parse breakdown JSON: %w", err)
	}
	if len(subtaskDefs) == 0 {
		return nil, fmt.Errorf("hierarchical: %s returned 0 subtasks", domain.ModelOpus)
	}
	if len(subtaskDefs) > 8 {
		subtaskDefs = subtaskDefs[:8]
	}

	// ── Step 2: Sonnet assigns ──────────────────────────────────────────────

	subtasksJSON, _ := json.Marshal(subtaskDefs)
	assignMsg := fmt.Sprintf("Subtasks:\n%s\n\nAgent context:\n%s", subtasksJSON, mem.AgentDoc)

	assignCtx, assignCancel := context.WithTimeout(ctx, 60*time.Second)
	assignResp, err := h.router.Call(assignCtx, port.LLMRequest{
		Model:     domain.ModelSonnet,
		System:    assignSystem,
		Messages:  []port.LLMMessage{{Role: "user", Content: assignMsg}},
		MaxTokens: 4096,
		TaskID:    parentTask.ID,
	})
	assignCancel()
	if err != nil {
		return nil, fmt.Errorf("hierarchical: %s assign: %w", domain.ModelSonnet, err)
	}

	assignRaw := stripMarkdownFences(assignResp.Content)
	var confirmed []subtaskDef
	if err := json.Unmarshal([]byte(assignRaw), &confirmed); err != nil {
		// Fall back to original breakdown if Sonnet fails to parse.
		confirmed = subtaskDefs
	}

	// ── Step 3: Create domain.Task for each subtask ─────────────────────────

	now := time.Now()
	tasks := make([]*domain.Task, 0, len(confirmed))
	for _, sd := range confirmed {
		t := &domain.Task{
			ID:          generateID(),
			Title:       sd.Title,
			Description: fmt.Sprintf("Subtask of: %s", parentTask.Title),
			AgentRole:   sd.Role,
			Status:      "pending",
			Complexity:  sd.Complexity,
			Phase:       domain.PhaseUnderstand,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := h.taskStore.SaveTask(t); err != nil {
			return nil, fmt.Errorf("hierarchical: save subtask %q: %w", sd.Title, err)
		}
		tasks = append(tasks, t)
	}

	// ── Step 4: Delegate to parallel orchestrator ───────────────────────────

	return parallelOrch.Run(ctx, tasks, mem)
}

// truncate returns the first n characters of s, appending "…" if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// generateID returns a random 16-byte hex string as a task ID.
// Uses crypto/rand for uniqueness without external dependencies.
func generateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// stripMarkdownFences removes ```json ... ``` wrappers from LLM output.
func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
	}
	return strings.TrimSpace(s)
}
