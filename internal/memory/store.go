package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"log/slog"

	_ "github.com/mattn/go-sqlite3"

	"github.com/patricksign/AgentClaw/internal/adapter"
	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/state"
)

// Resolved returns the ResolvedStore for error pattern lookups and saves.
// May be nil if the store was created without a state base directory.
func (s *Store) Resolved() *state.ResolvedStore {
	return s.resolved
}

// Scope returns the ScopeStore for agent scope manifests.
// May be nil if the store was created without a state base directory.
func (s *Store) Scope() *state.ScopeStore {
	return s.scope
}

// AgentDoc returns the AgentDocStore for per-role memory files.
// May be nil if the store was created without a state base directory.
func (s *Store) AgentDoc() *state.AgentDocStore {
	return s.agentDoc
}

// AppendAgentDoc appends an outcome summary to the role memory file.
// No-ops silently if AgentDocStore was not initialised.
func (s *Store) AppendAgentDoc(role, section string) error {
	if s.agentDoc == nil {
		return nil
	}
	return s.agentDoc.Append(role, section)
}

// Scratchpad returns the shared team scratchpad. May be nil.
func (s *Store) Scratchpad() *state.Scratchpad {
	return s.scratchpad
}

// AddScratchpadEntry appends an entry to the shared scratchpad.
// No-ops silently if Scratchpad was not initialised.
func (s *Store) AddScratchpadEntry(entry state.ScratchpadEntry) error {
	if s.scratchpad == nil {
		return nil
	}
	return s.scratchpad.AddEntry(entry)
}

// ReadScratchpadContext returns the compact 24 h context string for injection
// into agent system prompts. Returns empty string if scratchpad is nil.
func (s *Store) ReadScratchpadContext() string {
	if s.scratchpad == nil {
		return ""
	}
	ctx, err := s.scratchpad.ReadForContext()
	if err != nil {
		slog.Warn("ReadScratchpadContext failed", "err", err)
		return ""
	}
	return ctx
}

// Close checkpoints the WAL and closes the database connection.
// The PRAGMA wal_checkpoint(TRUNCATE) ensures all WAL data is written back
// to the main database file before closing — prevents data loss on unclean
// restarts where the WAL file might be deleted or corrupted.
func (s *Store) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
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
			complexity    TEXT NOT NULL DEFAULT 'M',
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
	if err != nil {
		return err
	}
	if err := s.migrateCheckpointsTable(); err != nil {
		return fmt.Errorf("migrate checkpoints: %w", err)
	}
	return s.migratePreExecColumns()
}

// migratePreExecColumns adds the pre-execution protocol columns to existing databases.
// Uses ALTER TABLE ... ADD COLUMN IF NOT EXISTS pattern — no-ops on fresh DBs.
func (s *Store) migrateCheckpointsTable() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS checkpoints (
			task_id         TEXT PRIMARY KEY,
			agent_id        TEXT NOT NULL DEFAULT '',
			phase           TEXT NOT NULL DEFAULT '',
			step_index      INTEGER NOT NULL DEFAULT 0,
			step_name       TEXT NOT NULL DEFAULT '',
			accumulated     TEXT NOT NULL DEFAULT '{}',
			pending_query   TEXT NOT NULL DEFAULT '',
			pending_query_id TEXT NOT NULL DEFAULT '',
			suspended_model TEXT NOT NULL DEFAULT '',
			escalated_to    TEXT NOT NULL DEFAULT '',
			last_system     TEXT NOT NULL DEFAULT '',
			last_messages   TEXT NOT NULL DEFAULT '[]',
			last_response   TEXT NOT NULL DEFAULT '',
			input_tokens    INTEGER NOT NULL DEFAULT 0,
			output_tokens   INTEGER NOT NULL DEFAULT 0,
			cost_usd        REAL NOT NULL DEFAULT 0,
			saved_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	return err
}

