package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog/log"

	"github.com/patricksign/agentclaw/internal/agent"
	"github.com/patricksign/agentclaw/internal/state"
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
		log.Warn().Err(err).Msg("ReadScratchpadContext failed")
		return ""
	}
	return ctx
}

func New(dbPath, projectPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal=WAL&_busy_timeout=5000&_synchronous=NORMAL")
	if err != nil {
		return nil, err
	}
	// WAL mode supports concurrent readers with a single writer.
	// With 9 role workers + summarizer + API handlers, we need enough
	// read connections to avoid blocking. SQLite serialises writes
	// internally via _busy_timeout.
	//
	// _synchronous=NORMAL is safe with WAL — data is durable against
	// application crashes (only OS crash can lose last txn, acceptable
	// for task metadata).
	// 9 role workers + cron + API handlers + pipeline goroutines need concurrent reads.
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(30 * time.Minute) // recycle connections to avoid stale state

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

		ss, serr := state.NewScopeStore(stateBaseDir)
		if serr != nil {
			return nil, fmt.Errorf("memory: init scope store: %w", serr)
		}
		s.scope = ss

		// Derive memoryBaseDir from stateBaseDir (sibling directory).
		memoryBaseDir := filepath.Join(filepath.Dir(stateBaseDir), "memory")
		ads, aerr := state.NewAgentDocStore(memoryBaseDir)
		if aerr != nil {
			return nil, fmt.Errorf("memory: init agent doc store: %w", aerr)
		}
		s.agentDoc = ads

		sp, serr2 := state.NewScratchpad(stateBaseDir)
		if serr2 != nil {
			return nil, fmt.Errorf("memory: init scratchpad: %w", serr2)
		}
		s.scratchpad = sp
	}
	return s, nil
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
	return s.migratePreExecColumns()
}

// migratePreExecColumns adds the pre-execution protocol columns to existing databases.
// Uses ALTER TABLE ... ADD COLUMN IF NOT EXISTS pattern — no-ops on fresh DBs.
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

