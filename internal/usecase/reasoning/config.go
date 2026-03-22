package reasoning

import "github.com/patricksign/AgentClaw/internal/domain"

// Policy determines when extended thinking should be enabled
// and with what token budget, based on model tier, execution phase,
// and task complexity.
type Policy struct {
	// MinComplexityForWorker is the minimum complexity level
	// at which worker-tier models get thinking enabled.
	// Default: "L" (only large tasks).
	MinComplexityForWorker string

	// MinComplexityForSmart is the minimum complexity level
	// at which smart-tier models (Sonnet) get thinking enabled.
	// Default: "M" (medium and large tasks).
	MinComplexityForSmart string

	// ExpertAlwaysThinks controls whether expert-tier models (Opus)
	// always use thinking regardless of complexity.
	// Default: true.
	ExpertAlwaysThinks bool
}

// DefaultPolicy returns the recommended reasoning policy.
func DefaultPolicy() *Policy {
	return &Policy{
		MinComplexityForWorker: "L",
		MinComplexityForSmart:  "M",
		ExpertAlwaysThinks:     true,
	}
}

// ShouldReason returns true if thinking should be enabled for
// the given model, phase, and complexity combination.
func (p *Policy) ShouldReason(model string, phase domain.ExecutionPhase, complexity string) bool {
	if !domain.SupportsExtendedThinking(model) {
		return false
	}

	tier := domain.TierFor(model)

	switch {
	case tier >= domain.TierExpert:
		return p.ExpertAlwaysThinks
	case tier == domain.TierSmart:
		return complexityAtLeast(complexity, p.MinComplexityForSmart)
	case tier == domain.TierWorker:
		return complexityAtLeast(complexity, p.MinComplexityForWorker)
	default:
		return false
	}
}

// BudgetFor returns the thinking token budget for the given model and phase.
// Returns 0 if thinking is not supported.
func (p *Policy) BudgetFor(model string, phase domain.ExecutionPhase) int {
	return domain.DefaultThinkingBudget(model, phase)
}

// complexityAtLeast returns true if actual complexity >= required complexity.
// Order: S < M < L.
func complexityAtLeast(actual, required string) bool {
	return complexityRank(actual) >= complexityRank(required)
}

func complexityRank(c string) int {
	switch c {
	case "S":
		return 1
	case "M":
		return 2
	case "L":
		return 3
	default:
		return 0
	}
}
