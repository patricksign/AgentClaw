package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/patricksign/agentclaw/internal/agent"
	"github.com/patricksign/agentclaw/internal/memory"
	"github.com/rs/zerolog/log"
)


func (s *Server) HandlerTask(mux *http.ServeMux) {
	// Tasks
	mux.HandleFunc("GET /api/tasks", cors(s.listTasks))
	mux.HandleFunc("POST /api/tasks", cors(s.createTasks))
	mux.HandleFunc("GET /api/tasks/{id}", cors(s.getTaskById))
	mux.HandleFunc("PATCH /api/tasks/{id}", cors(s.updateTaskById))
	mux.HandleFunc("GET /api/tasks/{id}/logs", cors(s.getTokenLogTask))
}

// ─── Tasks ───────────────────────────────────────────────────────────────────

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		tasks, err := s.mem.ListTasks()
		if err != nil {
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		if tasks == nil {
			tasks = []*agent.Task{}
		}
		writeJSON(w, http.StatusOK, tasks)
	default:
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) createTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var req struct {
			Title       string         `json:"title"`
			Description string         `json:"description"`
			AgentRole   string         `json:"agent_role"`
			Priority    agent.Priority `json:"priority"`
			DependsOn   []string       `json:"depends_on"`
			Tags        []string       `json:"tags"`
		}
		if err := readJSON(r, &req); err != nil {
			errJSON(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if req.Title == "" || req.AgentRole == "" {
			errJSON(w, http.StatusBadRequest, "title and agent_role required")
			return
		}
		if req.Priority == 0 {
			req.Priority = agent.PriorityNormal
		}

		task := &agent.Task{
			ID:          "T-" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12],
			Title:       req.Title,
			Description: req.Description,
			AgentRole:   req.AgentRole,
			Priority:    req.Priority,
			DependsOn:   req.DependsOn,
			Tags:        req.Tags,
			Status:      agent.TaskQueued,
			CreatedAt:   time.Now(),
		}

		// Lưu vào memory
		if err := s.mem.SaveTask(task); err != nil {
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Push vào queue
		s.queue.Push(task)

		// Broadcast event
		s.bus.Publish(agent.Event{
			Type:    agent.EvtTaskQueued,
			TaskID:  task.ID,
			Payload: task,
		})

		log.Info().Str("task", task.ID).Str("role", task.AgentRole).Msg("task queued")
		writeJSON(w, http.StatusCreated, task)

	default:
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// GET /api/tasks/{id} — get task detail
func (s *Server) getTaskById(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if taskID == "" {
		errJSON(w, http.StatusBadRequest, "missing task id")
		return
	}
	task, err := s.mem.GetTask(taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			errJSON(w, http.StatusNotFound, "task not found")
		} else {
			log.Error().Err(err).Str("task", taskID).Msg("getTaskById failed")
			errJSON(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// PATCH /api/tasks/{id} — update status
func (s *Server) updateTaskById(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if taskID == "" {
		errJSON(w, http.StatusBadRequest, "missing task id")
		return
	}
	var req struct {
		Status agent.TaskStatus `json:"status"`
	}
	if err := readJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	switch req.Status {
	case agent.TaskPending, agent.TaskQueued, agent.TaskRunning,
		agent.TaskDone, agent.TaskFailed, agent.TaskCancelled:
		// valid
	default:
		errJSON(w, http.StatusBadRequest, "invalid status value")
		return
	}
	if err := s.mem.UpdateTaskStatus(taskID, req.Status); err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	task, err := s.mem.GetTask(taskID)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "task updated but could not be retrieved")
		return
	}
	// Map status to the correct event type instead of always emitting EvtTaskDone.
	evtType := agent.EvtTaskDone
	switch req.Status {
	case agent.TaskFailed:
		evtType = agent.EvtTaskFailed
	case agent.TaskQueued:
		evtType = agent.EvtTaskQueued
	case agent.TaskRunning:
		evtType = agent.EvtTaskStarted
	}
	s.bus.Publish(agent.Event{
		Type:    evtType,
		TaskID:  taskID,
		Payload: task,
	})
	writeJSON(w, http.StatusOK, task)
}

// GET /api/tasks/{id}/logs — token logs for task
func (s *Server) getTokenLogTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if taskID == "" {
		errJSON(w, http.StatusBadRequest, "missing task id")
		return
	}
	logs, err := s.mem.GetTokenLogs(taskID)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if logs == nil {
		logs = []memory.TokenLog{}
	}
	writeJSON(w, http.StatusOK, logs)
}
