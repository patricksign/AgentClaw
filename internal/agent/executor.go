package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/patricksign/agentclaw/internal/state"
	"github.com/rs/zerolog/log"
)

// MemoryStore interface — avoids circular import with the memory package.
// BuildContext returns a MemoryContext with []*Task to avoid copying mutex values.
type MemoryStore interface {
	BuildContext(role, taskTitle string) MemoryContext
	SaveTask(t *Task) error
	UpdateTaskStatus(id string, status TaskStatus) error
	AddTokens(taskID string, in, out int64, cost float64) error
	LogTokenUsage(taskID, agentID, model string, in, out int64, cost float64, durationMs int64) error
	// Resolved returns the ResolvedStore for error-pattern lookups. May be nil.
	Resolved() *state.ResolvedStore
}

// Executor wires Pool + Queue + Memory + EventBus together for task execution.
type Executor struct {
	pool *Pool
	bus  *EventBus
	mem  MemoryStore
}

func NewExecutor(pool *Pool, bus *EventBus, mem MemoryStore) *Executor {
	return &Executor{pool: pool, bus: bus, mem: mem}
}

// Execute runs a task on the first available agent for the required role.
func (e *Executor) Execute(ctx context.Context, task *Task) error {
	candidates := e.pool.GetByRole(task.AgentRole)
	if len(candidates) == 0 {
		return fmt.Errorf("no agent available for role: %s", task.AgentRole)
	}
	// Prefer the first idle agent (GetByRole returns idle first).
	a := candidates[0]
	agentID := a.Config().ID

	log.Info().
		Str("task", task.ID).
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

	if err := e.mem.SaveTask(task); err != nil {
		log.Error().Err(err).Str("task", task.ID).Msg("SaveTask failed")
	}
	if err := e.mem.UpdateTaskStatus(task.ID, TaskRunning); err != nil {
		log.Error().Err(err).Str("task", task.ID).Msg("UpdateTaskStatus(running) failed")
	}

	e.bus.Publish(Event{
		Type:    EvtTaskStarted,
		AgentID: agentID,
		TaskID:  task.ID,
	})

	// Build memory context — agents never forget.
	memCtx := e.mem.BuildContext(task.AgentRole, task.Title)

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

		if dbErr := e.mem.UpdateTaskStatus(task.ID, TaskFailed); dbErr != nil {
			log.Error().Err(dbErr).Str("task", task.ID).Msg("UpdateTaskStatus(failed) failed")
		}

		// Check whether this error matches a known resolution pattern.
		// If so, append the resolution hint to the task's Blockers-style meta
		// so the Opus review endpoint can surface it immediately.
		if rs := e.mem.Resolved(); rs != nil {
			if matches, serr := rs.Search(err.Error(), task.AgentRole); serr == nil && len(matches) > 0 {
				best := matches[0]
				log.Info().
					Str("task", task.ID).
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

		e.bus.Publish(Event{
			Type:    EvtTaskFailed,
			AgentID: agentID,
			TaskID:  task.ID,
			Payload: err.Error(),
		})
		log.Error().Err(err).Str("task", task.ID).Msg("task failed")
		return err
	}

	// Log token usage.
	if result != nil {
		addErr := e.mem.AddTokens(task.ID, result.InputTokens, result.OutputTokens, result.CostUSD)
		if addErr != nil {
			log.Error().Err(addErr).Str("task", task.ID).Msg("AddTokens failed")
		}
		logErr := e.mem.LogTokenUsage(
			task.ID, agentID, a.Config().Model,
			result.InputTokens, result.OutputTokens,
			result.CostUSD, result.DurationMs,
		)
		if logErr != nil {
			log.Error().Err(logErr).Str("task", task.ID).Msg("LogTokenUsage failed")
			if addErr == nil {
				log.Warn().Str("task", task.ID).Msg("token accounting inconsistent: AddTokens succeeded but LogTokenUsage failed")
			}
		}
		e.bus.Publish(Event{
			Type:    EvtTokenLogged,
			AgentID: agentID,
			TaskID:  task.ID,
			Payload: result,
		})
	}

	finished := time.Now()
	task.Lock()
	task.Status = TaskDone
	task.FinishedAt = &finished
	task.Unlock()

	if err := e.mem.UpdateTaskStatus(task.ID, TaskDone); err != nil {
		log.Error().Err(err).Str("task", task.ID).Msg("UpdateTaskStatus(done) failed")
	}

	e.bus.Publish(Event{
		Type:    EvtTaskDone,
		AgentID: agentID,
		TaskID:  task.ID,
		Payload: result,
	})

	if result != nil {
		log.Info().
			Str("task", task.ID).
			Float64("cost", result.CostUSD).
			Int64("tokens", result.InputTokens+result.OutputTokens).
			Msg("task done")
	}
	return nil
}
