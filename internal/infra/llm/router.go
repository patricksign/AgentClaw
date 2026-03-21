package llm

import (
	"context"

	"github.com/patricksign/AgentClaw/internal/port"

	llmcore "github.com/patricksign/AgentClaw/internal/llm"
)

// Compile-time check: Router implements port.LLMRouter.
var _ port.LLMRouter = (*Router)(nil)

// Router wraps the core llm.Router to satisfy port.LLMRouter.
type Router struct {
	core *llmcore.Router
}

// NewRouter creates an infra Router wrapping the core LLM router.
func NewRouter(core *llmcore.Router) *Router {
	return &Router{core: core}
}

// NewRouterWithEnv creates an infra Router with per-agent key overrides.
func NewRouterWithEnv(env map[string]string) *Router {
	return &Router{core: llmcore.NewRouterWithEnv(env)}
}

// Call translates port types to core types and delegates to the core router.
func (r *Router) Call(ctx context.Context, req port.LLMRequest) (*port.LLMResponse, error) {
	coreMessages := make([]llmcore.Message, len(req.Messages))
	for i, m := range req.Messages {
		coreMessages[i] = llmcore.Message{Role: m.Role, Content: m.Content}
	}

	coreReq := llmcore.Request{
		Model:     req.Model,
		System:    req.System,
		Messages:  coreMessages,
		MaxTokens: req.MaxTokens,
		TaskID:    req.TaskID,
		BatchMode: req.BatchMode,
	}

	// Map cache control from port to core types.
	if req.CacheControl != nil {
		coreReq.CacheControl = &llmcore.CacheControl{
			CacheSystem: req.CacheControl.CacheSystem,
			TTL:         req.CacheControl.TTL,
		}
	}

	resp, err := r.core.Call(ctx, coreReq)
	if err != nil {
		return nil, err
	}

	return &port.LLMResponse{
		Content:      resp.Content,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		CacheTokens:  resp.CacheTokens,
		CostUSD:      resp.CostUSD,
		CostMode:     string(resp.CostMode),
		DurationMs:   resp.DurationMs,
		ModelUsed:    resp.ModelUsed,
	}, nil
}

// Stats returns per-provider call statistics from the core router.
func (r *Router) Stats() map[string]int64 {
	return r.core.Stats()
}
