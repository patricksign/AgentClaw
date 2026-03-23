package port

import "github.com/patricksign/AgentClaw/internal/domain"

// EventHandler processes a domain event. Must be safe for concurrent calls.
type EventHandler func(evt domain.Event)

// DomainEventBus is the central event backbone.
// Publishers emit domain.Event; subscribers receive filtered events.
// All cross-package communication goes through this bus.
type DomainEventBus interface {
	// Publish emits an event to all matching subscribers (non-blocking, fan-out).
	Publish(evt domain.Event)

	// Subscribe registers a handler for specific event types.
	// Returns an unsubscribe function.
	// Handler is called in a dedicated goroutine per event.
	Subscribe(subscriberID string, eventTypes []domain.EventType, handler EventHandler) func()

	// Stop marks the bus as closed and waits for all in-flight handlers to complete.
	Stop()
}
