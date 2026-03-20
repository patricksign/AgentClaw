package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/patricksign/agentclaw/internal/state"
	"github.com/rs/zerolog/log"
)

// MemoryStore interface — avoids circular import with the memory package.
// BuildContext returns a MemoryContext with []*Task to avoid copying mutex values.
type MemoryStore interface {
	BuildContext(agentID, role, taskTitle, complexity string) MemoryContext
	SaveTask(t *Task) error
	GetTask(id string) (*Task, error)
	UpdateTaskStatus(id string, status TaskStatus) error
	AddTokens(taskID string, in, out int64, cost float64) error
	LogTokenUsage(taskID, agentID, model string, in, out int64, cost float64, durationMs int64) error
	// Resolved returns the ResolvedStore for error-pattern lookups. May be nil.
	Resolved() *state.ResolvedStore
	// AppendAgentDoc appends an outcome summary to the role memory file.
	// Implementations that do not support this may return nil silently.
	AppendAgentDoc(role, section string) error
	// AddScratchpadEntry appends an entry to the shared team scratchpad.
	AddScratchpadEntry(entry state.ScratchpadEntry) error
	// ListTasksByPhase returns all tasks in a given phase with the given status.
	ListTasksByPhase(phase ExecutionPhase, status TaskStatus) ([]*Task, error)
}

// PostRunHook is called after a task completes successfully. It receives the
// executing agent, the task, and the result. Errors are logged but do not
// fail the task — hooks are best-effort side effects (e.g. Trello card
// creation, Slack notifications).
type PostRunHook func(ctx context.Context, a Agent, task *Task, result *TaskResult)

// Executor wires Pool + Queue + Memory + EventBus together for task execution.
type Executor struct {
	pool       *Pool
	bus        *EventBus
	mem        MemoryStore
	hooks      []PostRunHook
	replyStore *ReplyStore       // optional — enables task resume after human answer
	queue      taskQueuer        // optional — enables RecoverSuspendedTasks re-dispatch
	skillStore *state.SkillStore // optional — enables agent self-improvement via skills
}

// taskQueuer is the minimal queue interface needed by RecoverSuspendedTasks.
type taskQueuer interface {
	Push(task *Task)
}

func NewExecutor(pool *Pool, bus *EventBus, mem MemoryStore) *Executor {
	return &Executor{pool: pool, bus: bus, mem: mem}
}

// SetReplyStore injects the ReplyStore so ResumeTask can route answers.
func (e *Executor) SetReplyStore(rs *ReplyStore) {
	e.replyStore = rs
}

// SetQueue injects the task queue so RecoverSuspendedTasks can re-dispatch tasks.
func (e *Executor) SetQueue(q taskQueuer) {
	e.queue = q
}

// SetSkillStore injects the SkillStore for agent self-improvement.
func (e *Executor) SetSkillStore(ss *state.SkillStore) {
	e.skillStore = ss
}

// AddPostRunHook registers a hook that runs after every successful task.
func (e *Executor) AddPostRunHook(h PostRunHook) {
	e.hooks = append(e.hooks, h)
}

// ResumeTask re-dispatches a task that is suspended in PhaseClarify.
// It is called by the /api/tasks/{id}/answer handler after an answer is recorded.
func (e *Executor) ResumeTask(ctx context.Context, taskID string) error {
	task, err := e.mem.GetTask(taskID)
	if err != nil {
		return fmt.Errorf("ResumeTask: load task %s: %w", taskID, err)
	}
	task.Lock()
	phase := task.Phase
	task.Unlock()

	if phase != PhaseClarify {
		return fmt.Errorf("ResumeTask: task %s is in phase %s, not clarify", taskID, phase)
	}
	if e.queue == nil {
		return fmt.Errorf("ResumeTask: queue not configured")
	}
	e.queue.Push(task)
	log.Info().Str("task", taskID).Msg("ResumeTask: task re-queued after answer")
	return nil
}

