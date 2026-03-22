package preflight

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/port"
)

// Checker verifies that all required tools are installed before pipeline execution.
// Missing tools are reported to the human via Telegram. Install is NOT automatic —
// only hardcoded install hints are shown; no LLM-generated commands are executed.
type Checker struct {
	runner   port.CommandRunner
	notifier port.Notifier
}

// NewChecker creates a preflight checker.
func NewChecker(runner port.CommandRunner, notifier port.Notifier) *Checker {
	return &Checker{runner: runner, notifier: notifier}
}

// Check verifies all tools required for the project config.
// Runs checks in parallel for performance. Returns the list of results
// and an error if any required tool is missing.
func (c *Checker) Check(ctx context.Context, cfg domain.ProjectConfig) ([]domain.PreflightResult, error) {
	tools := domain.ToolsForProject(cfg)
	if len(tools) == 0 {
		return nil, nil
	}

	results := make([]domain.PreflightResult, len(tools))

	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(8) // parallel tool checks

	for i, tool := range tools {
		group.Go(func() error {
			results[i] = c.checkTool(gctx, tool, cfg.WorkDir)
			return nil // never fail the group — collect all results
		})
	}

	_ = group.Wait()

	// Report results.
	var missing []string
	for _, r := range results {
		if !r.Installed && r.Tool.Required {
			missing = append(missing, fmt.Sprintf("- %s: %s (install: %s)",
				r.Tool.Name, r.Error, r.Tool.InstallHint))
		}
	}

	if len(missing) > 0 {
		msg := fmt.Sprintf("Preflight check failed. Missing required tools:\n%s", strings.Join(missing, "\n"))
		_ = c.notifier.Dispatch(ctx, domain.Event{
			Type:       domain.EventTaskFailed,
			Channel:    domain.HumanChannel,
			Payload:    map[string]string{"message": msg},
			OccurredAt: time.Now(),
		})
		return results, fmt.Errorf("preflight: %d required tools missing:\n%s", len(missing), strings.Join(missing, "\n"))
	}

	// Success notification.
	_ = c.notifier.Dispatch(ctx, domain.Event{
		Type:       domain.EventPhaseTransition,
		Channel:    domain.StatusChannel,
		Payload:    map[string]string{"message": fmt.Sprintf("Preflight OK: %d tools verified", len(tools))},
		OccurredAt: time.Now(),
	})

	return results, nil
}

// CloneRepo clones the project repository into WorkDir.
func (c *Checker) CloneRepo(ctx context.Context, cfg domain.ProjectConfig) error {
	if cfg.RepoURL == "" {
		return fmt.Errorf("preflight: repo_url is empty")
	}
	if cfg.WorkDir == "" {
		return fmt.Errorf("preflight: work_dir is empty")
	}

	result, err := c.runner.Run(ctx, "", "git", "clone", cfg.RepoURL, cfg.WorkDir)
	if err != nil {
		return fmt.Errorf("preflight: git clone: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("preflight: git clone failed (exit %d): %s", result.ExitCode, result.Stderr)
	}

	return nil
}

// checkTool runs the tool's check command and returns the result.
func (c *Checker) checkTool(ctx context.Context, tool domain.ToolRequirement, workDir string) domain.PreflightResult {
	result, err := c.runner.Run(ctx, workDir, tool.CheckCmd, tool.CheckArgs...)
	if err != nil {
		return domain.PreflightResult{
			Tool:      tool,
			Installed: false,
			Error:     err.Error(),
		}
	}

	if result.ExitCode != 0 {
		return domain.PreflightResult{
			Tool:      tool,
			Installed: false,
			Error:     fmt.Sprintf("exit code %d: %s", result.ExitCode, result.Stderr),
		}
	}

	// Extract version from first line of stdout.
	// Use strings.Clone to avoid retaining full stdout backing array (#41).
	version := result.Stdout
	if idx := strings.IndexByte(version, '\n'); idx != -1 {
		version = strings.Clone(version[:idx])
	}

	return domain.PreflightResult{
		Tool:      tool,
		Installed: true,
		Version:   version,
	}
}
