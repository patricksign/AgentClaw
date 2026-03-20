package port

import (
	"context"

	"github.com/patricksign/agentclaw/internal/domain"
)

// EscalatorRequest contains the context needed to resolve a question
// that an agent could not answer on its own.
type EscalatorRequest struct {
	Question    string `json:"question"`
	TaskContext string `json:"task_context"`
	AgentModel  string `json:"agent_model"`
	AgentRole   string `json:"agent_role"`
	TaskID      string `json:"task_id"`
	QuestionID  string `json:"question_id"`
}

// Escalator resolves questions by trying progressively more expensive
// strategies: cache lookup, cheaper model, more capable model, human.
type Escalator interface {
	Resolve(ctx context.Context, req EscalatorRequest) (domain.EscalationResult, error)
}
