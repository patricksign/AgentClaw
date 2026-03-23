package subscriber

import (
	"context"
	"time"

	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/port"
	"log/slog"
)

// FeedbackSubscriber listens for question/escalation events and routes them
// to the human channel via port.HumanAsker.
type FeedbackSubscriber struct {
	bus   port.DomainEventBus
	asker port.HumanAsker
	unsub func()
}

// NewFeedbackSubscriber creates and immediately starts the subscriber.
func NewFeedbackSubscriber(bus port.DomainEventBus, asker port.HumanAsker) *FeedbackSubscriber {
	fs := &FeedbackSubscriber{
		bus:   bus,
		asker: asker,
	}
	fs.unsub = bus.Subscribe("feedback-subscriber", []domain.EventType{
		domain.EventQuestionAsked,
		domain.EventEscalated,
	}, fs.handle)
	return fs
}

// Stop unsubscribes from the event bus.
func (fs *FeedbackSubscriber) Stop() {
	if fs.unsub != nil {
		fs.unsub()
	}
}

// handle routes question/escalation events to the human channel.
func (fs *FeedbackSubscriber) handle(evt domain.Event) {
	if fs.asker == nil {
		return
	}

	question := evt.Payload["question"]
	if question == "" {
		question = evt.Payload["message"]
	}
	if question == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	questionID := evt.Payload["question_id"]
	taskTitle := evt.Payload["task_title"]

	_, err := fs.asker.AskHuman(ctx, evt.AgentID, evt.TaskID, taskTitle, questionID, question)
	if err != nil {
		slog.Warn("feedback-subscriber: AskHuman failed", "err", err, "task_id", evt.TaskID, "event_type", string(evt.Type))
	}
}
