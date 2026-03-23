package eventbus

import (
	"sync"
	"sync/atomic"

	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/port"
	"log/slog"
)

// Compile-time check.
var _ port.DomainEventBus = (*InProcessDomainBus)(nil)

// maxConcurrentHandlers limits the number of goroutines spawned per Publish call.
const maxConcurrentHandlers = 64

// subscription holds a single subscriber's metadata and handler.
type subscription struct {
	id      uint64
	name    string
	types   map[domain.EventType]bool
	handler port.EventHandler
}

// InProcessDomainBus is a lightweight, in-process domain event bus.
// Publishers emit events; subscribers receive filtered events via a bounded
// goroutine pool. Thread-safe. Designed to be swapped for NATS/Redis Streams
// later without changing the port.DomainEventBus interface.
type InProcessDomainBus struct {
	mu      sync.RWMutex
	subs    []subscription
	nextID  uint64 // atomic counter for unique subscription IDs
	wg      sync.WaitGroup
	sem     chan struct{} // bounded concurrency semaphore
	stopped uint32       // atomic flag: 1 = stopped
	done    chan struct{} // closed on Stop — used to unblock sem acquire
}

// NewInProcessDomainBus creates a ready-to-use event bus.
func NewInProcessDomainBus() *InProcessDomainBus {
	return &InProcessDomainBus{
		sem:  make(chan struct{}, maxConcurrentHandlers),
		done: make(chan struct{}),
	}
}

// Publish fans out the event to all subscribers whose type filter matches.
// Each matching handler is called in its own goroutine, bounded by the
// concurrency semaphore. Panics inside handlers are recovered and logged.
func (b *InProcessDomainBus) Publish(evt domain.Event) {
	if atomic.LoadUint32(&b.stopped) == 1 {
		return // bus is stopped — no new work
	}

	b.mu.RLock()
	snapshot := make([]subscription, len(b.subs))
	copy(snapshot, b.subs)
	b.mu.RUnlock()

	for _, sub := range snapshot {
		if len(sub.types) > 0 && !sub.types[evt.Type] {
			continue
		}
		h := sub.handler
		name := sub.name

		// Acquire semaphore slot with a select on done channel to prevent
		// blocking indefinitely if Stop() is called between the stopped check
		// and the semaphore acquire (TOCTOU fix).
		b.wg.Add(1)
		select {
		case b.sem <- struct{}{}: // acquired slot
		case <-b.done: // bus is stopping — abort
			b.wg.Done()
			return
		}
		go func() {
			defer func() {
				<-b.sem // release slot
				b.wg.Done()
				if r := recover(); r != nil {
					slog.Error("eventbus: subscriber panicked", "subscriber", name, "event_type", string(evt.Type), "panic", r)
				}
			}()
			h(evt)
		}()
	}
}

// Stop marks the bus as closed and waits for all in-flight handlers to complete.
// After Stop, Publish is a no-op. Closing done unblocks any Publish goroutine
// that is waiting on the semaphore.
func (b *InProcessDomainBus) Stop() {
	atomic.StoreUint32(&b.stopped, 1)
	close(b.done)
	b.wg.Wait()
}

// Subscribe registers a handler for specific event types.
// If eventTypes is empty, the handler receives ALL events.
// Returns an unsubscribe function that removes this subscription.
// Each subscription gets a unique internal ID to avoid name-collision issues.
func (b *InProcessDomainBus) Subscribe(subscriberID string, eventTypes []domain.EventType, handler port.EventHandler) func() {
	typeMap := make(map[domain.EventType]bool, len(eventTypes))
	for _, t := range eventTypes {
		typeMap[t] = true
	}

	subID := atomic.AddUint64(&b.nextID, 1)

	b.mu.Lock()
	b.subs = append(b.subs, subscription{
		id:      subID,
		name:    subscriberID,
		types:   typeMap,
		handler: handler,
	})
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for i, s := range b.subs {
			if s.id == subID {
				b.subs = append(b.subs[:i], b.subs[i+1:]...)
				return
			}
		}
	}
}
