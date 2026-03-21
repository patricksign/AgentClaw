package domain

import "time"

// PhaseCheckpoint captures the full state of a phase execution at the moment
// it suspends (e.g., to escalate a question to a higher-tier model or human).
// This allows the phase to resume from exactly where it stopped, without
// re-running earlier steps or re-calling the LLM.
type PhaseCheckpoint struct {
	// Identity
	TaskID  string         `json:"task_id"`
	AgentID string         `json:"agent_id"`
	Phase   ExecutionPhase `json:"phase"`

	// Progress within the phase
	StepIndex int    `json:"step_index"` // which sub-step within the phase (0-based)
	StepName  string `json:"step_name"`  // human-readable step name for debugging

	// Accumulated context from completed sub-steps — carries forward on resume.
	// Keys are phase-specific, e.g. "understanding", "qa_resolved", "plan_draft".
	Accumulated map[string]string `json:"accumulated"`

	// Escalation state
	PendingQuery   string `json:"pending_query,omitempty"`   // question text sent upstream
	PendingQueryID string `json:"pending_query_id,omitempty"` // question ID for reply routing
	SuspendedModel string `json:"suspended_model"`            // model that triggered the suspend
	EscalatedTo    string `json:"escalated_to"`               // model/human the question was sent to

	// LLM conversation context — preserved so resume doesn't re-pay input tokens.
	LastSystemPrompt string       `json:"last_system_prompt,omitempty"`
	LastMessages     []LLMMessage `json:"last_messages,omitempty"`
	LastResponse     string       `json:"last_response,omitempty"`

	// Token accounting — accumulated before suspend, restored on resume.
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`

	SavedAt time.Time `json:"saved_at"`
}

// LLMMessage is a minimal message struct for checkpoint serialization.
// Mirrors port.LLMMessage but lives in domain to avoid import cycles.
type LLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// HasPendingQuery returns true if the checkpoint has an unanswered question.
func (c *PhaseCheckpoint) HasPendingQuery() bool {
	return c.PendingQuery != ""
}

// AddAccumulated merges a key-value pair into the accumulated context.
func (c *PhaseCheckpoint) AddAccumulated(key, value string) {
	if c.Accumulated == nil {
		c.Accumulated = make(map[string]string)
	}
	c.Accumulated[key] = value
}

// GetAccumulated returns the value for a key, or empty string if not found.
func (c *PhaseCheckpoint) GetAccumulated(key string) string {
	if c.Accumulated == nil {
		return ""
	}
	return c.Accumulated[key]
}

// ClearPendingQuery resets the pending query fields after the answer arrives.
func (c *PhaseCheckpoint) ClearPendingQuery() {
	c.PendingQuery = ""
	c.PendingQueryID = ""
	c.EscalatedTo = ""
}
