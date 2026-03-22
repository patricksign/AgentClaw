package reasoning

import (
	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/port"
)

// defaultPolicy is the package-level policy used by WithThinking.
// Immutable after init — do not modify at runtime.
var defaultPolicy = *DefaultPolicy()

// WithThinking enriches an LLMRequest with thinking configuration
// if the policy determines reasoning is appropriate for the given
// model, phase, and complexity. Returns the request unchanged if
// thinking is not needed or already explicitly configured.
func WithThinking(req port.LLMRequest, phase domain.ExecutionPhase, complexity string) port.LLMRequest {
	// Respect explicit caller override.
	if req.Thinking != nil {
		return req
	}

	cfg := ThinkingFor(req.Model, phase, complexity)
	if cfg != nil {
		req.Thinking = cfg
	}
	return req
}

// ThinkingFor returns a ThinkingConfig if reasoning is recommended
// for the given model, phase, and complexity. Returns nil otherwise.
func ThinkingFor(model string, phase domain.ExecutionPhase, complexity string) *port.ThinkingConfig {
	p := &defaultPolicy
	if !p.ShouldReason(model, phase, complexity) {
		return nil
	}

	budget := p.BudgetFor(model, phase)
	if budget <= 0 {
		return nil
	}

	return &port.ThinkingConfig{
		Enabled:      true,
		BudgetTokens: budget,
	}
}

// WithThinkingPolicy is like WithThinking but uses a custom policy.
func WithThinkingPolicy(req port.LLMRequest, phase domain.ExecutionPhase, complexity string, policy *Policy) port.LLMRequest {
	if req.Thinking != nil {
		return req
	}

	if !policy.ShouldReason(req.Model, phase, complexity) {
		return req
	}

	budget := policy.BudgetFor(req.Model, phase)
	if budget <= 0 {
		return req
	}

	req.Thinking = &port.ThinkingConfig{
		Enabled:      true,
		BudgetTokens: budget,
	}
	return req
}
