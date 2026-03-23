package subscriber

import (
	"time"

	"github.com/patricksign/AgentClaw/internal/adapter"
	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/port"
	"log/slog"
)

// TaskDeliverySubscriber listens for EventTaskSubmitted and pushes the task
// into the legacy queue. Pipeline publishes the event; this subscriber routes it.
type TaskDeliverySubscriber struct {
	bus   port.DomainEventBus
	queue port.TaskQueue
	unsub func()
}

// NewTaskDeliverySubscriber creates and immediately starts the subscriber.
func NewTaskDeliverySubscriber(bus port.DomainEventBus, queue port.TaskQueue) *TaskDeliverySubscriber {
	tds := &TaskDeliverySubscriber{
		bus:   bus,
		queue: queue,
	}
	tds.unsub = bus.Subscribe("task-delivery-subscriber", []domain.EventType{
		domain.EventTaskSubmitted,
	}, tds.handle)
	return tds
}

// Stop unsubscribes from the event bus.
func (tds *TaskDeliverySubscriber) Stop() {
	if tds.unsub != nil {
		tds.unsub()
	}
}

// handle receives a task.submitted event and pushes the task into the queue.
func (tds *TaskDeliverySubscriber) handle(evt domain.Event) {
	if tds.queue == nil {
		slog.Warn("task-delivery: queue is nil — cannot dispatch", "task_id", evt.TaskID)
		return
	}

	slog.Info("task-delivery: received task.submitted — pushing to queue", "task_id", evt.TaskID, "agent_role", evt.AgentRole)

	// Convert domain event payload back to adapter.Task for the legacy queue.
	task := &adapter.Task{
		ID:          evt.TaskID,
		Title:       evt.Payload["title"],
		Description: evt.Payload["description"],
		AgentRole:   evt.AgentRole,
		Complexity:  evt.Payload["complexity"],
		Priority:    adapter.PriorityNormal,
		Status:      adapter.TaskPending,
		CreatedAt:   time.Now(),
	}

	tds.queue.Push(task)
}
