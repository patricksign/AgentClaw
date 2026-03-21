package api

import (
	"context"
	"net/http"

	"github.com/patricksign/AgentClaw/internal/agent"
	"github.com/patricksign/AgentClaw/internal/integrations/pipeline"
	"github.com/patricksign/AgentClaw/internal/integrations/trello"
	"github.com/patricksign/AgentClaw/internal/memory"
	"github.com/patricksign/AgentClaw/internal/queue"
	"github.com/patricksign/AgentClaw/internal/state"
	"github.com/patricksign/AgentClaw/internal/summarizer"
)

// ─── Server ──────────────────────────────────────────────────────────────────

type Server struct {
	pool        *agent.Pool
	queue       *queue.Queue
	mem         *memory.Store
	bus         *agent.EventBus
	hub         *wsHub
	triggerSvc  *pipeline.Service
	resolved    *state.ResolvedStore   // may be nil
	scratchpad  *state.Scratchpad      // may be nil
	summarizer  *summarizer.Summarizer // may be nil — set via SetSummarizer
	rateLimiter *rateLimiter
	ctx         context.Context    // server-scoped context — cancelled on Shutdown
	cancel      context.CancelFunc // cancels ctx
}

func NewServer(
	pool *agent.Pool,
	q *queue.Queue,
	exec *agent.Executor,
	mem *memory.Store,
	bus *agent.EventBus,
	trelloClient *trello.Client,
	telegramToken string,
	telegramChatID string,
) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		pool:        pool,
		queue:       q,
		mem:         mem,
		bus:         bus,
		hub:         newWsHub(),
		triggerSvc:  pipeline.NewService(trelloClient, exec, q, bus),
		resolved:    mem.Resolved(),   // may be nil — endpoints handle nil gracefully
		scratchpad:  mem.Scratchpad(), // may be nil
		rateLimiter: newRateLimiter(),
		ctx:         ctx,
		cancel:      cancel,
	}
	// Forward EventBus → WebSocket clients
	go s.forwardEvents()
	go s.hub.run()
	return s
}

// Shutdown stops the WebSocket hub and its event-forwarding goroutine.
// Call this after the HTTP server has stopped accepting new connections.
// Shutdown stops background pipelines and the WebSocket hub.
// Call this after the HTTP server has stopped accepting new connections.
func (s *Server) Shutdown() {
	s.cancel() // cancel all in-flight pipeline goroutines
	s.hub.shutdown()
	s.rateLimiter.Stop()
}

// Context returns the server-scoped context that is cancelled on Shutdown.
// Use this for background work that should stop when the server stops.
func (s *Server) Context() context.Context {
	return s.ctx
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	s.HandlerWebsocket(mux)
	s.HandlerAgent(mux)
	s.HandlerTask(mux)
	s.HandlerMemory(mux)
	s.HandlerResolved(mux)
	s.HandlerScratchpad(mux)
	s.HandlerTrigger(mux)
	s.HandlerState(mux)
	s.HandlerMetric(mux)
	s.HandlerPricing(mux)
	s.HandlerHealth(mux)

	// Static — serve dashboard frontend
	mux.Handle("/", http.FileServer(http.Dir("./static")))

	// Apply rate limiting to all routes except /ws.
	return withRateLimit(s.rateLimiter, []string{"/ws"}, mux)
}
