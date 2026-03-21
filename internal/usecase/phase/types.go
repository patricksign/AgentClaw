package phase

import (
	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/port"
)

// PhaseContext carries all dependencies needed by every execution phase.
type PhaseContext struct {
	Task            *domain.Task
	AgentCfg        domain.AgentConfig
	Memory          port.MemoryContext
	Router          port.LLMRouter
	Notifier        port.Notifier
	Escalator       port.Escalator
	TaskStore       port.TaskStore
	StateStore      port.StateStore
	CheckpointStore port.CheckpointStore // nil-safe: checkpoint is best-effort
}