func (s *Store) migratePreExecColumns() error {
	cols := []struct {
		name       string
		definition string
	}{
		{"phase", "TEXT NOT NULL DEFAULT 'understand'"},
		{"understanding", "TEXT NOT NULL DEFAULT ''"},
		{"assumptions", "TEXT NOT NULL DEFAULT '[]'"},
		{"risks", "TEXT NOT NULL DEFAULT '[]'"},
		{"questions", "TEXT NOT NULL DEFAULT '[]'"},
		{"implement_plan", "TEXT NOT NULL DEFAULT ''"},
		{"plan_approved_by", "TEXT NOT NULL DEFAULT ''"},
		{"redirect_count", "INTEGER NOT NULL DEFAULT 0"},
		{"phase_started_at", "DATETIME"},
	}
	for _, col := range cols {
		// SQLite does not support IF NOT EXISTS on ALTER TABLE ADD COLUMN,
		// so we attempt the ALTER and ignore "duplicate column" errors.
		// SAFETY: col.name and col.definition are hardcoded constants above —
		// never derived from user input. Do not refactor to accept external values.
		_, err := s.db.Exec(fmt.Sprintf(
			"ALTER TABLE tasks ADD COLUMN %s %s", col.name, col.definition,
		))
		if err != nil && !isDuplicateColumnError(err) {
			return fmt.Errorf("migratePreExecColumns: add %s: %w", col.name, err)
		}
	}
	return nil
}

// isDuplicateColumnError returns true for SQLite "duplicate column name" errors
// which arise when ALTER TABLE ADD COLUMN is called on an already-migrated DB.
func isDuplicateColumnError(err error) bool {
	return strings.Contains(err.Error(), "duplicate column name")
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

func (s *Store) SaveTask(t *adapter.Task) error {
	t.Lock()
	deps := strings.Join(t.DependsOn, ",")
	tags := strings.Join(t.Tags, ",")
	id, title, desc, role, assigned := t.ID, t.Title, t.Description, t.AgentRole, t.AssignedTo
	complexity := t.Complexity
	if complexity == "" {
		complexity = "M"
	}
	status, priority := t.Status, t.Priority
	inTok, outTok, cost, retries, createdAt := t.InputTokens, t.OutputTokens, t.CostUSD, t.Retries, t.CreatedAt

	// Pre-execution protocol fields.
	phase := string(t.Phase)
	if phase == "" {
		phase = "understand"
	}
	understanding := t.Understanding
	assumptions, _ := marshalJSONField(t.Assumptions)
	risks, _ := marshalJSONField(t.Risks)
	questions, _ := marshalJSONField(t.Questions)
	implementPlan := t.ImplementPlan
	planApprovedBy := t.PlanApprovedBy
	redirectCount := t.RedirectCount
	phaseStartedAt := t.PhaseStartedAt
	t.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// UPSERT: INSERT on first save, UPDATE on subsequent saves.
	// Faster than INSERT OR REPLACE which deletes and re-inserts the row.
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tasks
		(id,title,description,agent_role,assigned_to,complexity,status,priority,depends_on,tags,
		 input_tokens,output_tokens,cost_usd,retries,created_at,
		 phase,understanding,assumptions,risks,questions,implement_plan,plan_approved_by,redirect_count,phase_started_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
		 title=excluded.title, description=excluded.description,
		 agent_role=excluded.agent_role, assigned_to=excluded.assigned_to,
		 complexity=excluded.complexity, status=excluded.status, priority=excluded.priority,
		 depends_on=excluded.depends_on, tags=excluded.tags,
		 input_tokens=excluded.input_tokens, output_tokens=excluded.output_tokens,
		 cost_usd=excluded.cost_usd, retries=excluded.retries,
		 phase=excluded.phase, understanding=excluded.understanding,
		 assumptions=excluded.assumptions, risks=excluded.risks, questions=excluded.questions,
		 implement_plan=excluded.implement_plan, plan_approved_by=excluded.plan_approved_by,
		 redirect_count=excluded.redirect_count, phase_started_at=excluded.phase_started_at`,
		id, title, desc, role, assigned, complexity,
		status, priority, deps, tags,
		inTok, outTok, cost, retries, createdAt,
		phase, understanding, assumptions, risks, questions,
		implementPlan, planApprovedBy, redirectCount, phaseStartedAt,
	)
	return err
}

// marshalJSONField marshals a value to a JSON string for SQLite storage.
// Returns "[]" on nil slices and "{}" on nil maps.
func marshalJSONField(v any) (string, error) {
	if v == nil {
		return "[]", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "[]", err
	}
	return string(b), nil
}

func (s *Store) UpdateTaskStatus(id string, status adapter.TaskStatus) error {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	now := time.Now()
	switch status {
	case adapter.TaskRunning:
		_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=?, started_at=? WHERE id=?`, status, now, id)
		return err
	case adapter.TaskDone, adapter.TaskFailed:
		_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=?, finished_at=? WHERE id=?`, status, now, id)
		return err
	default:
		_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=? WHERE id=?`, status, id)
		return err
	}
}

