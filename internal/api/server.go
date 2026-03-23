package api

import (
	"context"
	"sync"

	"github.com/gofiber/fiber/v2"
	"github.com/patricksign/AgentClaw/config"
	infra "github.com/patricksign/AgentClaw/internal/infra/config"
	"github.com/patricksign/AgentClaw/internal/integrations/pipeline"
	"github.com/patricksign/AgentClaw/internal/integrations/trello"
	"github.com/patricksign/AgentClaw/internal/memory"
	"github.com/patricksign/AgentClaw/internal/middleware"
	"github.com/patricksign/AgentClaw/internal/port"
	"github.com/patricksign/AgentClaw/internal/state"
)

// maxConcurrentPipelines limits how many trigger pipelines can run simultaneously.
const maxConcurrentPipelines = 5

// ─── Server ──────────────────────────────────────────────────────────────────

type Server struct {
	pool       port.AgentPool    // clean-arch: was *agent.Pool
	queue      port.TaskQueue    // clean-arch: was *queue.Queue
	executor   port.TaskExecutor // clean-arch: was *agent.Executor (stored for handlers)
	events     port.EventBus     // clean-arch: was *agent.EventBus
	mem        *memory.Store     // legacy — separate migration
	hub        *wsHub
	triggerSvc *pipeline.Service      // legacy — separate migration
	resolved   *state.ResolvedStore   // may be nil
	scratchpad *state.Scratchpad      // may be nil
	summarizer port.HistorySummarizer // clean-arch: was *summarizer.Summarizer
	ctx        context.Context        // server-scoped context — cancelled on Shutdown
	cancel     context.CancelFunc     // cancels ctx
	httpServer *infra.HttpServer
	pipelineSem chan struct{}  // counting semaphore for concurrent pipeline executions
	mu          sync.RWMutex   // protects triggerSvc and summarizer
	wg          sync.WaitGroup // tracks background goroutines (forwardEvents, hub.run, pipelines)
}

func (s *Server) App() *fiber.App {
	return s.httpServer.App()
}

func NewServer(
	cf *config.Config,
	redisClient *infra.RedisClient,
	pool port.AgentPool,
	q port.TaskQueue,
	exec port.TaskExecutor,
	mem *memory.Store,
	events port.EventBus,
	trelloClient *trello.Client,
	telegramToken string,
	telegramChatID string,
) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		pool:        pool,
		queue:       q,
		executor:    exec,
		mem:         mem,
		events:      events,
		hub:         newWsHub(),
		resolved:    mem.Resolved(),   // may be nil — endpoints handle nil gracefully
		scratchpad:  mem.Scratchpad(), // may be nil
		pipelineSem: make(chan struct{}, maxConcurrentPipelines),
		ctx:         ctx,
		cancel:      cancel,
	}
	httpClient := infra.HttpServer{
		AppName: cf.Server.Http.AppName,
		Conf:    &cf.Server.Http,
		CORS:    &cf.Middleware,
		Redis:   redisClient.Redis(),
		RedisCf: &cf.Redis,
	}
	httpClient.InitHttpServer()
	s.httpServer = &httpClient
	// NOTE: goroutines are NOT started here — call StartBackground() after
	// SetSummarizer/SetTriggerService to avoid data races (#69).
	return s
}

// StartBackground launches background goroutines (forwardEvents, hub.run).
// Must be called AFTER SetSummarizer/SetTriggerService to avoid data races.
func (s *Server) StartBackground() {
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.forwardEvents()
	}()
	go func() {
		defer s.wg.Done()
		s.hub.run()
	}()
}

// SetSummarizer sets the history summarizer (optional — may be nil).
func (s *Server) SetSummarizer(sum port.HistorySummarizer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summarizer = sum
}

// GetSummarizer returns the history summarizer (thread-safe).
func (s *Server) GetSummarizer() port.HistorySummarizer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.summarizer
}

// SetTriggerService sets the pipeline trigger service (optional).
func (s *Server) SetTriggerService(svc *pipeline.Service) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.triggerSvc = svc
}

// GetTriggerService returns the pipeline trigger service (thread-safe).
func (s *Server) GetTriggerService() *pipeline.Service {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.triggerSvc
}

// Shutdown cancels the server context, waits for background goroutines,
// and stops the WebSocket hub.
// HTTP server shutdown is handled by the caller via App().ShutdownWithTimeout().
func (s *Server) Shutdown() {
	s.cancel()
	s.hub.shutdown()
	s.wg.Wait() // wait for forwardEvents and hub.run to exit (#62)
}

// Context returns the server-scoped context that is cancelled on Shutdown.
func (s *Server) Context() context.Context {
	return s.ctx
}

// RegisterRoutes registers all API routes on the internal Fiber app.
// Call Start() separately to begin listening.
func (s *Server) RegisterRoutes(cf *config.Config) {
	jwtCache := middleware.NewJWTCache(s.httpServer.Redis, true)
	auth := middleware.NewAuthenHandler(cf.Middleware, jwtCache)

	app := s.httpServer.App()
	// Public routes (no auth required)
	s.HandlerWebsocket(app)
	s.HandlerHealth(app)

	// Static — serve dashboard frontend
	app.Static("/", "./static")

	// API group
	api := app.Group("/api")

	// Public API endpoints (read-only, no mutations)
	s.HandlerMetric(api)
	s.HandlerPricing(api)

	// Protected API endpoints (require auth)
	protected := api.Group("", auth.AuthMiddleware())
	s.HandlerAgent(protected) // agent restart/kill requires auth
	s.HandlerTask(protected)
	s.HandlerMemory(protected)
	s.HandlerResolved(protected)
	s.HandlerScratchpad(protected)
	s.HandlerTrigger(protected)
	s.HandlerState(protected)
}

// Start launches the HTTP listener in a background goroutine.
// Returns a channel that receives an error if Listen fails.
func (s *Server) Start() <-chan error {
	return s.httpServer.Start()
}
