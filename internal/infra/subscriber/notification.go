package subscriber

import (
	"context"
	"time"

	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/port"
	"log/slog"
)

// NotificationSubscriber listens for domain events and fans out
// to all registered port.Notifier implementations (Telegram, Slack, etc.).
// Adding a new channel = append to notifiers slice, zero code changes.
type NotificationSubscriber struct {
	bus       port.DomainEventBus
	notifiers []port.Notifier
	unsub     func()
}

// subscribedEventTypes lists all event types this subscriber cares about.
var subscribedEventTypes = []domain.EventType{
	domain.EventTaskStarted,
	domain.EventTaskDone,
	domain.EventTaskFailed,
	domain.EventFallbackTriggered,
	domain.EventFallbackExhausted,
	domain.EventPlanApproved,
	domain.EventPlanFailed,
	domain.EventEscalated,
	domain.EventPRCreated,
	domain.EventDeployStarted,
	domain.EventDeployDone,
	domain.EventPipelineStarted,
	domain.EventPipelineCompleted,
	domain.EventPipelinePartial,
}

// NewNotificationSubscriber creates and immediately starts the subscriber.
func NewNotificationSubscriber(bus port.DomainEventBus, notifiers ...port.Notifier) *NotificationSubscriber {
	ns := &NotificationSubscriber{
		bus:       bus,
		notifiers: notifiers,
	}
	ns.unsub = bus.Subscribe("notification-subscriber", subscribedEventTypes, ns.handle)
	return ns
}

// Stop unsubscribes from the event bus.
func (ns *NotificationSubscriber) Stop() {
	if ns.unsub != nil {
		ns.unsub()
	}
}

// handle fans out a domain event to every registered notifier concurrently.
// Fire-and-forget: does NOT block the event bus semaphore slot while
// waiting for slow notifiers (e.g. Telegram with network timeout).
func (ns *NotificationSubscriber) handle(evt domain.Event) {
	for _, n := range ns.notifiers {
		go func(notifier port.Notifier) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := notifier.Dispatch(ctx, evt); err != nil {
				slog.Warn("notification-subscriber: dispatch failed", "err", err, "event_type", string(evt.Type), "task_id", evt.TaskID)
			}
		}(n)
	}
}
