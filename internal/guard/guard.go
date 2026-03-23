package guard

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/patricksign/AgentClaw/internal/domain"
	"log/slog"

	"github.com/patricksign/AgentClaw/internal/guard/patterns"
	"github.com/patricksign/AgentClaw/internal/port"
)

// Compile-time check.
var _ port.Guard = (*Engine)(nil)

// Engine is the central guard that enforces hard security boundaries.
// All checks are deterministic — no LLM, no network calls.
// Thread-safe: multiple goroutines can call Check* concurrently.
type Engine struct {
	bus            port.DomainEventBus // emit violation events (optional)
	violationCount uint64              // atomic — total violations since start
	mu             sync.Mutex          // protects agentViolations
	agentViolations map[string]int     // role → violation count
}

// NewEngine creates a guard engine. bus can be nil (violations are still logged).
func NewEngine(bus port.DomainEventBus) *Engine {
	return &Engine{
		bus:             bus,
		agentViolations: make(map[string]int),
	}
}

// CheckCommand validates a command before execution.
func (e *Engine) CheckCommand(_ context.Context, role, binary string, args []string) *port.Verdict {
	// 1. Role-based binary allowlist.
	if allowed, ok := patterns.RoleBinaryAllowlist[role]; ok {
		if len(allowed) == 0 {
			v := &port.Verdict{
				Blocked:  true,
				Rule:     "WF-001",
				Category: "workflow",
				Reason:   fmt.Sprintf("role %q is not allowed to execute any commands", role),
			}
			e.recordViolation(role, v)
			return v
		}
		found := false
		for _, b := range allowed {
			if b == binary {
				found = true
				break
			}
		}
		if !found {
			v := &port.Verdict{
				Blocked:  true,
				Rule:     "WF-002",
				Category: "workflow",
				Reason:   fmt.Sprintf("role %q is not allowed to execute %q", role, binary),
			}
			e.recordViolation(role, v)
			return v
		}
	}

	// 2. Pattern scan on full command string.
	full := binary + " " + strings.Join(args, " ")
	for _, p := range patterns.CommandPatterns {
		var match bool
		switch p.Targets {
		case "binary":
			match = p.Re.MatchString(binary)
		case "args":
			match = p.Re.MatchString(strings.Join(args, " "))
		default: // "full"
			match = p.Re.MatchString(full)
		}
		if match {
			v := &port.Verdict{
				Blocked:  true,
				Rule:     p.ID,
				Category: "command",
				Reason:   fmt.Sprintf("%s: %s", p.Reason, truncate(full, 200)),
			}
			e.recordViolation(role, v)
			return v
		}
	}

	return nil // allowed
}

// CheckFileWrite validates a file write before execution.
func (e *Engine) CheckFileWrite(_ context.Context, role, path, content string) *port.Verdict {
	// 1. Role-based path prefix check.
	if prefixes, ok := patterns.RoleWritePathPrefixes[role]; ok {
		if len(prefixes) == 0 {
			v := &port.Verdict{
				Blocked:  true,
				Rule:     "WF-010",
				Category: "workflow",
				Reason:   fmt.Sprintf("role %q is not allowed to write any files", role),
			}
			e.recordViolation(role, v)
			return v
		}
		allowed := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(path, prefix) {
				allowed = true
				break
			}
		}
		if !allowed {
			v := &port.Verdict{
				Blocked:  true,
				Rule:     "WF-011",
				Category: "workflow",
				Reason:   fmt.Sprintf("role %q cannot write to %q (allowed prefixes: %v)", role, path, prefixes),
			}
			e.recordViolation(role, v)
			return v
		}
	}

	// 2. Dangerous path patterns.
	overrides := patterns.PathRoleOverrides[role]
	for _, p := range patterns.PathPatterns {
		if p.Re.MatchString(path) {
			if overrides != nil && overrides[p.ID] {
				continue // role has explicit override for this pattern
			}
			v := &port.Verdict{
				Blocked:  true,
				Rule:     p.ID,
				Category: "path",
				Reason:   fmt.Sprintf("%s: %s", p.Reason, path),
			}
			e.recordViolation(role, v)
			return v
		}
	}

	// 3. Secret scanning in content.
	for _, p := range patterns.SecretPatterns {
		if p.Re.MatchString(content) {
			v := &port.Verdict{
				Blocked:  true,
				Rule:     p.ID,
				Category: "content",
				Reason:   fmt.Sprintf("file %q contains %s", path, p.Reason),
			}
			e.recordViolation(role, v)
			return v
		}
	}

	return nil
}

