package memory

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/patricksign/agentclaw/internal/agent"
	"github.com/patricksign/agentclaw/internal/state"
)

// Store manages the 3-layer memory architecture.
type Store struct {
	db          *sql.DB
	projectPath string // path to project.md
	resolved    *state.ResolvedStore
}

// Resolved returns the ResolvedStore for error pattern lookups and saves.
// May be nil if the store was created without a state base directory.
func (s *Store) Resolved() *state.ResolvedStore {
	return s.resolved
}

func New(dbPath, projectPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal=WAL")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db, projectPath: projectPath}
	return s, s.migrate()
}

// NewWithState creates a Store and attaches a ResolvedStore rooted at stateBaseDir.
// If stateBaseDir is empty, the ResolvedStore is not initialised (Resolved() returns nil).
func NewWithState(dbPath, projectPath, stateBaseDir string) (*Store, error) {
	s, err := New(dbPath, projectPath)
	if err != nil {
		return nil, err
	}
	if stateBaseDir != "" {
		rs, rerr := state.NewResolvedStore(stateBaseDir)
		if rerr != nil {
			return nil, fmt.Errorf("memory: init resolved store: %w", rerr)
		}
		s.resolved = rs
	}
	return s, nil
}

// Close flushes the WAL and closes the database connection.
// Must be called on graceful shutdown to avoid data loss.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS tasks (
			id            TEXT PRIMARY KEY,
			title         TEXT NOT NULL,
			description   TEXT,
			agent_role    TEXT,
			assigned_to   TEXT,
			status        TEXT NOT NULL DEFAULT 'pending',
			priority      INTEGER NOT NULL DEFAULT 50,
			depends_on    TEXT NOT NULL DEFAULT '',
			tags          TEXT NOT NULL DEFAULT '',
			input_tokens  INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cost_usd      REAL NOT NULL DEFAULT 0,
			retries       INTEGER NOT NULL DEFAULT 0,
			meta          TEXT NOT NULL DEFAULT '{}',
			created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			started_at    DATETIME,
			finished_at   DATETIME
		);

		CREATE TABLE IF NOT EXISTS token_logs (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id       TEXT NOT NULL,
			agent_id      TEXT NOT NULL,
			model         TEXT NOT NULL,
			input_tokens  INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cost_usd      REAL NOT NULL DEFAULT 0,
			duration_ms   INTEGER NOT NULL DEFAULT 0,
			created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS adr (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			title      TEXT NOT NULL,
			decision   TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);

		CREATE INDEX IF NOT EXISTS idx_tasks_role    ON tasks(agent_role);
		CREATE INDEX IF NOT EXISTS idx_tasks_status  ON tasks(status);
		CREATE INDEX IF NOT EXISTS idx_logs_task     ON token_logs(task_id);
		CREATE INDEX IF NOT EXISTS idx_logs_created  ON token_logs(created_at);
	`)
	return err
}

// ─── LAYER 1: Project Memory ─────────────────────────────────────────────────

// ReadProjectDoc reads project.md — all agents call this before starting work.
func (s *Store) ReadProjectDoc() string {
	data, err := os.ReadFile(s.projectPath)
	if err != nil {
		return "# Project\n\nNo project doc found. Please create project.md"
	}
	return string(data)
}

// AppendProjectDoc appends a new section to project.md.
// Uses 0600 permissions (owner read/write only) to protect potentially
// sensitive architectural decisions from other system users.
func (s *Store) AppendProjectDoc(section string) error {
	f, err := os.OpenFile(s.projectPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n\n---\n*Updated: %s*\n%s", time.Now().Format(time.RFC3339), section)
	return err
}

// ─── LAYER 2: Task History ────────────────────────────────────────────────────

func (s *Store) SaveTask(t *agent.Task) error {
	t.Lock()
	deps := strings.Join(t.DependsOn, ",")
	tags := strings.Join(t.Tags, ",")
	id, title, desc, role, assigned := t.ID, t.Title, t.Description, t.AgentRole, t.AssignedTo
	status, priority := t.Status, t.Priority
	inTok, outTok, cost, retries, createdAt := t.InputTokens, t.OutputTokens, t.CostUSD, t.Retries, t.CreatedAt
	t.Unlock()

	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO tasks
		(id,title,description,agent_role,assigned_to,status,priority,depends_on,tags,input_tokens,output_tokens,cost_usd,retries,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, title, desc, role, assigned,
		status, priority, deps, tags,
		inTok, outTok, cost, retries, createdAt,
	)
	return err
}

func (s *Store) UpdateTaskStatus(id string, status agent.TaskStatus) error {
	now := time.Now()
	switch status {
	case agent.TaskRunning:
		_, err := s.db.Exec(`UPDATE tasks SET status=?, started_at=? WHERE id=?`, status, now, id)
		return err
	case agent.TaskDone, agent.TaskFailed:
		_, err := s.db.Exec(`UPDATE tasks SET status=?, finished_at=? WHERE id=?`, status, now, id)
		return err
	default:
		_, err := s.db.Exec(`UPDATE tasks SET status=? WHERE id=?`, status, id)
		return err
	}
}

func (s *Store) AddTokens(taskID string, in, out int64, cost float64) error {
	_, err := s.db.Exec(`
		UPDATE tasks SET
			input_tokens  = input_tokens  + ?,
			output_tokens = output_tokens + ?,
			cost_usd      = cost_usd      + ?
		WHERE id = ?`, in, out, cost, taskID)
	return err
}

// RecentByRole returns the N most recent completed tasks for a given role.
func (s *Store) RecentByRole(role string, n int) ([]*agent.Task, error) {
	rows, err := s.db.Query(`
		SELECT id,title,description,agent_role,status,cost_usd,created_at
		FROM tasks WHERE agent_role=? AND status IN ('done','failed')
		ORDER BY created_at DESC LIMIT ?`, role, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*agent.Task
	for rows.Next() {
		t := &agent.Task{}
		if err := rows.Scan(&t.ID, &t.Title, &t.Description, &t.AgentRole, &t.Status, &t.CostUSD, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("RecentByRole scan: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// ─── LAYER 3: Simple RAG (keyword search) ────────────────────────────────────

// escapeLike escapes LIKE special characters (%, _, \) in a user-controlled
// string so they are treated as literals in a LIKE pattern.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// SearchTasks finds completed tasks matching the query in title or description.
func (s *Store) SearchTasks(query string, limit int) ([]*agent.Task, error) {
	like := "%" + escapeLike(query) + "%"
	rows, err := s.db.Query(`
		SELECT id,title,description,agent_role,status,cost_usd,created_at
		FROM tasks WHERE (title LIKE ? ESCAPE '\' OR description LIKE ? ESCAPE '\') AND status='done'
		ORDER BY created_at DESC LIMIT ?`, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*agent.Task
	for rows.Next() {
		t := &agent.Task{}
		if err := rows.Scan(&t.ID, &t.Title, &t.Description, &t.AgentRole, &t.Status, &t.CostUSD, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("SearchTasks scan: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// ─── ADR (Architecture Decision Records) ─────────────────────────────────────

// SaveADR persists an architecture decision record.
func (s *Store) SaveADR(title, decision string) error {
	_, err := s.db.Exec(
		`INSERT INTO adr (title, decision, created_at) VALUES (?, ?, ?)`,
		title, decision, time.Now(),
	)
	return err
}

// ListADRs returns all ADRs ordered by creation time.
func (s *Store) ListADRs() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT title, decision FROM adr ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var adrs []string
	for rows.Next() {
		var title, decision string
		if err := rows.Scan(&title, &decision); err != nil {
			return nil, fmt.Errorf("ListADRs scan: %w", err)
		}
		adrs = append(adrs, fmt.Sprintf("## %s\n%s", title, decision))
	}
	return adrs, rows.Err()
}

// ─── Build MemoryContext for agents ──────────────────────────────────────────

// BuildContext assembles a full MemoryContext to inject into an agent before
// running a task. It reads all three memory layers and attaches the
// ResolvedStore for layer 4 (known error patterns).
func (s *Store) BuildContext(role, taskTitle string) agent.MemoryContext {
	ctx := agent.MemoryContext{}

	// Layer 1: project doc
	ctx.ProjectDoc = s.ReadProjectDoc()

	// Layer 2: recent tasks for the same role
	recent, err := s.RecentByRole(role, 5)
	if err == nil {
		ctx.RecentTasks = recent
	}

	// Layer 3: RAG search from task title
	related, err := s.SearchTasks(taskTitle, 3)
	if err == nil {
		for _, t := range related {
			ctx.RelevantCode = append(ctx.RelevantCode,
				fmt.Sprintf("[%s] %s: %s", t.Status, t.Title, t.Description)) //nolint:govet // t is *Task, no lock copied
		}
	}

	// ADRs
	adrs, err := s.ListADRs()
	if err == nil {
		ctx.ADRs = adrs
	}

	// Layer 4: resolved error pattern store (may be nil — agents handle nil safely)
	ctx.Resolved = s.resolved

	return ctx
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

func (s *Store) LogTokens(l TokenLog) error {
	_, err := s.db.Exec(`
		INSERT INTO token_logs (task_id,agent_id,model,input_tokens,output_tokens,cost_usd,duration_ms,created_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		l.TaskID, l.AgentID, l.Model, l.InputTokens, l.OutputTokens,
		l.CostUSD, l.DurationMs, l.CreatedAt,
	)
	return err
}

func (s *Store) LogTokenUsage(taskID, agentID, model string, in, out int64, cost float64, durationMs int64) error {
	return s.LogTokens(TokenLog{
		TaskID:       taskID,
		AgentID:      agentID,
		Model:        model,
		InputTokens:  in,
		OutputTokens: out,
		CostUSD:      cost,
		DurationMs:   durationMs,
		CreatedAt:    time.Now(),
	})
}

// GetTokenLogs returns all token logs for a task ordered by creation time.
func (s *Store) GetTokenLogs(taskID string) ([]TokenLog, error) {
	rows, err := s.db.Query(`
		SELECT task_id,agent_id,model,input_tokens,output_tokens,cost_usd,duration_ms,created_at
		FROM token_logs WHERE task_id=? ORDER BY created_at ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []TokenLog
	for rows.Next() {
		var l TokenLog
		if err := rows.Scan(&l.TaskID, &l.AgentID, &l.Model,
			&l.InputTokens, &l.OutputTokens, &l.CostUSD,
			&l.DurationMs, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("GetTokenLogs scan: %w", err)
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

// ─── Metrics ─────────────────────────────────────────────────────────────────

type PeriodStats struct {
	Period       string  `json:"period"`
	TotalTasks   int     `json:"total_tasks"`
	DoneTasks    int     `json:"done_tasks"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCostUSD float64 `json:"total_cost_usd"`
}

func (s *Store) StatsForPeriod(dateExpr string) (*PeriodStats, error) {
	st := &PeriodStats{Period: dateExpr}
	err := s.db.QueryRow(`
		SELECT COUNT(*),
		       SUM(CASE WHEN status='done' THEN 1 ELSE 0 END),
		       COALESCE(SUM(input_tokens+output_tokens),0),
		       COALESCE(SUM(cost_usd),0)
		FROM tasks WHERE DATE(created_at)=?`, dateExpr,
	).Scan(&st.TotalTasks, &st.DoneTasks, &st.TotalTokens, &st.TotalCostUSD)
	if err != nil {
		return nil, fmt.Errorf("StatsForPeriod: %w", err)
	}
	return st, nil
}

func (s *Store) StatsForRange(from, to string) (*PeriodStats, error) {
	st := &PeriodStats{Period: from + " to " + to}
	err := s.db.QueryRow(`
		SELECT COUNT(*),
		       SUM(CASE WHEN status='done' THEN 1 ELSE 0 END),
		       COALESCE(SUM(input_tokens+output_tokens),0),
		       COALESCE(SUM(cost_usd),0)
		FROM tasks WHERE DATE(created_at) BETWEEN ? AND ?`, from, to,
	).Scan(&st.TotalTasks, &st.DoneTasks, &st.TotalTokens, &st.TotalCostUSD)
	if err != nil {
		return nil, fmt.Errorf("StatsForRange: %w", err)
	}
	return st, nil
}

// ─── Task queries ─────────────────────────────────────────────────────────────

// ListTasks returns all tasks ordered by created_at DESC.
func (s *Store) ListTasks() ([]*agent.Task, error) {
	rows, err := s.db.Query(`
		SELECT id,title,description,agent_role,assigned_to,status,priority,
		       depends_on,tags,input_tokens,output_tokens,cost_usd,retries,
		       created_at,started_at,finished_at
		FROM tasks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*agent.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// GetTask returns a single task by ID.
func (s *Store) GetTask(id string) (*agent.Task, error) {
	row := s.db.QueryRow(`
		SELECT id,title,description,agent_role,assigned_to,status,priority,
		       depends_on,tags,input_tokens,output_tokens,cost_usd,retries,
		       created_at,started_at,finished_at
		FROM tasks WHERE id=?`, id)
	return scanTask(row)
}

type scannable interface {
	Scan(...any) error
}

func scanTask(row scannable) (*agent.Task, error) {
	t := &agent.Task{}
	var deps, tags string
	var startedAt, finishedAt sql.NullTime
	err := row.Scan(
		&t.ID, &t.Title, &t.Description, &t.AgentRole, &t.AssignedTo,
		&t.Status, &t.Priority, &deps, &tags,
		&t.InputTokens, &t.OutputTokens, &t.CostUSD, &t.Retries,
		&t.CreatedAt, &startedAt, &finishedAt,
	)
	if err != nil {
		return nil, err
	}
	if deps != "" {
		t.DependsOn = strings.Split(deps, ",")
	}
	if tags != "" {
		t.Tags = strings.Split(tags, ",")
	}
	if startedAt.Valid {
		t.StartedAt = &startedAt.Time
	}
	if finishedAt.Valid {
		t.FinishedAt = &finishedAt.Time
	}
	return t, nil
}
