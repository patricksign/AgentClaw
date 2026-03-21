package domain

import "time"

// ─── Channel ────────────────────────────────────────────────────────────────

type Channel int

const (
	StatusChannel Channel = iota // automated pipeline events
	HumanChannel                 // human↔agent Q&A
)

// ─── Event Types ────────────────────────────────────────────────────────────

type EventType string

const (
	EventTaskStarted      EventType = "task.started"
	EventTaskDone         EventType = "task.done"
	EventTaskFailed       EventType = "task.failed"
	EventPhaseTransition  EventType = "phase.transition"
	EventQuestionAsked    EventType = "question.asked"
	EventQuestionAnswered EventType = "question.answered"
	EventQuestionExpired  EventType = "question.expired"
	EventEscalated        EventType = "escalated"
	EventPlanSubmitted    EventType = "plan.submitted"
	EventPlanApproved     EventType = "plan.approved"
	EventPlanRedirected   EventType = "plan.redirected"
	EventPlanFailed       EventType = "plan.failed"
	EventParallelStarted    EventType = "parallel.started"
	EventParallelDone       EventType = "parallel.done"
	EventFallbackTriggered  EventType = "fallback.triggered"
	EventFallbackExhausted  EventType = "fallback.exhausted"
)

// Event represents something that happened in the system.
type Event struct {
	Type       EventType         `json:"type"`
	Channel    Channel           `json:"channel"`
	TaskID     string            `json:"task_id"`
	AgentID    string            `json:"agent_id"`
	AgentRole  string            `json:"agent_role"`
	Model      string            `json:"model"`
	Payload    map[string]string `json:"payload"`
	OccurredAt time.Time         `json:"occurred_at"`
}
