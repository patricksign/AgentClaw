package port

import (
	"time"

	"github.com/patricksign/agentclaw/internal/domain"
)

// ─── Task Store ─────────────────────────────────────────────────────────────

// TaskFilter constrains which tasks are returned by ListTasks.
type TaskFilter struct {
	Role   string `json:"role"`
	Status string `json:"status"`
	Phase  string `json:"phase"`
	Limit  int    `json:"limit"`
}

// TaskStore persists and retrieves tasks.
type TaskStore interface {
	SaveTask(task *domain.Task) error
	GetTask(id string) (*domain.Task, error)
	ListTasks(filter TaskFilter) ([]domain.Task, error)
	ListByPhase(phase domain.ExecutionPhase) ([]domain.Task, error)
}

// ─── State Store ────────────────────────────────────────────────────────────

// AgentState is a snapshot of an agent's current operational state.
type AgentState struct {
	AgentID    string    `json:"agent_id"`
	Role       string    `json:"role"`
	Model      string    `json:"model"`
	Status     string    `json:"status"`
	TaskID     string    `json:"task_id"`
	TaskTitle  string    `json:"task_title"`
	Progress   string    `json:"progress"`
	Blockers   string    `json:"blockers"`
	TimeStuck  string    `json:"time_stuck"`
	LastOutput string    `json:"last_output"`
	UpdatedAt  time.Time `json:"updated_at"`

	// Performance metrics — updated after each task.
	LastTaskMetrics *TaskMetrics `json:"last_task_metrics,omitempty"`

	// Cumulative performance stats for this agent.
	TotalTasks     int     `json:"total_tasks"`
	TotalSuccesses int     `json:"total_successes"`
	TotalCostUSD   float64 `json:"total_cost_usd"`
	AvgDurationMs  int64   `json:"avg_duration_ms"`
	CacheHitRate   float64 `json:"cache_hit_rate"` // 0.0–1.0

	// SkillVersion tracks which version of the role skills was active.
	SkillVersion int `json:"skill_version"`
}

// TaskMetrics captures performance data from the most recent task execution.
type TaskMetrics struct {
	TaskID         string  `json:"task_id"`
	Success        bool    `json:"success"`
	CostUSD        float64 `json:"cost_usd"`
	InputTokens    int64   `json:"input_tokens"`
	OutputTokens   int64   `json:"output_tokens"`
	CacheHitTokens int64   `json:"cache_hit_tokens"`
	DurationMs     int64   `json:"duration_ms"`
	CostMode       string  `json:"cost_mode"`     // "cache_hit", "batch", etc.
	CostSavingsUSD float64 `json:"cost_savings"`  // how much saved vs standard pricing
}

// StateStore reads and writes per-agent runtime state.
type StateStore interface {
	WriteState(agentID string, state AgentState) error
	ReadState(agentID string) (*AgentState, error)
	ReadAllStates() ([]AgentState, error)
}

// ─── Memory Store ───────────────────────────────────────────────────────────

// MemoryContext is the assembled context injected into an agent before execution.
type MemoryContext struct {
	ProjectDoc  string `json:"project_doc"`
	AgentDoc    string `json:"agent_doc"`
	ScopeDoc    string `json:"scope_doc"`
	RecentTasks string `json:"recent_tasks"`
	ScratchPad  string `json:"scratch_pad"`
	KnownErrors string `json:"known_errors"`
}

// MemoryStore builds and manages the multi-layer memory context for agents.
type MemoryStore interface {
	BuildContext(agentID, role, taskTitle, complexity string) MemoryContext
	AppendProjectDoc(section string) error
}