// RecoverSuspendedTasks finds all tasks stuck in PhaseClarify (e.g. after a server restart)
// and re-dispatches them to the queue so they can resume waiting for human answers.
// Call this once at startup after all components are initialised, before starting workers.
func (e *Executor) RecoverSuspendedTasks(ctx context.Context, tg suspendedNotifier) error {
	if e.queue == nil {
		return nil
	}
	tasks, err := e.mem.ListTasksByPhase(PhaseClarify, TaskRunning)
	if err != nil {
		return fmt.Errorf("RecoverSuspendedTasks: query: %w", err)
	}

	for _, task := range tasks {
		task.Lock()
		taskID := task.ID
		taskTitle := task.Title
		phaseStartedAt := task.PhaseStartedAt
		// Deep copy the questions slice (C4) — the backing array must not be
		// shared with phaseClarify which may iterate it concurrently after re-queue.
		questions := make([]Question, len(task.Questions))
		copy(questions, task.Questions)
		task.Unlock()

		// Re-send AskHuman for each unresolved question (server may have restarted
		// before the Telegram message was sent or the channel was registered).
		if tg != nil && e.replyStore != nil {
			for _, q := range questions {
				if q.Resolved {
					continue
				}
				if !e.replyStore.HasPending(taskID) {
					msgID, askErr := tg.NotifyResumeAfterRestart(ctx, taskID, taskTitle, q.Text, phaseStartedAt.Format(time.RFC3339))
					if askErr != nil {
						log.Warn().Err(askErr).Str("task", taskID).Msg("RecoverSuspendedTasks: re-notify failed")
						continue
					}
					// Re-register so the reply can be routed.
					_ = e.replyStore.Register(msgID, taskID, q.ID)
				}
			}
		}

		e.queue.Push(task)
		log.Info().Str("task", taskID).Msg("RecoverSuspendedTasks: re-queued suspended task")
	}
	return nil
}

// suspendedNotifier is satisfied by telegram.DualChannelClient — used in RecoverSuspendedTasks.
type suspendedNotifier interface {
	NotifyResumeAfterRestart(ctx context.Context, taskID, taskTitle, questionText, phaseStartedAt string) (int, error)
}

