package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/patricksign/agentclaw/internal/agent"
	"github.com/patricksign/agentclaw/internal/integrations/pipeline"
	"github.com/patricksign/agentclaw/internal/integrations/trello"
	"github.com/patricksign/agentclaw/internal/memory"
	"github.com/patricksign/agentclaw/internal/queue"
	"github.com/patricksign/agentclaw/internal/state"
	"github.com/rs/zerolog/log"
)

// ─── Server ──────────────────────────────────────────────────────────────────

type Server struct {
	pool       *agent.Pool
	queue      *queue.Queue
	mem        *memory.Store
	bus        *agent.EventBus
	hub        *wsHub
	triggerSvc *pipeline.Service
	resolved   *state.ResolvedStore // may be nil
}

func NewServer(
	pool *agent.Pool,
	q *queue.Queue,
	mem *memory.Store,
	bus *agent.EventBus,
	trelloClient *trello.Client,
	telegramToken string,
	telegramChatID string,
) *Server {
	s := &Server{
		pool:       pool,
		queue:      q,
		mem:        mem,
		bus:        bus,
		hub:        newWsHub(),
		triggerSvc: pipeline.NewService(trelloClient),
		resolved:   mem.Resolved(), // may be nil — endpoints handle nil gracefully
	}
	// Forward EventBus → WebSocket clients
	go s.forwardEvents()
	go s.hub.run()
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// WebSocket
	mux.HandleFunc("/ws", s.handleWS)

	// Agents
	mux.HandleFunc("/api/agents", cors(s.handleAgents))
	mux.HandleFunc("/api/agents/", cors(s.handleAgent))

	// Tasks
	mux.HandleFunc("/api/tasks", cors(s.handleTasks))
	mux.HandleFunc("/api/tasks/", cors(s.handleTask))

	// Metrics
	mux.HandleFunc("/api/metrics/today", cors(s.handleMetricsToday))
	mux.HandleFunc("/api/metrics/period", cors(s.handleMetricsPeriod))

	// Memory
	mux.HandleFunc("/api/memory/project", cors(s.handleProjectMemory))

	// Resolved error pattern store
	mux.HandleFunc("/api/state/resolved", cors(s.handleResolved))
	mux.HandleFunc("/api/state/resolved/", cors(s.handleResolvedItem))

	// Trigger pipeline
	mux.HandleFunc("/api/trigger", cors(s.handleTrigger))

	// Static — serve dashboard frontend
	mux.Handle("/", http.FileServer(http.Dir("./static")))

	return mux
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error().Err(err).Msg("writeJSON encode failed")
	}
}

// maxBodyBytes is the maximum accepted request body size (1 MiB).
const maxBodyBytes = 1 << 20

func readJSON(r *http.Request, v any) error {
	limited := io.LimitReader(r.Body, maxBodyBytes)
	return json.NewDecoder(limited).Decode(v)
}

func errJSON(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func cors(next http.HandlerFunc) http.HandlerFunc {
	allowedOrigin := os.Getenv("CORS_ORIGIN")

	return func(w http.ResponseWriter, r *http.Request) {
		origin := allowedOrigin
		switch {
		case r.Method == http.MethodGet || r.Method == http.MethodOptions:
			// GET and preflight: allow wildcard if no specific origin configured.
			if origin == "" {
				origin = "*"
			}
		default:
			// Mutation methods (POST, PATCH, PUT, DELETE): require a specific origin.
			if origin == "" {
				errJSON(w, http.StatusForbidden, "CORS: mutation requests require a specific CORS_ORIGIN")
				return
			}
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,PUT,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

// ─── Agents ──────────────────────────────────────────────────────────────────

// GET /api/agents — list all agents + status
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, 405, "method not allowed")
		return
	}
	statuses := s.pool.StatusAll()
	type agentInfo struct {
		ID     string       `json:"id"`
		Status agent.Status `json:"status"`
	}
	out := make([]agentInfo, 0, len(statuses))
	for id, st := range statuses {
		out = append(out, agentInfo{ID: id, Status: st})
	}
	writeJSON(w, 200, out)
}

// POST /api/agents/:id/restart  — restart agent
// POST /api/agents/:id/kill     — kill agent
func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/agents/"), "/")
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch {
	case action == "restart" && r.Method == http.MethodPost:
		if err := s.pool.Restart(id); err != nil {
			errJSON(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"status": "restarted"})

	case action == "kill" && r.Method == http.MethodPost:
		if err := s.pool.Kill(id); err != nil {
			errJSON(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"status": "killed"})

	default:
		errJSON(w, 405, "method not allowed")
	}
}

// ─── Tasks ───────────────────────────────────────────────────────────────────

