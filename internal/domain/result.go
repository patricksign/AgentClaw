package domain

// Artifact is a concrete output produced by a task execution.
type Artifact struct {
	Type    string `json:"type"` // "code" | "test" | "doc" | "plan"
	Path    string `json:"path"`
	Content string `json:"content"`
}

// TaskResult captures everything produced by a completed task.
type TaskResult struct {
	TaskID       string     `json:"task_id"`
	Output       string     `json:"output"`
	Artifacts    []Artifact `json:"artifacts"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	CostUSD      float64    `json:"cost_usd"`
	DurationMs   int64      `json:"duration_ms"`
	ModelUsed    string     `json:"model_used"`
}

// PhaseResult is the outcome of a single execution phase.
type PhaseResult struct {
	Done       bool        `json:"done"`
	Suspended  bool        `json:"suspended"`  // waiting for human answer
	Restarted  bool        `json:"restarted"`  // plan rejected, loop back to understand
	Err        error       `json:"-"`
	TaskResult *TaskResult `json:"task_result,omitempty"`
}

// EscalationResult captures the outcome of a question escalation.
type EscalationResult struct {
	Answer     string `json:"answer"`
	AnsweredBy string `json:"answered_by"` // "sonnet" | "opus" | "human" | "cache"
	Resolved   bool   `json:"resolved"`
	NeedsHuman bool   `json:"needs_human"`
}