// Execute runs a task on the first available agent for the required role.
func (e *Executor) Execute(ctx context.Context, task *Task) error {
	// Snapshot all fields needed for this execution under a single lock to
	// avoid data races with concurrent readers (HTTP status endpoints, etc.).
	task.Lock()
	taskID := task.ID
	taskRole := task.AgentRole
	taskTitle := task.Title
	taskComplexity := task.Complexity
	task.Unlock()

	candidates := e.pool.GetByRole(taskRole)
	if len(candidates) == 0 {
		return fmt.Errorf("no agent available for role: %s", taskRole)
	}
	// Prefer the first idle agent (GetByRole returns idle first).
	a := candidates[0]
	agentID := a.Config().ID

	log.Info().
		Str("task", taskID).
		Str("agent", agentID).
		Str("model", a.Config().Model).
		Msg("executing task")

	// Atomically update task fields before handing off to the agent.
	now := time.Now()
	task.Lock()
	task.Status = TaskRunning
	task.StartedAt = &now
	task.AssignedTo = agentID
	task.Unlock()

	// SaveTask does INSERT OR REPLACE which includes the status=running set above.
	// No need for a separate UpdateTaskStatus call — avoids double DB write.
	if err := e.mem.SaveTask(task); err != nil {
		log.Error().Err(err).Str("task", taskID).Msg("SaveTask failed")
		return fmt.Errorf("execute: save task %s: %w", taskID, err)
	}

	e.bus.Publish(Event{
		Type:    EvtTaskStarted,
		AgentID: agentID,
		TaskID:  taskID,
	})

	// Scratchpad: announce task start.
	_ = e.mem.AddScratchpadEntry(state.ScratchpadEntry{
		AgentID:   agentID,
		Kind:      state.KindInProgress,
		Message:   taskTitle,
		TaskID:    taskID,
		Timestamp: time.Now(),
	})

	// Build tiered memory context using snapshotted fields.
	memCtx := e.mem.BuildContext(agentID, taskRole, taskTitle, taskComplexity)

	// Inject learned skills into context for self-improvement.
	// Uses multi-part buffer: index (summaries) is always loaded,
	// detail bodies are loaded on-demand for the most relevant skills.
	if e.skillStore != nil {
		task.Lock()
		taskTags := make([]string, len(task.Tags))
		copy(taskTags, task.Tags)
		task.Unlock()

		memCtx.SkillContext = e.skillStore.BuildSkillContextForTask(state.SkillQuery{
			Role:       taskRole,
			TaskTitle:  taskTitle,
			TaskTags:   taskTags,
			Complexity: taskComplexity,
		})
		memCtx.SkillStore = e.skillStore
	}

	// Run with per-agent timeout.
	timeout := time.Duration(a.Config().TimeoutSecs) * time.Second
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := a.Run(runCtx, task, memCtx)
	if err != nil {
		task.Lock()
		task.Status = TaskFailed
		task.Unlock()

		if dbErr := e.mem.UpdateTaskStatus(taskID, TaskFailed); dbErr != nil {
			log.Error().Err(dbErr).Str("task", taskID).Msg("UpdateTaskStatus(failed) failed")
		}

		// Check whether this error matches a known resolution pattern.
		// If so, append the resolution hint to the task's Blockers-style meta
		// so the Opus review endpoint can surface it immediately.
		if rs := e.mem.Resolved(); rs != nil {
			if matches, serr := rs.Search(err.Error(), taskRole); serr == nil && len(matches) > 0 {
				best := matches[0]
				log.Info().
					Str("task", taskID).
					Str("pattern_id", best.ID).
					Str("resolution", best.ResolutionSummary).
					Msg("known error pattern matched")

				hint := fmt.Sprintf("ERROR: %s\n\nKNOWN FIX: %s\nSee: %s",
					err.Error(), best.ResolutionSummary, best.DetailFile)
				task.Lock()
				if task.Meta == nil {
					task.Meta = make(map[string]string)
				}
				task.Meta["resolution_hint"] = hint
				task.Unlock()
			}
		}

		// Scratchpad: record failure.
		_ = e.mem.AddScratchpadEntry(state.ScratchpadEntry{
			AgentID:   agentID,
			Kind:      state.KindWarning,
			Message:   err.Error(),
			TaskID:    taskID,
			Timestamp: time.Now(),
		})

		e.bus.Publish(Event{
			Type:    EvtTaskFailed,
			AgentID: agentID,
			TaskID:  taskID,
			Payload: err.Error(),
		})

		// Record failure reflection for skill improvement.
		if e.skillStore != nil {
			failReflection := state.PostTaskReflection{
				TaskID:    taskID,
				AgentID:   agentID,
				Role:      taskRole,
				Success:   false,
				AntiPatterns: []string{
					fmt.Sprintf("Task '%s' failed: %s", taskTitle, truncateForReflection(err.Error(), 200)),
				},
				Timestamp: time.Now(),
			}
			if applyErr := e.skillStore.ApplyReflection(failReflection); applyErr != nil {
				log.Warn().Err(applyErr).Str("task", taskID).Msg("ApplyReflection(failure) failed")
			}
		}

		log.Error().Err(err).Str("task", taskID).Msg("task failed")
		return err
	}

	// Log token usage.
	if result != nil {
		addErr := e.mem.AddTokens(taskID, result.InputTokens, result.OutputTokens, result.CostUSD)
		if addErr != nil {
			log.Error().Err(addErr).Str("task", taskID).Msg("AddTokens failed")
		}
		logErr := e.mem.LogTokenUsage(
			taskID, agentID, a.Config().Model,
			result.InputTokens, result.OutputTokens,
			result.CostUSD, result.DurationMs,
		)
		if logErr != nil {
			log.Error().Err(logErr).Str("task", taskID).Msg("LogTokenUsage failed")
			if addErr == nil {
				log.Warn().Str("task", taskID).Msg("token accounting inconsistent: AddTokens succeeded but LogTokenUsage failed")
			}
		}
		e.bus.Publish(Event{
			Type:    EvtTokenLogged,
			AgentID: agentID,
			TaskID:  taskID,
			Payload: result,
		})
	}

	finished := time.Now()
	task.Lock()
	task.Status = TaskDone
	task.FinishedAt = &finished
	task.Unlock()

	if err := e.mem.UpdateTaskStatus(taskID, TaskDone); err != nil {
		log.Error().Err(err).Str("task", taskID).Msg("UpdateTaskStatus(done) failed")
	}

	e.bus.Publish(Event{
		Type:    EvtTaskDone,
		AgentID: agentID,
		TaskID:  taskID,
		Payload: result,
	})

	if result != nil {
		log.Info().
			Str("task", taskID).
			Float64("cost", result.CostUSD).
			Int64("tokens", result.InputTokens+result.OutputTokens).
			Msg("task done")
	}

	// Scratchpad: record handoff with next-role hint.
	_ = e.mem.AddScratchpadEntry(state.ScratchpadEntry{
		AgentID:   agentID,
		Kind:      state.KindHandoff,
		Message:   "done, next: " + nextRole(taskRole),
		TaskID:    taskID,
		Timestamp: time.Now(),
	})

	// Persist a one-line outcome summary to the agent's role memory doc for
	// architect/idea roles and for tasks tagged architecture, milestone, or adr.
	if isMemoryWorthy(task) {
		var cost float64
		if result != nil {
			cost = result.CostUSD
		}
		summary := fmt.Sprintf("**[%s] %s** — done (cost $%.4f)", taskID, taskTitle, cost)
		if aerr := e.mem.AppendAgentDoc(taskRole, summary); aerr != nil {
			log.Warn().Err(aerr).Str("task", taskID).Msg("AppendAgentDoc failed")
		}
	}

	// Run post-completion hooks (Trello card creation, notifications, etc.).
	// Errors are logged but never fail the task.
	for _, hook := range e.hooks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error().Interface("panic", r).Str("task", taskID).Msg("post-run hook panicked")
				}
			}()
			hook(ctx, a, task, result)
		}()
	}

	return nil
}

