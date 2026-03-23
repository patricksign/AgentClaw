package guard

import (
	"context"
	"fmt"

	"github.com/patricksign/AgentClaw/internal/port"
)

// Compile-time check.
var _ port.CommandRunner = (*GuardedRunner)(nil)

// GuardedRunner wraps a port.CommandRunner with guard checks.
// Every command is scanned before execution — blocked commands never reach the OS.
type GuardedRunner struct {
	inner port.CommandRunner
	guard port.Guard
	role  string
}

// NewGuardedRunner wraps an existing CommandRunner with guard enforcement.
func NewGuardedRunner(inner port.CommandRunner, guard port.Guard, role string) *GuardedRunner {
	return &GuardedRunner{inner: inner, guard: guard, role: role}
}

// Run checks the command against guard rules before executing.
func (g *GuardedRunner) Run(ctx context.Context, workDir, binary string, args ...string) (port.CommandResult, error) {
	if v := g.guard.CheckCommand(ctx, g.role, binary, args); v != nil && v.Blocked {
		return port.CommandResult{ExitCode: -1}, fmt.Errorf("guard: blocked [%s] %s", v.Rule, v.Reason)
	}
	return g.inner.Run(ctx, workDir, binary, args...)
}
