package guard

import (
	"context"
	"fmt"

	"github.com/patricksign/AgentClaw/internal/port"
)

// Compile-time check.
var _ port.FileWriter = (*GuardedWriter)(nil)

// GuardedWriter wraps a port.FileWriter with guard checks.
// Every file write is scanned for path safety and content secrets.
type GuardedWriter struct {
	inner port.FileWriter
	guard port.Guard
	role  string
}

// NewGuardedWriter wraps an existing FileWriter with guard enforcement.
func NewGuardedWriter(inner port.FileWriter, guard port.Guard, role string) *GuardedWriter {
	return &GuardedWriter{inner: inner, guard: guard, role: role}
}

// WriteFile checks the path and content against guard rules before writing.
func (g *GuardedWriter) WriteFile(relativePath, content string) error {
	if v := g.guard.CheckFileWrite(context.Background(), g.role, relativePath, content); v != nil && v.Blocked {
		return fmt.Errorf("guard: blocked [%s] %s", v.Rule, v.Reason)
	}
	return g.inner.WriteFile(relativePath, content)
}

// MkdirAll delegates directly — directory creation is low risk.
func (g *GuardedWriter) MkdirAll(relativePath string) error {
	return g.inner.MkdirAll(relativePath)
}
