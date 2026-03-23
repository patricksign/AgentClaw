package port

import "context"

// Verdict is the result of a guard check.
type Verdict struct {
	Blocked  bool   // true = action must not proceed
	Rule     string // pattern/rule ID that triggered (e.g. "CMD-003")
	Category string // "command", "prompt", "path", "content", "workflow"
	Reason   string // human-readable explanation
}

// Guard enforces hard security boundaries.
// It does NOT trust the model, user input, or any external data source.
// All checks are deterministic (regex + rules), no LLM involved.
type Guard interface {
	// CheckCommand validates a command before execution.
	CheckCommand(ctx context.Context, role, binary string, args []string) *Verdict

	// CheckFileWrite validates a file write before execution.
	CheckFileWrite(ctx context.Context, role, path, content string) *Verdict

	// CheckLLMInput scans prompt content for injection patterns.
	CheckLLMInput(ctx context.Context, input string) *Verdict

	// CheckLLMOutput scans LLM response for dangerous content.
	CheckLLMOutput(ctx context.Context, output string) *Verdict

	// CheckAPIInput validates external input (task title, description, memory, etc.).
	CheckAPIInput(ctx context.Context, inputType string, data string) *Verdict
}
