package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/port"
	"github.com/patricksign/AgentClaw/internal/usecase/phase"
	"github.com/patricksign/AgentClaw/internal/usecase/reasoning"
)

const defaultMaxFixAttempts = 3

// TestFixOrchestrator implements the implement -> test -> fix cycle.
// Sonnet writes tests blind (without reading implementation), then the
// test runner executes them. If tests fail, MiniMax fixes the code and
// tests are re-run. Max 3 attempts before escalation.
type TestFixOrchestrator struct {
	runner       *phase.Runner
	llmRouter    port.LLMRouter
	cmdRunner    port.CommandRunner
	notifier     port.Notifier
	fileWriter   port.FileWriter
	maxAttempts  int
}

// NewTestFixOrchestrator creates a test-fix loop orchestrator.
func NewTestFixOrchestrator(
	runner *phase.Runner,
	llmRouter port.LLMRouter,
	cmdRunner port.CommandRunner,
	notifier port.Notifier,
	fileWriter port.FileWriter,
	maxAttempts int,
) *TestFixOrchestrator {
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxFixAttempts
	}
	return &TestFixOrchestrator{
		runner:      runner,
		llmRouter:   llmRouter,
		cmdRunner:   cmdRunner,
		notifier:    notifier,
		fileWriter:  fileWriter,
		maxAttempts: maxAttempts,
	}
}

// Run executes the implement -> test -> fix cycle for a task.
// workDir is the project root where code lives and tests run.
// testCmd/testArgs define how to run tests (e.g. "go", ["test", "./..."]).
func (t *TestFixOrchestrator) Run(
	ctx context.Context,
	task *domain.Task,
	pctx phase.PhaseContext,
	workDir string,
	testCmd string,
	testArgs []string,
) (*domain.TaskResult, error) {

	// Step 1: Run implement phase (already completed by this point — get result).
	implResult, err := t.runner.Run(ctx, pctx)
	if err != nil {
		return nil, fmt.Errorf("test_fix: implement: %w", err)
	}
	if implResult == nil {
		return nil, fmt.Errorf("test_fix: implement returned nil (suspended)")
	}

	for attempt := 1; attempt <= t.maxAttempts; attempt++ {
		// Step 2: Run tests.
		_ = t.notifier.Dispatch(ctx, domain.Event{
			Type:       domain.EventPhaseTransition,
			Channel:    domain.StatusChannel,
			TaskID:     task.ID,
			Payload:    map[string]string{"message": fmt.Sprintf("Running tests — attempt %d/%d", attempt, t.maxAttempts)},
			OccurredAt: time.Now(),
		})

		testResult, testErr := t.cmdRunner.Run(ctx, workDir, testCmd, testArgs...)
		if testErr != nil {
			return nil, fmt.Errorf("test_fix: run tests: %w", testErr)
		}

		// Tests pass.
		if testResult.ExitCode == 0 {
			_ = t.notifier.Dispatch(ctx, domain.Event{
				Type:       domain.EventTaskDone,
				Channel:    domain.StatusChannel,
				TaskID:     task.ID,
				Payload:    map[string]string{"message": fmt.Sprintf("Tests passed on attempt %d", attempt)},
				OccurredAt: time.Now(),
			})
			return implResult, nil
		}

		// Tests fail — last attempt, don't try to fix.
		if attempt >= t.maxAttempts {
			break
		}

		// Step 3: Ask MiniMax to fix based on test error output.
		// Performance: send only the error output, not the entire codebase.
		_ = t.notifier.Dispatch(ctx, domain.Event{
			Type:       domain.EventPhaseTransition,
			Channel:    domain.StatusChannel,
			TaskID:     task.ID,
			Payload:    map[string]string{"message": fmt.Sprintf("Tests failed — requesting fix (attempt %d/%d)", attempt, t.maxAttempts)},
			OccurredAt: time.Now(),
		})

		fixPrompt := buildFixPrompt(testResult, attempt)
		fixCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		fixReq := reasoning.WithThinking(port.LLMRequest{
			Model:     domain.WorkerModelForComplexity(task.Complexity),
			System:    "You are a senior engineer. Fix the code based on the test failure output. Return ONLY the corrected code with file paths.",
			Messages:  []port.LLMMessage{{Role: "user", Content: fixPrompt}},
			MaxTokens: 8192,
			TaskID:    task.ID,
		}, domain.PhaseImplement, task.Complexity)
		fixResp, fixErr := t.llmRouter.Call(fixCtx, fixReq)
		cancel()

		if fixErr != nil {
			return nil, fmt.Errorf("test_fix: fix attempt %d: %w", attempt, fixErr)
		}

		// Update implementation result with the fix.
		implResult.Output = fixResp.Content
		implResult.InputTokens += fixResp.InputTokens
		implResult.OutputTokens += fixResp.OutputTokens
		implResult.ThinkingTokens += fixResp.ThinkingTokens
		implResult.CostUSD += fixResp.CostUSD
	}

	// Exhausted all attempts.
	_ = t.notifier.Dispatch(ctx, domain.Event{
		Type:       domain.EventTaskFailed,
		Channel:    domain.HumanChannel,
		TaskID:     task.ID,
		Payload:    map[string]string{"message": fmt.Sprintf("Tests still failing after %d fix attempts — needs human intervention", t.maxAttempts)},
		OccurredAt: time.Now(),
	})

	return implResult, fmt.Errorf("test_fix: tests still failing after %d attempts", t.maxAttempts)
}

// buildFixPrompt creates the prompt for the fix LLM call.
// Sends only the test error output (not the full code) for token efficiency.
func buildFixPrompt(testResult port.CommandResult, attempt int) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Test failure (attempt %d). Fix the code.\n\n", attempt))
	sb.WriteString("--- STDOUT ---\n")
	// Cap stdout to ~2000 chars to control token usage.
	// Use strings.Clone to avoid retaining the full backing array (#41 substring leak).
	stdout := testResult.Stdout
	if len(stdout) > 2000 {
		stdout = strings.Clone(stdout[:2000]) + "\n... [truncated]"
	}
	sb.WriteString(stdout)
	sb.WriteString("\n\n--- STDERR ---\n")
	stderr := testResult.Stderr
	if len(stderr) > 2000 {
		stderr = strings.Clone(stderr[:2000]) + "\n... [truncated]"
	}
	sb.WriteString(stderr)
	return sb.String()
}
