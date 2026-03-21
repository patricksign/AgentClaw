package domain

import "time"

// ─── Execution Phase ────────────────────────────────────────────────────────

type ExecutionPhase string

const (
	PhaseUnderstand ExecutionPhase = "understand"
	PhaseClarify    ExecutionPhase = "clarify"
	PhasePlan       ExecutionPhase = "plan"
	PhaseImplement  ExecutionPhase = "implement"
	PhaseDone       ExecutionPhase = "done"
)

// Question is a clarification question raised during the understand phase.
type Question struct {
	ID         string    `json:"id"`
	Text       string    `json:"text"`
	Answer     string    `json:"answer"`
	AnsweredBy string    `json:"answered_by"` // "haiku" | "sonnet" | "opus" | "human" | "cache"
	Resolved   bool      `json:"resolved"`
	CreatedAt  time.Time `json:"created_at"`
}

// AgentConfig identifies an agent and its LLM model.
type AgentConfig struct {
	ID    string `json:"id"`
	Role  string `json:"role"`
	Model string `json:"model"`
}

// Task is the domain-level unit of work flowing through the pipeline.
type Task struct {
	ID             string         `json:"id"`
	Title          string         `json:"title"`
	Description    string         `json:"description"`
	AgentID        string         `json:"agent_id"`
	AgentRole      string         `json:"agent_role"`
	Status         string         `json:"status"`
	Phase          ExecutionPhase `json:"phase"`
	Complexity     string         `json:"complexity"` // S | M | L
	Tags           []string       `json:"tags"`
	Understanding  string         `json:"understanding"`
	Assumptions    []string       `json:"assumptions"`
	Risks          []string       `json:"risks"`
	Questions      []Question     `json:"questions"`
	ImplementPlan  string         `json:"implement_plan"`
	PlanApprovedBy string         `json:"plan_approved_by"`
	RedirectCount  int            `json:"redirect_count"`
	PhaseStartedAt time.Time      `json:"phase_started_at"`
	Output         string         `json:"output"`
	InputTokens    int64          `json:"input_tokens"`
	OutputTokens   int64          `json:"output_tokens"`
	CostUSD        float64        `json:"cost_usd"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}
