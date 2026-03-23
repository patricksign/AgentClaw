package guard

import (
	"context"
	"fmt"

	"github.com/patricksign/AgentClaw/internal/port"
)

// Compile-time check.
var _ port.LLMRouter = (*GuardedLLM)(nil)

// GuardedLLM wraps a port.LLMRouter with input/output guard checks.
// Scans prompts for injection before sending to LLM,
// and scans responses for dangerous content before returning.
type GuardedLLM struct {
	inner port.LLMRouter
	guard port.Guard
}

// NewGuardedLLM wraps an existing LLMRouter with guard enforcement.
func NewGuardedLLM(inner port.LLMRouter, guard port.Guard) *GuardedLLM {
	return &GuardedLLM{inner: inner, guard: guard}
}

// Stats delegates to the inner router.
func (g *GuardedLLM) Stats() map[string]int64 {
	return g.inner.Stats()
}

// Call scans input messages for injection, executes the LLM call,
// then scans the response for dangerous content.
func (g *GuardedLLM) Call(ctx context.Context, req port.LLMRequest) (*port.LLMResponse, error) {
	// Scan system prompt.
	if req.System != "" {
		if v := g.guard.CheckLLMInput(ctx, req.System); v != nil && v.Blocked {
			return nil, fmt.Errorf("guard: system prompt blocked [%s] %s", v.Rule, v.Reason)
		}
	}

	// Scan user messages.
	for _, msg := range req.Messages {
		if msg.Role == "user" {
			if v := g.guard.CheckLLMInput(ctx, msg.Content); v != nil && v.Blocked {
				return nil, fmt.Errorf("guard: user message blocked [%s] %s", v.Rule, v.Reason)
			}
		}
	}

	// Execute LLM call.
	resp, err := g.inner.Call(ctx, req)
	if err != nil {
		return nil, err
	}

	// Scan output for dangerous content.
	if v := g.guard.CheckLLMOutput(ctx, resp.Content); v != nil && v.Blocked {
		return nil, fmt.Errorf("guard: LLM output blocked [%s] %s", v.Rule, v.Reason)
	}

	return resp, nil
}