// GET  /api/tasks — list tasks
// POST /api/tasks — create + enqueue task
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		tasks, err := s.mem.ListTasks()
		if err != nil {
			errJSON(w, 500, err.Error())
			return
		}
		if tasks == nil {
			tasks = []*agent.Task{}
		}
		writeJSON(w, 200, tasks)

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
			errJSON(w, 400, "invalid JSON")
			return
		}
		if req.Title == "" || req.AgentRole == "" {
			errJSON(w, 400, "title and agent_role required")
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
			errJSON(w, 500, err.Error())
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
		writeJSON(w, 201, task)

	default:
		errJSON(w, 405, "method not allowed")
	}
}

// GET   /api/tasks/:id         — get task detail
// PATCH /api/tasks/:id         — update status
// GET   /api/tasks/:id/logs    — token logs for task
func (s *Server) handleTask(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
	parts := strings.SplitN(path, "/", 2)
	taskID := parts[0]
	sub := ""
	if len(parts) == 2 {
		sub = parts[1]
	}

	switch {
	case sub == "" && r.Method == http.MethodGet:
		task, err := s.mem.GetTask(taskID)
		if err != nil {
			if err == sql.ErrNoRows {
				errJSON(w, http.StatusNotFound, "task not found")
			} else {
				errJSON(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
		writeJSON(w, 200, task)

	case sub == "" && r.Method == http.MethodPatch:
		var req struct {
			Status agent.TaskStatus `json:"status"`
		}
		if err := readJSON(r, &req); err != nil {
			errJSON(w, 400, "invalid JSON")
			return
		}
		switch req.Status {
		case agent.TaskPending, agent.TaskQueued, agent.TaskRunning,
			agent.TaskDone, agent.TaskFailed, agent.TaskCancelled:
			// valid
		default:
			errJSON(w, 400, "invalid status value")
			return
		}
		if err := s.mem.UpdateTaskStatus(taskID, req.Status); err != nil {
			errJSON(w, 500, err.Error())
			return
		}
		task, err := s.mem.GetTask(taskID)
		if err != nil {
			errJSON(w, 500, "task updated but could not be retrieved")
			return
		}
		s.bus.Publish(agent.Event{
			Type:    agent.EvtTaskDone,
			TaskID:  taskID,
			Payload: task,
		})
		writeJSON(w, 200, task)

	case sub == "logs" && r.Method == http.MethodGet:
		logs, err := s.mem.GetTokenLogs(taskID)
		if err != nil {
			errJSON(w, 500, err.Error())
			return
		}
		if logs == nil {
			logs = []memory.TokenLog{}
		}
		writeJSON(w, 200, logs)

	default:
		errJSON(w, 405, "method not allowed")
	}
}

// ─── Metrics ─────────────────────────────────────────────────────────────────

// GET /api/metrics/today
func (s *Server) handleMetricsToday(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, 405, "method not allowed")
		return
	}
	today := time.Now().Format("2006-01-02")
	stats, err := s.mem.StatsForPeriod(today)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, stats)
}

// GET /api/metrics/period?from=2026-01-01&to=2026-03-31
func (s *Server) handleMetricsPeriod(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, 405, "method not allowed")
		return
	}
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" {
		from = time.Now().AddDate(0, -1, 0).Format("2006-01-02")
	}
	if to == "" {
		to = time.Now().Format("2006-01-02")
	}
	stats, err := s.mem.StatsForRange(from, to)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, stats)
}

// ─── Memory ──────────────────────────────────────────────────────────────────

// GET   /api/memory/project — đọc project.md
// PATCH /api/memory/project — cập nhật project.md
func (s *Server) handleProjectMemory(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		doc := s.mem.ReadProjectDoc()
		writeJSON(w, 200, map[string]string{"content": doc})

	case http.MethodPatch:
		var req struct {
			Section string `json:"section"`
		}
		if err := readJSON(r, &req); err != nil {
			errJSON(w, 400, "invalid JSON")
			return
		}
		if err := s.mem.AppendProjectDoc(req.Section); err != nil {
			errJSON(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"status": "updated"})

	default:
		errJSON(w, 405, "method not allowed")
	}
}

// ─── Resolved error patterns ─────────────────────────────────────────────────

// GET /api/state/resolved
// Returns all ErrorPattern entries sorted by occurrence_count desc.
func (s *Server) handleResolved(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, 405, "method not allowed")
		return
	}
	if s.resolved == nil {
		writeJSON(w, 200, []state.ErrorPattern{})
		return
	}
	patterns, err := s.resolved.LoadAll()
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if patterns == nil {
		patterns = []state.ErrorPattern{}
	}
	writeJSON(w, 200, patterns)
}

