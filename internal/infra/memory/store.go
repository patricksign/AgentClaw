package memory

import (
	"fmt"

	memcore "github.com/patricksign/AgentClaw/internal/memory"
	"github.com/patricksign/AgentClaw/internal/port"
)

// Compile-time check: Store implements port.MemoryStore.
var _ port.MemoryStore = (*Store)(nil)

// Store wraps the core memory.Store to satisfy port.MemoryStore.
type Store struct {
	core *memcore.Store
}

// NewStore creates an infra memory Store wrapping the core memory store.
func NewStore(core *memcore.Store) *Store {
	return &Store{core: core}
}

// BuildContext assembles a tiered MemoryContext and converts to port.MemoryContext.
func (s *Store) BuildContext(agentID, role, taskTitle, complexity string) port.MemoryContext {
	coreMem := s.core.BuildContext(agentID, role, taskTitle, complexity)

	// Convert from the core agent.MemoryContext to port.MemoryContext.
	scratchpad := ""
	if coreMem.Scratchpad != nil {
		ctx, err := coreMem.Scratchpad.ReadForContext()
		if err == nil {
			scratchpad = ctx
		}
	}

	knownErrors := ""
	if coreMem.Resolved != nil {
		if matches, err := coreMem.Resolved.Search(taskTitle, role); err == nil {
			for _, m := range matches {
				knownErrors += m.ErrorPattern + ": " + m.ResolutionSummary + "\n"
			}
		}
	}

	return port.MemoryContext{
		ProjectDoc:  coreMem.ProjectDoc,
		AgentDoc:    coreMem.AgentDoc,
		ScopeDoc:    scopeToString(coreMem.Scope),
		RecentTasks: recentTasksToString(coreMem.RecentTasks),
		ScratchPad:  scratchpad,
		KnownErrors: knownErrors,
	}
}

// AppendProjectDoc delegates to the core memory store.
func (s *Store) AppendProjectDoc(section string) error {
	return s.core.AppendProjectDoc(section)
}

// scopeToString formats a ScopeManifest into a readable string.
func scopeToString(scope interface{}) string {
	if scope == nil {
		return ""
	}
	// ScopeManifest has a String() method or we format manually.
	return fmt.Sprintf("%v", scope)
}

// recentTasksToString formats recent tasks into a compact string.
func recentTasksToString(tasks interface{}) string {
	if tasks == nil {
		return ""
	}
	return fmt.Sprintf("%v", tasks)
}