func (s *Store) AddTokens(taskID string, in, out int64, cost float64) error {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET
			input_tokens  = input_tokens  + ?,
			output_tokens = output_tokens + ?,
			cost_usd      = cost_usd      + ?
		WHERE id = ?`, in, out, cost, taskID)
	return err
}

// RecentByRole returns the N most recent completed tasks for a given role.
func (s *Store) RecentByRole(role string, n int) ([]*adapter.Task, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id,title,description,agent_role,status,cost_usd,created_at
		FROM tasks WHERE agent_role=? AND status IN ('done','failed')
		ORDER BY created_at DESC LIMIT ?`, role, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*adapter.Task
	for rows.Next() {
		t := &adapter.Task{}
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
func (s *Store) SearchTasks(query string, limit int) ([]*adapter.Task, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	like := "%" + escapeLike(query) + "%"
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,title,description,agent_role,status,cost_usd,created_at
		FROM tasks WHERE (title LIKE ? ESCAPE '\' OR description LIKE ? ESCAPE '\') AND status='done'
		ORDER BY created_at DESC LIMIT ?`, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*adapter.Task
	for rows.Next() {
		t := &adapter.Task{}
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
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO adr (title, decision, created_at) VALUES (?, ?, ?)`,
		title, decision, time.Now(),
	)
	return err
}

// ListADRs returns all ADRs ordered by creation time.
func (s *Store) ListADRs() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	rows, err := s.db.QueryContext(ctx,
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

// estimateTokens returns the approximate token count for a string (1 token ≈ 4 chars).
func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}

// truncateToTokens caps s at approximately maxTokens (1 token ≈ 4 chars).
// If truncation occurs, "... [truncated]" is appended.
func truncateToTokens(s string, maxTokens int) string {
	maxChars := maxTokens * 4
	if len(s) <= maxChars {
		return s
	}
	return s[:maxChars] + "... [truncated]"
}

// maxContextTokens defines the total token budget per complexity tier.
// These budgets prevent context from exceeding the model's effective window
// and keep costs proportional to task importance.
var maxContextTokens = map[string]int{
	"S": 2000,  // ~8 KB — minimal context for cheap fast tasks
	"M": 6000,  // ~24 KB — standard context for mid-tier tasks
	"L": 12000, // ~48 KB — full context for complex tasks
}

// enforceTokenBudget checks the total token usage of all string fields in the
// context and proportionally trims the largest sections if the budget is exceeded.
// Fields are trimmed in priority order (lowest priority trimmed first):
// ADRs → RelevantCode → ProjectDoc → AgentDoc (never trimmed below 200 tokens).
func enforceTokenBudget(ctx *adapter.MemoryContext, budget int) {
	type section struct {
		ptr      *string
		name     string
		priority int // higher = more important = trimmed last
	}

	sections := []section{
		{ptr: &ctx.ProjectDoc, name: "ProjectDoc", priority: 2},
		{ptr: &ctx.AgentDoc, name: "AgentDoc", priority: 4},
	}

	// Calculate current total from string fields.
	total := estimateTokens(ctx.ProjectDoc) + estimateTokens(ctx.AgentDoc)
	for _, c := range ctx.RelevantCode {
		total += estimateTokens(c)
	}
	for _, a := range ctx.ADRs {
		total += estimateTokens(a)
	}

	if total <= budget {
		return // within budget
	}

	excess := total - budget

	// Phase 1: Trim ADRs (lowest priority).
	for i := len(ctx.ADRs) - 1; i >= 0 && excess > 0; i-- {
		tokens := estimateTokens(ctx.ADRs[i])
		ctx.ADRs = ctx.ADRs[:i]
		excess -= tokens
	}

	// Phase 2: Trim RelevantCode from the end.
	for i := len(ctx.RelevantCode) - 1; i >= 0 && excess > 0; i-- {
		tokens := estimateTokens(ctx.RelevantCode[i])
		ctx.RelevantCode = ctx.RelevantCode[:i]
		excess -= tokens
	}

	// Phase 3: Proportionally trim string sections (lowest priority first).
	// Sort by priority ascending so we trim least important first.
	sort.Slice(sections, func(i, j int) bool {
		return sections[i].priority < sections[j].priority
	})

	for _, s := range sections {
		if excess <= 0 {
			break
		}
		current := estimateTokens(*s.ptr)
		minTokens := 200 // never trim below 200 tokens
		if current <= minTokens {
			continue
		}
		canTrim := current - minTokens
		trimAmount := canTrim
		if trimAmount > excess {
			trimAmount = excess
		}
		newCap := current - trimAmount
		*s.ptr = truncateToTokens(*s.ptr, newCap)
		excess -= trimAmount
	}

	if excess > 0 {
		slog.Warn("BuildContext: could not fit within token budget after trimming", "excess_tokens", excess, "budget", budget)
	}
}

// ─── Checkpoint Store (SQLite) ────────────────────────────────────────────────

// SaveCheckpoint persists a phase checkpoint, replacing any existing one for the task.
func (s *Store) SaveCheckpoint(cp *domain.PhaseCheckpoint) error {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	accum, err := json.Marshal(cp.Accumulated)
	if err != nil {
		return fmt.Errorf("save checkpoint %s: marshal accumulated: %w", cp.TaskID, err)
	}
	msgs, mErr := json.Marshal(cp.LastMessages)
	if mErr != nil {
		return fmt.Errorf("save checkpoint %s: marshal messages: %w", cp.TaskID, mErr)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO checkpoints
		(task_id,agent_id,phase,step_index,step_name,accumulated,
		 pending_query,pending_query_id,suspended_model,escalated_to,
		 last_system,last_messages,last_response,
		 input_tokens,output_tokens,cost_usd,saved_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		cp.TaskID, cp.AgentID, string(cp.Phase), cp.StepIndex, cp.StepName, string(accum),
		cp.PendingQuery, cp.PendingQueryID, cp.SuspendedModel, cp.EscalatedTo,
		cp.LastSystemPrompt, string(msgs), cp.LastResponse,
		cp.InputTokens, cp.OutputTokens, cp.CostUSD, time.Now(),
	)
	return err
}

// LoadCheckpoint retrieves a checkpoint for a task. Returns nil, nil if none exists.
func (s *Store) LoadCheckpoint(taskID string) (*domain.PhaseCheckpoint, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	var cp domain.PhaseCheckpoint
	var phase, accum, msgs string

	err := s.db.QueryRowContext(ctx, `
		SELECT task_id,agent_id,phase,step_index,step_name,accumulated,
		       pending_query,pending_query_id,suspended_model,escalated_to,
		       last_system,last_messages,last_response,
		       input_tokens,output_tokens,cost_usd,saved_at
		FROM checkpoints WHERE task_id=?`, taskID).Scan(
		&cp.TaskID, &cp.AgentID, &phase, &cp.StepIndex, &cp.StepName, &accum,
		&cp.PendingQuery, &cp.PendingQueryID, &cp.SuspendedModel, &cp.EscalatedTo,
		&cp.LastSystemPrompt, &msgs, &cp.LastResponse,
		&cp.InputTokens, &cp.OutputTokens, &cp.CostUSD, &cp.SavedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load checkpoint %s: %w", taskID, err)
	}

	cp.Phase = domain.ExecutionPhase(phase)
	if accum != "" {
		if err := json.Unmarshal([]byte(accum), &cp.Accumulated); err != nil {
			return nil, fmt.Errorf("load checkpoint %s: unmarshal accumulated: %w", taskID, err)
		}
	}
	if msgs != "" {
		if err := json.Unmarshal([]byte(msgs), &cp.LastMessages); err != nil {
			return nil, fmt.Errorf("load checkpoint %s: unmarshal last_messages: %w", taskID, err)
		}
	}

	return &cp, nil
}

// DeleteCheckpoint removes the checkpoint for a task.
func (s *Store) DeleteCheckpoint(taskID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `DELETE FROM checkpoints WHERE task_id=?`, taskID)
	return err
}

func (s *Store) LogTokens(l TokenLog) error {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	_, err := s.db.ExecContext(ctx, `
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
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
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

func (s *Store) StatsForPeriod(dateExpr string) (*PeriodStats, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	st := &PeriodStats{Period: dateExpr}
	err := s.db.QueryRowContext(ctx, `
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
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	st := &PeriodStats{Period: from + " to " + to}
	err := s.db.QueryRowContext(ctx, `
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

// taskSelectCols is the common SELECT column list used by ListTasks and GetTask.
const taskSelectCols = `id,title,description,agent_role,assigned_to,complexity,status,priority,
       depends_on,tags,input_tokens,output_tokens,cost_usd,retries,
       created_at,started_at,finished_at,
       COALESCE(phase,'understand'),COALESCE(understanding,''),
       COALESCE(assumptions,'[]'),COALESCE(risks,'[]'),COALESCE(questions,'[]'),
       COALESCE(implement_plan,''),COALESCE(plan_approved_by,''),
       COALESCE(redirect_count,0),phase_started_at`

// ListTasks returns all tasks ordered by created_at DESC.
func (s *Store) ListTasks() ([]*adapter.Task, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `SELECT `+taskSelectCols+` FROM tasks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*adapter.Task
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
func (s *Store) GetTask(id string) (*adapter.Task, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	row := s.db.QueryRowContext(ctx, `SELECT `+taskSelectCols+` FROM tasks WHERE id=?`, id)
	return scanTask(row)
}

// ListTasksByPhase returns all tasks in a given execution phase that have the given status.
func (s *Store) ListTasksByPhase(phase adapter.ExecutionPhase, status adapter.TaskStatus) ([]*adapter.Task, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskSelectCols+` FROM tasks WHERE COALESCE(phase,'understand')=? AND status=?`,
		string(phase), string(status),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*adapter.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

type scannable interface {
	Scan(...any) error
}

func scanTask(row scannable) (*adapter.Task, error) {
	t := &adapter.Task{}
	var deps, tags string
	var startedAt, finishedAt, phaseStartedAt sql.NullTime
	var phase, understanding, assumptions, risks, questions string
	var implementPlan, planApprovedBy string
	var redirectCount int

	err := row.Scan(
		&t.ID, &t.Title, &t.Description, &t.AgentRole, &t.AssignedTo, &t.Complexity,
		&t.Status, &t.Priority, &deps, &tags,
		&t.InputTokens, &t.OutputTokens, &t.CostUSD, &t.Retries,
		&t.CreatedAt, &startedAt, &finishedAt,
		&phase, &understanding, &assumptions, &risks, &questions,
		&implementPlan, &planApprovedBy, &redirectCount, &phaseStartedAt,
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

	// Pre-execution protocol fields.
	t.Phase = adapter.ExecutionPhase(phase)
	t.Understanding = understanding
	t.ImplementPlan = implementPlan
	t.PlanApprovedBy = planApprovedBy
	t.RedirectCount = redirectCount
	if phaseStartedAt.Valid {
		t.PhaseStartedAt = phaseStartedAt.Time
	}

	// Unmarshal JSON-encoded slices. Errors are propagated (E2) — corrupted JSON
	// in the DB would otherwise silently skip the clarification phase.
	if assumptions != "" && assumptions != "[]" {
		if err := json.Unmarshal([]byte(assumptions), &t.Assumptions); err != nil {
			return nil, fmt.Errorf("scanTask: unmarshal assumptions: %w", err)
		}
	}
	if risks != "" && risks != "[]" {
		if err := json.Unmarshal([]byte(risks), &t.Risks); err != nil {
			return nil, fmt.Errorf("scanTask: unmarshal risks: %w", err)
		}
	}
	if questions != "" && questions != "[]" {
		if err := json.Unmarshal([]byte(questions), &t.Questions); err != nil {
			return nil, fmt.Errorf("scanTask: unmarshal questions: %w", err)
		}
	}

	return t, nil
}
