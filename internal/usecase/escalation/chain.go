package escalation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/patricksign/agentclaw/internal/domain"
	"github.com/patricksign/agentclaw/internal/port"
)

// HumanAsker abstracts the human notification channel (Telegram, Slack, etc.).
type HumanAsker interface {
	AskHuman(ctx context.Context, agentID, taskID, taskTitle, questionID, question string) (msgID int, err error)
	RegisterReply(msgID int, taskID, questionID string) <-chan string
}

// Chain implements port.Escalator using a multi-level resolution strategy:
// cache → cheaper model → more capable model → human.
type Chain struct {
	router   port.LLMRouter
	notifier port.Notifier
	cache    *Cache
	asker    HumanAsker
}

// NewChain creates an escalation chain with all dependencies.
func NewChain(router port.LLMRouter, notifier port.Notifier, cache *Cache, asker HumanAsker) *Chain {
	return &Chain{
		router:   router,
		notifier: notifier,
		cache:    cache,
		asker:    asker,
	}
}

// Resolve implements port.Escalator.
func (c *Chain) Resolve(ctx context.Context, req port.EscalatorRequest) (domain.EscalationResult, error) {
	// Step 1 — Check cache.
	if answer, hit := c.cache.Check(req.Question, req.AgentRole); hit {
		c.dispatch(ctx, domain.EventQuestionAnswered, domain.StatusChannel, req, map[string]string{
			"message":     "Resolved from cache",
			"answered_by": "cache",
		})
		return domain.EscalationResult{Answer: answer, AnsweredBy: "cache", Resolved: true}, nil
	}

	// Step 2 — Determine starting level.
	startLevel := c.levelFor(req.AgentModel)

	// Step 3 — Try each model level with 30s timeout.
	for level := startLevel; level != "human"; level = c.nextLevel(level) {
		answer, confident, tryErr := c.tryAt(ctx, level, req.Question, req.TaskContext, req.TaskID)
		if tryErr != nil {
			// Log but continue escalation — transient errors should not block the chain.
			c.dispatch(ctx, domain.EventEscalated, domain.StatusChannel, req, map[string]string{
				"message": fmt.Sprintf("LLM error at %s: %s — escalating", level, truncate(tryErr.Error(), 80)),
			})
			continue
		}
		if confident {
			c.cache.Save(req.Question, answer, req.AgentRole)
			c.dispatch(ctx, domain.EventEscalated, domain.StatusChannel, req, map[string]string{
				"message":     fmt.Sprintf("Question resolved by %s — Q: %s", level, truncate(req.Question, 80)),
				"answered_by": level,
			})
			return domain.EscalationResult{Answer: answer, AnsweredBy: level, Resolved: true}, nil
		}
	}

	// Step 4 — Escalate to human.
	msgID, err := c.asker.AskHuman(ctx, req.AgentModel, req.TaskID, "", req.QuestionID, req.Question)
	if err != nil {
		return domain.EscalationResult{}, fmt.Errorf("escalation: ask human: %w", err)
	}
	answerCh := c.asker.RegisterReply(msgID, req.TaskID, req.QuestionID)

	c.dispatch(ctx, domain.EventEscalated, domain.HumanChannel, req, map[string]string{
		"message": fmt.Sprintf("Full escalation chain exhausted — needs human. Path: %s → %s → human",
			req.AgentModel, startLevel),
	})

	waitCtx, cancel := context.WithTimeout(ctx, 24*time.Hour)
	defer cancel()

	select {
	case answer, ok := <-answerCh:
		if !ok {
			return domain.EscalationResult{}, fmt.Errorf("question expired")
		}
		c.cache.Save(req.Question, answer, req.AgentRole)
		return domain.EscalationResult{Answer: answer, AnsweredBy: "human", Resolved: true}, nil
	case <-waitCtx.Done():
		return domain.EscalationResult{NeedsHuman: true}, nil
	}
}

// levelFor determines which model to try first based on the agent's own model.
func (c *Chain) levelFor(model string) string {
	switch {
	case strings.HasPrefix(model, "glm"), strings.HasPrefix(model, "minimax"):
		return "sonnet"
	case model == "sonnet":
		return "opus"
	case model == "opus":
		return "human"
	default:
		return "sonnet"
	}
}

// nextLevel returns the next escalation level.
func (c *Chain) nextLevel(level string) string {
	switch level {
	case "sonnet":
		return "opus"
	case "opus":
		return "human"
	default:
		return "human"
	}
}

// tryAt calls the LLM at the given model level with a 30s timeout.
// Returns (answer, true, nil) if the model is confident.
// Returns ("", false, nil) if the model says ESCALATE or the answer is too long.
// Returns ("", false, err) on LLM failure — caller can distinguish from explicit ESCALATE.
func (c *Chain) tryAt(ctx context.Context, level, question, taskContext, taskID string) (string, bool, error) {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := c.router.Call(callCtx, port.LLMRequest{
		Model:  level,
		System: "Answer the following question concisely. If you cannot answer confidently, respond with ESCALATE.",
		Messages: []port.LLMMessage{
			{Role: "user", Content: fmt.Sprintf("Context: %s\n\nQuestion: %s", taskContext, question)},
		},
		MaxTokens: 1024,
		TaskID:    taskID,
	})
	if err != nil {
		return "", false, fmt.Errorf("tryAt(%s): %w", level, err)
	}

	content := strings.TrimSpace(resp.Content)
	if strings.HasPrefix(strings.ToUpper(content), "ESCALATE") || len(content) > 800 {
		return "", false, nil
	}
	return content, true, nil
}

// dispatch is a convenience helper for firing events.
func (c *Chain) dispatch(ctx context.Context, evtType domain.EventType, ch domain.Channel, req port.EscalatorRequest, payload map[string]string) {
	_ = c.notifier.Dispatch(ctx, domain.Event{
		Type:       evtType,
		Channel:    ch,
		TaskID:     req.TaskID,
		AgentRole:  req.AgentRole,
		Payload:    payload,
		OccurredAt: time.Now(),
	})
}

// truncate returns the first n characters of s, appending "…" if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
