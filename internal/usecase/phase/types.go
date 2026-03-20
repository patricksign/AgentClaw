package phase

import (
	"github.com/patricksign/agentclaw/internal/domain"
	"github.com/patricksign/agentclaw/internal/port"
)

// PhaseContext carries all dependencies needed by every execution phase.
type PhaseContext struct {
	Task       *domain.Task
	AgentCfg   domain.AgentConfig
	Memory     port.MemoryContext
	Router     port.LLMRouter
	Notifier   port.Notifier
	Escalator  port.Escalator
	TaskStore  port.TaskStore
	StateStore port.StateStore
}
