package agent

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/patricksign/agentclaw/internal/state"
)

// ─── Agent ───────────────────────────────────────────────────────────────────

type Status string

const (
	StatusIdle       Status = "idle"
	StatusRunning    Status = "running"
	StatusPaused     Status = "paused"
	StatusFailed     Status = "failed"
	StatusTerminated Status = "terminated"
)

type Config struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Role        string            `json:"role"`  // idea|architect|breakdown|coding|test|review|deploy|notify|docs
	Model       string            `json:"model"` // opus|sonnet|minimax|glm5|glm-flash
	MaxRetries  int               `json:"max_retries"`
	TimeoutSecs int               `json:"timeout_secs"`
	Tags        []string          `json:"tags"`
	Env         map[string]string `json:"env"`
}

// Agent is the interface every agent must implement.
type Agent interface {
	Config() *Config
	Status() Status
	Run(ctx context.Context, task *Task, mem MemoryContext) (*TaskResult, error)
	HealthCheck(ctx context.Context) bool
	OnShutdown(ctx context.Context)
}

// ─── Task ────────────────────────────────────────────────────────────────────

type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskQueued    TaskStatus = "queued"
	TaskRunning   TaskStatus = "running"
	TaskDone      TaskStatus = "done"
	TaskFailed    TaskStatus = "failed"
	TaskCancelled TaskStatus = "cancelled"
)

type Priority int

const (
	PriorityLow    Priority = 10
	PriorityNormal Priority = 50
	PriorityHigh   Priority = 80
	PriorityCrit   Priority = 100
)

// Task is the unit of work. All fields after mu are protected by mu when
// accessed concurrently across goroutines (e.g. Executor vs MarkFailed).
type Task struct {
	mu sync.Mutex `json:"-"`

	ID           string            `json:"id"`
	Title        string            `json:"title"`
	Description  string            `json:"description"`
	AgentRole    string            `json:"agent_role"`  // which role should handle this
	AssignedTo   string            `json:"assigned_to"` // agent ID after assignment
	Status       TaskStatus        `json:"status"`
	Priority     Priority          `json:"priority"`
	DependsOn    []string          `json:"depends_on"` // task IDs that must complete first
	Tags         []string          `json:"tags"`
	InputTokens  int64             `json:"input_tokens"`
	OutputTokens int64             `json:"output_tokens"`
	CostUSD      float64           `json:"cost_usd"`
	Retries      int               `json:"retries"`
	Meta         map[string]string `json:"meta"` // extra kv — PR number, branch, etc.
	CreatedAt    time.Time         `json:"created_at"`
	StartedAt    *time.Time        `json:"started_at,omitempty"`
	FinishedAt   *time.Time        `json:"finished_at,omitempty"`
}

// Lock/Unlock expose the embedded mutex for callers that need
// atomic multi-field updates (Executor, MarkFailed).
func (t *Task) Lock()   { t.mu.Lock() }
func (t *Task) Unlock() { t.mu.Unlock() }

// taskJSON is an alias used for JSON marshaling to avoid infinite recursion.
type taskJSON struct {
	ID           string            `json:"id"`
	Title        string            `json:"title"`
	Description  string            `json:"description"`
	AgentRole    string            `json:"agent_role"`
	AssignedTo   string            `json:"assigned_to"`
	Status       TaskStatus        `json:"status"`
	Priority     Priority          `json:"priority"`
	DependsOn    []string          `json:"depends_on"`
	Tags         []string          `json:"tags"`
	InputTokens  int64             `json:"input_tokens"`
	OutputTokens int64             `json:"output_tokens"`
	CostUSD      float64           `json:"cost_usd"`
	Retries      int               `json:"retries"`
	Meta         map[string]string `json:"meta"`
	CreatedAt    time.Time         `json:"created_at"`
	StartedAt    *time.Time        `json:"started_at,omitempty"`
	FinishedAt   *time.Time        `json:"finished_at,omitempty"`
}

// MarshalJSON serializes a Task safely under its own lock.
func (t *Task) MarshalJSON() ([]byte, error) {
	t.mu.Lock()
	snapshot := taskJSON{
		ID:           t.ID,
		Title:        t.Title,
		Description:  t.Description,
		AgentRole:    t.AgentRole,
		AssignedTo:   t.AssignedTo,
		Status:       t.Status,
		Priority:     t.Priority,
		DependsOn:    t.DependsOn,
		Tags:         t.Tags,
		InputTokens:  t.InputTokens,
		OutputTokens: t.OutputTokens,
		CostUSD:      t.CostUSD,
		Retries:      t.Retries,
		Meta:         t.Meta,
		CreatedAt:    t.CreatedAt,
		StartedAt:    t.StartedAt,
		FinishedAt:   t.FinishedAt,
	}
	t.mu.Unlock()
	return json.Marshal(snapshot)
}

type TaskResult struct {
	TaskID       string            `json:"task_id"`
	Output       string            `json:"output"`
	InputTokens  int64             `json:"input_tokens"`
	OutputTokens int64             `json:"output_tokens"`
	CostUSD      float64           `json:"cost_usd"`
	Artifacts    []Artifact        `json:"artifacts"`
	Meta         map[string]string `json:"meta"`
	DurationMs   int64             `json:"duration_ms"`
}

type ArtifactKind string

const (
	ArtifactPR      ArtifactKind = "pull_request"
	ArtifactFile    ArtifactKind = "file"
	ArtifactTrello  ArtifactKind = "trello_card"
	ArtifactNotif   ArtifactKind = "notification"
	ArtifactDiagram ArtifactKind = "diagram"
)

type Artifact struct {
	Kind    ArtifactKind      `json:"kind"`
	URL     string            `json:"url,omitempty"`
	Content string            `json:"content,omitempty"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// ─── Memory ──────────────────────────────────────────────────────────────────

// MemoryContext is injected into each agent before running a task.
// Agents read this context so they never "forget" project state.
type MemoryContext struct {
	ProjectDoc   string               // content of project.md
	RecentTasks  []*Task              // last 5 completed tasks for the same role
	RelevantCode []string             // RAG results from task history
	ADRs         []string             // Architecture Decision Records
	Resolved     *state.ResolvedStore // optional error pattern store for layer 4
}

// ─── Events ──────────────────────────────────────────────────────────────────

type EventType string

const (
	EvtAgentSpawned  EventType = "agent.spawned"
	EvtAgentKilled   EventType = "agent.killed"
	EvtAgentFailed   EventType = "agent.failed"
	EvtAgentHealthy  EventType = "agent.healthy"
	EvtTaskQueued    EventType = "task.queued"
	EvtTaskStarted   EventType = "task.started"
	EvtTaskDone      EventType = "task.done"
	EvtTaskFailed    EventType = "task.failed"
	EvtTokenLogged   EventType = "token.logged"
	EvtPRCreated     EventType = "pr.created"
	EvtPRMerged      EventType = "pr.merged"
	EvtDeployStarted EventType = "deploy.started"
	EvtDeployDone    EventType = "deploy.done"
	EvtNotifSent     EventType = "notif.sent"
)

type Event struct {
	Type      EventType   `json:"type"`
	AgentID   string      `json:"agent_id,omitempty"`
	TaskID    string      `json:"task_id,omitempty"`
	Payload   interface{} `json:"payload,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}