// nextRoleMap is allocated once at package init. The previous implementation
// allocated a new map on every call — wasteful since the mapping is static.
var nextRoleMap = map[string]string{
	"idea":      "architect",
	"architect": "breakdown",
	"breakdown": "coding",
	"coding":    "test",
	"test":      "review",
	"review":    "deploy",
	"deploy":    "notify",
	"notify":    "—",
	"docs":      "review",
}

// nextRole returns the conventional downstream role for a given upstream role.
func nextRole(role string) string {
	if n, ok := nextRoleMap[role]; ok {
		return n
	}
	return "—"
}

// isMemoryWorthy reports whether a completed task's outcome should be appended
// to the role's agent doc for future reference.
func isMemoryWorthy(task *Task) bool {
	task.Lock()
	role := task.AgentRole
	// Deep-copy tags to avoid iterating a shared backing array after unlock.
	tags := make([]string, len(task.Tags))
	copy(tags, task.Tags)
	task.Unlock()

	if role == "architect" || role == "idea" {
		return true
	}
	for _, tag := range tags {
		switch strings.ToLower(tag) {
		case "architecture", "milestone", "adr":
			return true
		}
	}
	return false
}

// truncateForReflection truncates a string to maxLen for use in reflection data.
// truncateForReflection returns the first maxLen runes. Safe for multi-byte UTF-8.
func truncateForReflection(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen])
}
