package reasoning

import (
	"context"

	"github.com/patricksign/AgentClaw/internal/port"
	"github.com/rs/zerolog/log"
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
		log.Debug().
			Str("model", req.Model).
			Str("task", req.TaskID).
			Int("budget", req.Thinking.BudgetTokens).
			Int64("thinking_tokens", resp.ThinkingTokens).
			Int64("output_tokens", resp.OutputTokens).
			Msg("reasoning: extended thinking completed")
	}

	return resp, nil
}

// Stats delegates to the inner router.
func (r *ReasoningRouter) Stats() map[string]int64 {
	return r.inner.Stats()
}
