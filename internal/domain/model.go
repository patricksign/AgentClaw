package domain

// ModelTier represents the capability tier of an LLM model.
// Higher tier = more capable, more expensive.
type ModelTier int

const (
	TierFlash  ModelTier = 10 // glm-flash — cheapest, fastest
	TierWorker ModelTier = 20 // haiku, glm5, minimax — mid-tier workers
	TierSmart  ModelTier = 30 // sonnet — capable generalist
	TierExpert ModelTier = 40 // opus — senior reviewer / supervisor
	TierHuman  ModelTier = 50 // human — final authority
)

// Model name constants — single source of truth for all model strings.
const (
	ModelOpus            = "opus"
	ModelSonnet          = "sonnet"
	ModelHaiku           = "haiku"
	ModelMinimax         = "minimax"
	ModelMinimaxHS       = "minimax-highspeed"
	ModelKimi            = "kimi"
	ModelGLM5            = "glm5"
	ModelGLMFlash        = "glm-flash"
	ModelHuman           = "human"
	ModelCache           = "cache"
)

// TierFor returns the capability tier for a given model name.
func TierFor(model string) ModelTier {
	switch model {
	case ModelGLMFlash:
		return TierFlash
	case ModelHaiku, ModelGLM5, ModelMinimax, ModelMinimaxHS, ModelKimi:
		return TierWorker
	case ModelSonnet:
		return TierSmart
	case ModelOpus:
		return TierExpert
	case ModelHuman:
		return TierHuman
	default:
		return TierWorker
	}
}

// EscalationTarget returns the model to escalate to from the given model.
// Escalation order: glm-flash → sonnet → opus → human
//
//	haiku/glm5/minimax → sonnet → opus → human
func EscalationTarget(model string) string {
	tier := TierFor(model)
	switch {
	case tier <= TierWorker:
		return ModelSonnet
	case tier == TierSmart:
		return ModelOpus
	case tier >= TierExpert:
		return ModelHuman
	default:
		return ModelSonnet
	}
}

// NextEscalationLevel returns the next level in the escalation chain
// given the current escalation level (not the agent's own model).
func NextEscalationLevel(level string) string {
	switch level {
	case ModelSonnet:
		return ModelOpus
	case ModelOpus:
		return ModelHuman
	default:
		return ModelHuman
	}
}

// IsHumanLevel returns true if the model represents human intervention.
func IsHumanLevel(model string) bool {
	return model == ModelHuman
}

// CanEscalate returns true if the model can escalate to a higher tier.
func CanEscalate(model string) bool {
	return TierFor(model) < TierHuman
}

// EscalationChain returns the ordered list of models to try when escalating
// from the given starting model, ending with "human".
func EscalationChain(fromModel string) []string {
	target := EscalationTarget(fromModel)
	if target == ModelHuman {
		return []string{ModelHuman}
	}

	var chain []string
	for level := target; level != ModelHuman; level = NextEscalationLevel(level) {
		chain = append(chain, level)
	}
	chain = append(chain, ModelHuman)
	return chain
}

// SupervisorModel returns the model that should review/supervise work
// done by the given model. Used for plan review and loop review.
func SupervisorModel(workerModel string) string {
	tier := TierFor(workerModel)
	switch {
	case tier <= TierSmart:
		return ModelOpus
	default:
		return ModelOpus // opus reviews itself or human reviews
	}
}

// WorkerModelForComplexity returns the default worker model for a task complexity.
func WorkerModelForComplexity(complexity string) string {
	switch complexity {
	case "S":
		return ModelGLMFlash
	case "M":
		return ModelMinimax
	case "L":
		return ModelSonnet
	default:
		return ModelMinimax
	}
}

// ─── Prompt Cache ──────────────────────────────────────────────────────────

// ─── Extended Thinking ────────────────────────────────────────────────────

// SupportsExtendedThinking returns true if the model supports extended thinking
// (chain-of-thought reasoning before generating the final answer).
// Currently only Anthropic Opus and Sonnet support this capability.
func SupportsExtendedThinking(model string) bool {
	switch model {
	case ModelOpus, ModelSonnet:
		return true
	default:
		return false
	}
}

// DefaultThinkingBudget returns the recommended thinking token budget
// for the given model and execution phase. Returns 0 if thinking
// is not recommended for this combination.
func DefaultThinkingBudget(model string, phase ExecutionPhase) int {
	tier := TierFor(model)
	switch {
	case tier >= TierExpert: // Opus
		switch phase {
		case PhasePlan:
			return 10000
		case PhaseUnderstand:
			return 8000
		case PhaseImplement:
			return 6000
		default:
			return 5000
		}
	case tier == TierSmart: // Sonnet
		switch phase {
		case PhasePlan:
			return 4000
		case PhaseUnderstand:
			return 4000
		case PhaseImplement:
			return 3000
		default:
			return 2000
		}
	case tier == TierWorker: // MiniMax, Kimi, GLM5
		if phase == PhaseImplement {
			return 2000
		}
		return 0
	default: // Flash
		return 0
	}
}

// CacheTTL constants for Anthropic prompt caching.
const (
	CacheTTLEphemeral = "ephemeral" // 5 min — task-specific content
	CacheTTLPersist   = "1h"        // 1 hour — stable content (role identity, project doc)
)

// SupportsPromptCache returns true if the model supports Anthropic-style prompt caching.
func SupportsPromptCache(model string) bool {
	switch model {
	case ModelOpus, ModelSonnet, ModelHaiku:
		return true
	default:
		return false
	}
}

// CacheTTLForContent returns the optimal cache TTL based on content stability.
// Stable content (role identity, project context) uses 1h TTL.
// Task-specific content uses ephemeral (5 min) TTL.
func CacheTTLForContent(contentType string) string {
	switch contentType {
	case "system", "role_identity", "project_doc":
		return CacheTTLPersist // stable across tasks — cache for 1 hour
	default:
		return CacheTTLEphemeral // task-specific — cache for 5 min
	}
}