// handleResolvedItem handles:
//
//	GET   /api/state/resolved/:id          — return full detail file content
//	PATCH /api/state/resolved/:id/resolve  — mark as resolved
func (s *Server) handleResolvedItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/state/resolved/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]
	sub := ""
	if len(parts) == 2 {
		sub = parts[1]
	}

	if !isValidResolvedID(id) {
		errJSON(w, http.StatusBadRequest, "invalid pattern id")
		return
	}

	if s.resolved == nil {
		errJSON(w, 503, "resolved store not configured")
		return
	}

	switch {
	case sub == "" && r.Method == http.MethodGet:
		detail, err := s.resolved.LoadDetail(id)
		if err != nil {
			errJSON(w, 404, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"id": id, "detail": detail})

	case sub == "resolve" && r.Method == http.MethodPatch:
		if err := s.resolved.MarkResolved(id); err != nil {
			errJSON(w, 404, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"status": "resolved"})

	default:
		errJSON(w, 405, "method not allowed")
	}
}

// isValidResolvedID reports whether id is a valid 6-hex-character pattern ID.
func isValidResolvedID(id string) bool {
	if len(id) != 6 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// ─── Trigger ─────────────────────────────────────────────────────────────────

// POST /api/trigger
// Body: {"workspace_id":"<board_id>","ticket_id":"<card_id_or_shortlink>"}
// Returns 202 Accepted immediately; the agent pipeline runs in the background.
func (s *Server) handleTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errJSON(w, 405, "method not allowed")
		return
	}

	var req struct {
		WorkspaceID string `json:"workspace_id"`
		TicketID    string `json:"ticket_id"`
	}
	if err := readJSON(r, &req); err != nil {
		errJSON(w, 400, "invalid JSON")
		return
	}
	if req.WorkspaceID == "" || req.TicketID == "" {
		errJSON(w, 400, "workspace_id and ticket_id are required")
		return
	}

	if s.triggerSvc == nil || !s.triggerSvc.IsConfigured() {
		errJSON(w, 503, "Trello integration not configured (TRELLO_KEY/TRELLO_TOKEN missing)")
		return
	}

	// Return 202 immediately; run pipeline in background.
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":       "accepted",
		"workspace_id": req.WorkspaceID,
		"ticket_id":    req.TicketID,
	})

	go func() {
		if err := s.triggerSvc.Run(context.Background(), req.WorkspaceID, req.TicketID); err != nil {
			log.Error().Err(err).
				Str("workspace_id", req.WorkspaceID).
				Str("ticket_id", req.TicketID).
				Msg("trigger pipeline failed")
		}
	}()
}

// ─── WebSocket ───────────────────────────────────────────────────────────────

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		allowed := os.Getenv("CORS_ORIGIN")
		if allowed == "" {
			// No origin configured: allow same-host only.
			origin := r.Header.Get("Origin")
			return origin == "" || origin == "http://"+r.Host || origin == "https://"+r.Host
		}
		return r.Header.Get("Origin") == allowed
	},
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("ws upgrade failed")
		return
	}
	s.hub.connect(conn)
}

// forwardEvents subscribe EventBus → broadcast to all WS clients
func (s *Server) forwardEvents() {
	ch, unsub := s.bus.Subscribe("ws-hub")
	defer unsub()
	for evt := range ch {
		data, err := json.Marshal(evt)
		if err != nil {
			continue
		}
		s.hub.broadcast <- data
	}
}

// ─── WebSocket Hub ────────────────────────────────────────────────────────────

type wsClient struct {
	conn *websocket.Conn
	send chan []byte
}

type wsHub struct {
	clients    map[*wsClient]bool
	broadcast  chan []byte
	register   chan *wsClient
	unregister chan *wsClient
}

func newWsHub() *wsHub {
	return &wsHub{
		clients:    make(map[*wsClient]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *wsClient),
		unregister: make(chan *wsClient),
	}
}

func (h *wsHub) run() {
	for {
		select {
		case c := <-h.register:
			h.clients[c] = true
			log.Debug().Int("total", len(h.clients)).Msg("ws client connected")

		case c := <-h.unregister:
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
			}
			log.Debug().Int("total", len(h.clients)).Msg("ws client disconnected")

		case msg := <-h.broadcast:
			for c := range h.clients {
				select {
				case c.send <- msg:
				default:
					close(c.send)
					delete(h.clients, c)
				}
			}
		}
	}
}

func (h *wsHub) connect(conn *websocket.Conn) {
	c := &wsClient{conn: conn, send: make(chan []byte, 64)}
	h.register <- c

	// unregisterOnce ensures exactly one unregister + conn.Close regardless
	// of which pump detects the disconnect first, preventing double-close panics.
	var unregisterOnce sync.Once
	cleanup := func() {
		unregisterOnce.Do(func() {
			h.unregister <- c
			conn.Close()
		})
	}

	// write pump
	go func() {
		defer cleanup()
		for msg := range c.send {
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}()

	// read pump — on disconnect, trigger cleanup so unregister fires even if
	// the write pump is blocked on a send.
	go func() {
		defer cleanup()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
}
