package memory

import (
	"database/sql"
	"time"

	"github.com/patricksign/AgentClaw/internal/state"
)

// dbTimeout is the default timeout for SQLite operations.
// SQLite is local disk I/O; 5s is generous for any single query.
const dbTimeout = 5 * time.Second

// Store manages the 3-layer memory architecture.
type Store struct {
	db          *sql.DB
	projectPath string // path to project.md
	resolved    *state.ResolvedStore
	scope       *state.ScopeStore
	agentDoc    *state.AgentDocStore
	scratchpad  *state.Scratchpad
}

// ─── Token Logs ──────────────────────────────────────────────────────────────

type TokenLog struct {
	TaskID       string    `json:"task_id"`
	AgentID      string    `json:"agent_id"`
	Model        string    `json:"model"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CostUSD      float64   `json:"cost_usd"`
	DurationMs   int64     `json:"duration_ms"`
	CreatedAt    time.Time `json:"created_at"`
}

// ─── Metrics ─────────────────────────────────────────────────────────────────

type PeriodStats struct {
	Period       string  `json:"period"`
	TotalTasks   int     `json:"total_tasks"`
	DoneTasks    int     `json:"done_tasks"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCostUSD float64 `json:"total_cost_usd"`
}