// CheckLLMInput scans prompt content for injection patterns.
func (e *Engine) CheckLLMInput(_ context.Context, input string) *port.Verdict {
	for _, p := range patterns.InjectionPatterns {
		if p.Re.MatchString(input) {
			v := &port.Verdict{
				Blocked:  true,
				Rule:     p.ID,
				Category: "prompt",
				Reason:   fmt.Sprintf("prompt injection detected: %s", p.Reason),
			}
			e.recordViolation("llm-input", v)
			return v
		}
	}
	return nil
}

// CheckLLMOutput scans LLM response for dangerous content.
func (e *Engine) CheckLLMOutput(_ context.Context, output string) *port.Verdict {
	// 1. Command patterns in LLM output (model trying to emit shell commands).
	for _, p := range patterns.CommandPatterns {
		if p.Re.MatchString(output) {
			v := &port.Verdict{
				Blocked:  true,
				Rule:     "OUT-" + p.ID,
				Category: "content",
				Reason:   fmt.Sprintf("LLM output contains dangerous command pattern: %s", p.Reason),
			}
			e.recordViolation("llm-output", v)
			return v
		}
	}

	// 2. Secret patterns in LLM output.
	for _, p := range patterns.SecretPatterns {
		if p.Re.MatchString(output) {
			v := &port.Verdict{
				Blocked:  true,
				Rule:     "OUT-" + p.ID,
				Category: "content",
				Reason:   fmt.Sprintf("LLM output contains %s", p.Reason),
			}
			e.recordViolation("llm-output", v)
			return v
		}
	}

	return nil
}

// CheckAPIInput validates external input before processing.
func (e *Engine) CheckAPIInput(_ context.Context, inputType string, data string) *port.Verdict {
	// 1. Length check.
	if maxLen, ok := patterns.MaxInputLength[inputType]; ok {
		if len(data) > maxLen {
			v := &port.Verdict{
				Blocked:  true,
				Rule:     "API-001",
				Category: "workflow",
				Reason:   fmt.Sprintf("%s exceeds max length (%d > %d)", inputType, len(data), maxLen),
			}
			e.recordViolation("api", v)
			return v
		}
	}

	// 2. Injection scan.
	for _, p := range patterns.InjectionPatterns {
		if p.Re.MatchString(data) {
			v := &port.Verdict{
				Blocked:  true,
				Rule:     "API-" + p.ID,
				Category: "prompt",
				Reason:   fmt.Sprintf("injection detected in %s: %s", inputType, p.Reason),
			}
			e.recordViolation("api", v)
			return v
		}
	}

	return nil
}

// ViolationCount returns the total number of violations since start.
func (e *Engine) ViolationCount() uint64 {
	return atomic.LoadUint64(&e.violationCount)
}

// AgentViolationCount returns the violation count for a specific role.
func (e *Engine) AgentViolationCount(role string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.agentViolations[role]
}

// recordViolation logs the violation, increments counters, and emits a domain event.
func (e *Engine) recordViolation(role string, v *port.Verdict) {
	atomic.AddUint64(&e.violationCount, 1)

	e.mu.Lock()
	e.agentViolations[role]++
	count := e.agentViolations[role]
	e.mu.Unlock()

	slog.Warn("guard: BLOCKED", "rule", v.Rule, "category", v.Category, "role", role, "role_violations", count, "reason", v.Reason)

	// Emit domain event if bus is available.
	if e.bus != nil {
		e.bus.Publish(domain.Event{
			Type:      domain.EventGuardViolation,
			Channel:   domain.HumanChannel,
			AgentRole: role,
			Payload: map[string]string{
				"rule":     v.Rule,
				"category": v.Category,
				"reason":   v.Reason,
			},
		})
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
