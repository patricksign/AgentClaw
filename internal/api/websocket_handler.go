package api

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
)

func (s *Server) HandlerWebsocket(c fiber.Router) {
	// Upgrade check middleware — reject non-WebSocket requests on /ws
	c.Use("/ws", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})
	c.Get("/ws", websocket.New(s.handleWS))
}

// ─── WebSocket ───────────────────────────────────────────────────────────────

func (s *Server) handleWS(conn *websocket.Conn) {
	c := &wsClient{conn: conn, send: make(chan []byte, 64)}

	// Non-blocking register: if hub is shutting down, close immediately.
	select {
	case s.hub.register <- c:
	case <-s.hub.stop:
		conn.Close()
		return
	}

	var unregisterOnce sync.Once
	cleanup := func() {
		unregisterOnce.Do(func() {
			select {
			case s.hub.unregister <- c:
			case <-s.hub.stop:
			}
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

	// read pump — blocks until disconnect
	// Limit read size to 4 KiB to prevent OOM from malicious large messages.
	conn.SetReadLimit(4096)
	defer cleanup()
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// forwardEvents subscribes to EventBus and broadcasts to all WS clients.
func (s *Server) forwardEvents() {
	ch, unsub := s.events.Subscribe("ws-hub")
	defer unsub()
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			s.hub.broadcast <- data
		case <-s.ctx.Done():
			return
		}
	}
}

// ─── WebSocket Hub ────────────────────────────────────────────────────────────

type wsClient struct {
	conn *websocket.Conn
	send chan []byte
}

// maxWSClients caps the number of concurrent WebSocket connections.
const maxWSClients = 100

type wsHub struct {
	clients    map[*wsClient]bool
	broadcast  chan []byte
	register   chan *wsClient
	unregister chan *wsClient
	stop       chan struct{}
}

func newWsHub() *wsHub {
	return &wsHub{
		clients:    make(map[*wsClient]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *wsClient),
		unregister: make(chan *wsClient),
		stop:       make(chan struct{}),
	}
}

func (h *wsHub) shutdown() {
	close(h.stop)
}

func (h *wsHub) run() {
	for {
		select {
		case <-h.stop:
			for c := range h.clients {
				close(c.send)
			}
			h.clients = make(map[*wsClient]bool)
			return

		case c := <-h.register:
			if len(h.clients) >= maxWSClients {
				close(c.send)
				c.conn.Close()
				slog.Warn("ws client rejected: max connections reached", "total", len(h.clients))
				continue
			}
			h.clients[c] = true
			slog.Debug("ws client connected", "total", len(h.clients))

		case c := <-h.unregister:
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
			}
			slog.Debug("ws client disconnected", "total", len(h.clients))

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