func (s *Store) SaveTask(t *agent.Task) error {
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

func (s *Store) UpdateTaskStatus(id string, status agent.TaskStatus) error {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	now := time.Now()
	switch status {
	case agent.TaskRunning:
		_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=?, started_at=? WHERE id=?`, status, now, id)
		return err
	case agent.TaskDone, agent.TaskFailed:
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
func (s *Store) RecentByRole(role string, n int) ([]*agent.Task, error) {
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

// truncateToTokens caps s at approximately maxTokens (1 token ≈ 4 chars).
// If truncation occurs, "... [truncated]" is appended.
func truncateToTokens(s string, maxTokens int) string {
	maxChars := maxTokens * 4
	if len(s) <= maxChars {
		return s
	}
	return s[:maxChars] + "... [truncated]"
}

// BuildContext assembles a tiered MemoryContext based on task complexity.
//
// Complexity "S" — Tier 1 only (~500 tokens): AgentDoc, ScopeManifest, Scratchpad.
// Complexity "M" — Tier 1 + Tier 2 (~1500 tokens total): adds RecentByRole(3),
//
//	ResolvedStore top-3 matches, first 800 chars of project.md.
//
// Complexity "L" — all tiers (~3000 tokens total): RecentByRole(5), full
//
//	project.md, ScopeStore.ReadAll() for cross-agent awareness.
//
// If complexity is empty it defaults to "M".
func (s *Store) BuildContext(agentID, role, taskTitle, complexity string) agent.MemoryContext {
	if complexity == "" {
		complexity = "M"
	}
	ctx := agent.MemoryContext{}

	// ── Tier 1 — always loaded ────────────────────────────────────────────

	// AgentDoc: role-specific conventions and pitfalls (cap 800 tokens).
	if s.agentDoc != nil {
		if doc, derr := s.agentDoc.Read(role); derr == nil {
			ctx.AgentDoc = truncateToTokens(doc, 800)
		} else {
			log.Warn().Err(derr).Str("role", role).Msg("BuildContext: AgentDoc.Read failed")
		}
	}

	// ScopeManifest: what this agent owns / must not touch.
	if s.scope != nil {
		if m, serr := s.scope.Read(role); serr != nil {
			log.Warn().Err(serr).Str("role", role).Msg("BuildContext: scope.Read failed")
		} else {
			ctx.Scope = m
		}
	}

	// Scratchpad: compact last-24 h team status (cap 400 tokens).
	ctx.Scratchpad = s.scratchpad

	// ── Tier 2 — M or L ──────────────────────────────────────────────────

	// Read project doc once; tier determines the token cap applied below.
	rawProjectDoc := s.ReadProjectDoc()

	if complexity == "M" || complexity == "L" {
		// RecentByRole: fetch 5 for L, 3 for M — single query avoids redundant DB call.
		recentLimit := 3
		if complexity == "L" {
			recentLimit = 5
		}
		recent, err := s.RecentByRole(role, recentLimit)
		if err != nil {
			log.Warn().Err(err).Str("role", role).Msg("BuildContext: RecentByRole failed")
		} else {
			for _, t := range recent {
				t.Lock()
				title, desc := t.Title, t.Description
				status := t.Status
				t.Unlock()
				entry := truncateToTokens(fmt.Sprintf("[%s] %s: %s", status, title, desc), 300)
				ctx.RelevantCode = append(ctx.RelevantCode, entry)
			}
			ctx.RecentTasks = recent
		}

		// ResolvedStore: top-3 matching error patterns (cap 200 tokens each).
		if s.resolved != nil {
			if matches, serr := s.resolved.Search(taskTitle, role); serr == nil {
				top := matches
				if len(top) > 3 {
					top = top[:3]
				}
				for _, m := range top {
					snippet := truncateToTokens(
						fmt.Sprintf("**%s** (seen %d×)\nFix: %s", m.ErrorPattern, m.OccurrenceCount, m.ResolutionSummary),
						200,
					)
					ctx.RelevantCode = append(ctx.RelevantCode, snippet)
				}
			}
		}

		// Project doc: first 800 tokens only (M), full 2000 tokens (L).
		if complexity == "L" {
			ctx.ProjectDoc = truncateToTokens(rawProjectDoc, 2000)
		} else {
			ctx.ProjectDoc = truncateToTokens(rawProjectDoc, 800)
		}
	}

	// ── Tier 3 — L only ──────────────────────────────────────────────────

	if complexity == "L" {

		// Cross-agent awareness via ScopeStore.ReadAll().
		if s.scope != nil {
			if all, aerr := s.scope.ReadAll(); aerr != nil {
				log.Warn().Err(aerr).Msg("BuildContext: ScopeStore.ReadAll failed")
			} else {
				for i := range all {
					ctx.AllScopes = append(ctx.AllScopes, &all[i])
				}
			}
		}

		// ADRs are only loaded at tier 3 to keep lower tiers lean.
		adrs, err := s.ListADRs()
		if err != nil {
			log.Warn().Err(err).Msg("BuildContext: ListADRs failed")
		} else {
			ctx.ADRs = adrs
		}
	}

	// ResolvedStore reference — agents use it directly for runtime lookups.
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

// ─── Metrics ─────────────────────────────────────────────────────────────────

type PeriodStats struct {
	Period       string  `json:"period"`
	TotalTasks   int     `json:"total_tasks"`
	DoneTasks    int     `json:"done_tasks"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCostUSD float64 `json:"total_cost_usd"`
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
func (s *Store) ListTasks() ([]*agent.Task, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `SELECT `+taskSelectCols+` FROM tasks ORDER BY created_at DESC`)
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
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	row := s.db.QueryRowContext(ctx, `SELECT `+taskSelectCols+` FROM tasks WHERE id=?`, id)
	return scanTask(row)
}

// ListTasksByPhase returns all tasks in a given execution phase that have the given status.
func (s *Store) ListTasksByPhase(phase agent.ExecutionPhase, status agent.TaskStatus) ([]*agent.Task, error) {
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

type scannable interface {
	Scan(...any) error
}

func scanTask(row scannable) (*agent.Task, error) {
	t := &agent.Task{}
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
	t.Phase = agent.ExecutionPhase(phase)
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
