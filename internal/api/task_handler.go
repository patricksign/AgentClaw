package api

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/patricksign/AgentClaw/common"
	"github.com/patricksign/AgentClaw/internal/adapter"
	"log/slog"

	"github.com/patricksign/AgentClaw/internal/memory"
)

func (s *Server) HandlerTask(c fiber.Router) {
	GET(c, "/tasks", s.listTasks)
	POST(c, "/tasks", s.createTasks)
	GET(c, "/tasks/:id", s.getTaskById)
	PUT(c, "/tasks/:id", s.updateTaskById)
	GET(c, "/tasks/:id/logs", s.getTokenLogTask)
}

// ─── Tasks ───────────────────────────────────────────────────────────────────

// validAgentRoles is the canonical set of roles that agents can be assigned.
var validAgentRoles = map[string]bool{
	"idea": true, "architect": true, "breakdown": true,
	"coding": true, "test": true, "review": true,
	"docs": true, "deploy": true, "notify": true,
}

func isValidAgentRole(role string) bool {
	return validAgentRoles[role]
}

func (s *Server) listTasks(c *fiber.Ctx) error {
	tasks, err := s.mem.ListTasks()
	if err != nil {
		return common.ResponseApiStatusCode(c, fiber.StatusInternalServerError, nil, err)
	}
	if tasks == nil {
		tasks = []*adapter.Task{}
	}
	return common.ResponseApiOK(c, tasks, nil)
}

func (s *Server) createTasks(c *fiber.Ctx) error {
	var req struct {
		Title       string           `json:"title"`
		Description string           `json:"description"`
		AgentRole   string           `json:"agent_role"`
		Priority    adapter.Priority `json:"priority"`
		DependsOn   []string         `json:"depends_on"`
		Tags        []string         `json:"tags"`
	}
	if err := c.BodyParser(&req); err != nil {
		return common.ResponseApiBadRequest(c, nil, errors.New("invalid JSON"))
	}
	if req.Title == "" || req.AgentRole == "" {
		return common.ResponseApiBadRequest(c, nil, errors.New("title and agent_role required"))
	}
	if !isValidAgentRole(req.AgentRole) {
		return common.ResponseApiBadRequest(c, nil, errors.New("invalid agent_role: must be one of idea, architect, breakdown, coding, test, review, docs, deploy, notify"))
	}
	if req.Priority == 0 {
		req.Priority = adapter.PriorityNormal
	}

	task := &adapter.Task{
		ID:          "T-" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12],
		Title:       req.Title,
		Description: req.Description,
		AgentRole:   req.AgentRole,
		Priority:    req.Priority,
		DependsOn:   req.DependsOn,
		Tags:        req.Tags,
		Status:      adapter.TaskQueued,
		CreatedAt:   time.Now(),
	}

	if err := s.mem.SaveTask(task); err != nil {
		return common.ResponseApiStatusCode(c, fiber.StatusInternalServerError, nil, err)
	}

	s.queue.Push(task)

	s.events.Publish(adapter.Event{
		Type:    adapter.EvtTaskQueued,
		TaskID:  task.ID,
		Payload: task,
	})

	slog.Info("task queued", "task", task.ID, "role", task.AgentRole)
	return common.ResponseApiStatusCode(c, fiber.StatusCreated, task, nil)
}

// GET /api/tasks/:id
func (s *Server) getTaskById(c *fiber.Ctx) error {
	taskID := c.Params("id")
	if taskID == "" {
		return common.ResponseApiBadRequest(c, nil, errors.New("missing task id"))
	}
	task, err := s.mem.GetTask(taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return common.ResponseApiStatusCode(c, fiber.StatusNotFound, nil, errors.New("task not found"))
		}
		slog.Error("getTaskById failed", "err", err, "task", taskID)
		return common.ResponseApiStatusCode(c, fiber.StatusInternalServerError, nil, errors.New("internal error"))
	}
	return common.ResponseApiOK(c, task, nil)
}

// PATCH /api/tasks/:id
func (s *Server) updateTaskById(c *fiber.Ctx) error {
	taskID := c.Params("id")
	if taskID == "" {
		return common.ResponseApiBadRequest(c, nil, errors.New("missing task id"))
	}
	var req struct {
		Status adapter.TaskStatus `json:"status"`
	}
	if err := c.BodyParser(&req); err != nil {
		return common.ResponseApiBadRequest(c, nil, errors.New("invalid JSON"))
	}
	switch req.Status {
	case adapter.TaskPending, adapter.TaskQueued, adapter.TaskRunning,
		adapter.TaskDone, adapter.TaskFailed, adapter.TaskCancelled:
		// valid
	default:
		return common.ResponseApiBadRequest(c, nil, errors.New("invalid status value"))
	}
	if err := s.mem.UpdateTaskStatus(taskID, req.Status); err != nil {
		return common.ResponseApiStatusCode(c, fiber.StatusInternalServerError, nil, err)
	}
	task, err := s.mem.GetTask(taskID)
	if err != nil {
		return common.ResponseApiStatusCode(c, fiber.StatusInternalServerError, nil, errors.New("task updated but could not be retrieved"))
	}
	evtType := adapter.EvtTaskDone
	switch req.Status {
	case adapter.TaskFailed:
		evtType = adapter.EvtTaskFailed
	case adapter.TaskQueued:
		evtType = adapter.EvtTaskQueued
	case adapter.TaskRunning:
		evtType = adapter.EvtTaskStarted
	}
	s.events.Publish(adapter.Event{
		Type:    evtType,
		TaskID:  taskID,
		Payload: task,
	})
	return common.ResponseApiOK(c, task, nil)
}

// GET /api/tasks/:id/logs
func (s *Server) getTokenLogTask(c *fiber.Ctx) error {
	taskID := c.Params("id")
	if taskID == "" {
		return common.ResponseApiBadRequest(c, nil, errors.New("missing task id"))
	}
	logs, err := s.mem.GetTokenLogs(taskID)
	if err != nil {
		return common.ResponseApiStatusCode(c, fiber.StatusInternalServerError, nil, err)
	}
	if logs == nil {
		logs = []memory.TokenLog{}
	}
	return common.ResponseApiOK(c, logs, nil)
}
