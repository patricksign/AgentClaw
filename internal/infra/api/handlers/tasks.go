package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/patricksign/AgentClaw/internal/port"
)

// ExecutorIface abstracts the executor for task resumption.
type ExecutorIface interface {
	ResumeTask(taskID string) error
}

// ReplyIface abstracts reply resolution for human answers.
type ReplyIface interface {
	ResolveByTask(taskID, answer string) bool
}

// TaskHandlers provides HTTP endpoints for task management.
type TaskHandlers struct {
	tasks   port.TaskStore
	exec    ExecutorIface
	replies ReplyIface
}

// NewTaskHandlers creates task handlers with injected dependencies.
func NewTaskHandlers(tasks port.TaskStore, exec ExecutorIface, replies ReplyIface) *TaskHandlers {
	return &TaskHandlers{tasks: tasks, exec: exec, replies: replies}
}

// Register mounts task routes on the given mux.
func (h *TaskHandlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tasks", h.listTasks)
	mux.HandleFunc("GET /api/tasks/{id}", h.getTask)
	mux.HandleFunc("POST /api/tasks/{id}/answer", h.answerTask)
	mux.HandleFunc("POST /api/tasks/{id}/resume", h.resumeTask)
}

func (h *TaskHandlers) listTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := h.tasks.ListTasks(port.TaskFilter{})
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, tasks)
}

func (h *TaskHandlers) getTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := h.tasks.GetTask(id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if task == nil {
		respondError(w, http.StatusNotFound, "task not found")
		return
	}
	respondJSON(w, http.StatusOK, task)
}

func (h *TaskHandlers) answerTask(w http.ResponseWriter, r *http.Request) {
	limitBody(w, r)
	id := r.PathValue("id")
	var body struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if h.replies == nil || !h.replies.ResolveByTask(id, body.Answer) {
		respondError(w, http.StatusNotFound, "no pending question for task")
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

func (h *TaskHandlers) resumeTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if h.exec == nil {
		respondError(w, http.StatusServiceUnavailable, "executor not available")
		return
	}
	if err := h.exec.ResumeTask(id); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
}
