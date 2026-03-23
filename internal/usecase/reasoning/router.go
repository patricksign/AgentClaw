package reasoning

import (
	"context"
	"log/slog"

	"github.com/patricksign/AgentClaw/internal/port"
)

// Compile-time check: ReasoningRouter implements port.LLMRouter.
var _ port.LLMRouter = (*ReasoningRouter)(nil)

// ReasoningRouter is a decorator around port.LLMRouter that handles
// extended thinking transparently. When a request has Thinking enabled,
// the router logs thinking usage. When Thinking is nil, the request
// passes through unchanged.
type ReasoningRouter struct {
	inner port.LLMRouter
}

// NewReasoningRouter wraps an existing LLMRouter with reasoning awareness.
func NewReasoningRouter(inner port.LLMRouter) *ReasoningRouter {
	return &ReasoningRouter{inner: inner}
}

// Call delegates to the inner router. When thinking is enabled, it logs
// thinking token usage for observability.
func (r *ReasoningRouter) Call(ctx context.Context, req port.LLMRequest) (*port.LLMResponse, error) {
	resp, err := r.inner.Call(ctx, req)
	if err != nil {
		return nil, err
	}

	// Log thinking usage when it was enabled and produced output.
	if req.Thinking != nil && req.Thinking.Enabled && resp.ThinkingTokens > 0 {
		slog.Debug("reasoning: extended thinking completed", "model", req.Model, "task", req.TaskID, "budget", req.Thinking.BudgetTokens, "thinking_tokens", resp.ThinkingTokens, "output_tokens", resp.OutputTokens)
	}

	return resp, nil
}

// Stats delegates to the inner router.
func (r *ReasoningRouter) Stats() map[string]int64 {
	return r.inner.Stats()
}
