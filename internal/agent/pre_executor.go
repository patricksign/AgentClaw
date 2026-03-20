package agent

// pre_executor.go implements the Pre-execution Protocol for BaseAgent.
//
// Every task must pass three phases before implementation:
//   1. phaseUnderstand — agent restates the task, lists assumptions, risks, and questions
//   2. phaseClarify    — each question is resolved via escalation chain or human input
//   3. phasePlan       — agent writes an implementation plan; Opus reviews and approves/redirects
//
// Only after all three phases pass does phaseImplement run the actual LLM work.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/patricksign/agentclaw/internal/integrations/telegram"
	"github.com/patricksign/agentclaw/internal/llm"
	"github.com/patricksign/agentclaw/internal/state"
	"github.com/rs/zerolog/log"
)

// maxRedirects is the maximum number of Opus plan rejections before a task is failed.
const maxRedirects = 3

// understandTimeout caps the phaseUnderstand LLM call.
const understandTimeout = 60 * time.Second

// planTimeout caps both the agent plan LLM call and the Opus review call in phasePlan.
const planTimeout = 60 * time.Second

// escalationTimeout caps each tryAnswerAt call in the escalation chain.
const escalationTimeout = 30 * time.Second

// humanWaitTimeout is the maximum time to wait for a human answer in phaseClarify.
const humanWaitTimeout = 24 * time.Hour

// ─── PreExecutorDeps groups optional pre-executor dependencies ────────────────
// Injected once at startup via BaseAgent.SetPreExecutorDeps().

type PreExecutorDeps struct {
	Telegram   *telegram.DualChannelClient
	ReplyStore *ReplyStore
	SaveTask   func(*Task) error // bridge to memory.Store.SaveTask
	StateStore StateWriter       // optional — may be nil
}

// StateWriter is satisfied by state.AgentDocStore or any compatible writer.
// We use a minimal interface to avoid import cycles.
type StateWriter interface {
	WriteAgentState(s AgentState) error
}

// AgentState captures the current execution state for persistence.
type AgentState struct {
	AgentID    string
	Role       string
	Model      string
	Status     string // "running" | "blocked"
	TaskID     string
	TaskTitle  string
	Progress   string
	Blockers   string
	TimeStuck  string
	LastOutput string
}

// ─── BaseAgent wiring ─────────────────────────────────────────────────────────

// SetPreExecutorDeps injects the pre-execution dependencies into the agent.
// Call this once at startup after spawning agents.
func (a *BaseAgent) SetPreExecutorDeps(deps PreExecutorDeps) {
	a.mu.Lock()
	a.preExecDeps = &deps
	a.mu.Unlock()
}

func (a *BaseAgent) deps() *PreExecutorDeps {
	a.mu.RLock()
	d := a.preExecDeps
	a.mu.RUnlock()
	return d
}

// saveTask saves the task via the injected SaveTask bridge. Logs on error.
func (a *BaseAgent) saveTask(task *Task) {
	d := a.deps()
	if d == nil || d.SaveTask == nil {
		return
	}
	if err := d.SaveTask(task); err != nil {
		log.Error().Err(err).Str("task", task.ID).Msg("pre_executor: saveTask failed")
	}
}

// ─── Phase 1: Understand ──────────────────────────────────────────────────────

// phaseUnderstand calls the agent's model to restate the task, list assumptions,
// risks, and open questions. Sets task.Phase to PhaseClarify or PhasePlan.
func (a *BaseAgent) phaseUnderstand(ctx context.Context, task *Task, mem MemoryContext) error {
	task.Lock()
	taskID := task.ID
	taskTitle := task.Title
	taskDesc := task.Description
	task.Unlock()

	d := a.deps()
	if d != nil && d.Telegram != nil {
		d.Telegram.NotifyUnderstandStart(ctx, a.cfg.ID, taskID, taskTitle)
	}

	log.Info().Str("agent", a.cfg.ID).Str("task", taskID).Msg("phase: understand — start")

	system := `You are a senior engineer. Before writing any code, you must fully understand the task.
Analyze the task and return ONLY valid JSON, no markdown fences:
{
  "understanding": "restate the task in your own words",
  "assumptions": ["assumption 1", "assumption 2"],
  "risks": ["risk 1", "risk 2"],
  "questions": ["question if unclear"]
}
Return questions as empty array [] if everything is clear.
Be specific. Do not be vague.`

	projectCtx := mem.ProjectDoc
	if len(projectCtx) > 800 {
		projectCtx = projectCtx[:800]
	}
	agentDocCtx := mem.AgentDoc
	if len(agentDocCtx) > 400 {
		agentDocCtx = agentDocCtx[:400]
	}

	userMsg := fmt.Sprintf(
		"Task ID: %s\nTitle: %s\nDescription: %s\n\nProject context:\n%s\n\nYour role: %s\nKnown pitfalls for your role:\n%s",
		taskID, taskTitle, taskDesc, projectCtx, a.cfg.Role, agentDocCtx,
	)

	tCtx, cancel := context.WithTimeout(ctx, understandTimeout)
	defer cancel()

	content, err := a.callModel(tCtx, a.cfg.Model, system, userMsg, 1024)
	if err != nil {
		return fmt.Errorf("phaseUnderstand: llm call: %w", err)
	}

	parsed, err := parseUnderstandJSON(content)
	if err != nil {
		// Retry once with stricter prompt, using a fresh full-timeout context (E4).
		retryCtx, retryCancel := context.WithTimeout(ctx, understandTimeout)
		defer retryCancel()
		strictSystem := system + "\n\nIMPORTANT: Your previous response was not valid JSON. Return ONLY raw JSON, no backticks, no markdown."
		content2, err2 := a.callModel(retryCtx, a.cfg.Model, strictSystem, userMsg, 1024)
		if err2 != nil {
			return fmt.Errorf("phaseUnderstand: llm retry: %w", err2)
		}
		parsed, err = parseUnderstandJSON(content2)
		if err != nil {
			return fmt.Errorf("phaseUnderstand: parse json after retry: %w", err)
		}
	}

	task.Lock()
	task.Understanding = parsed.Understanding
	task.Assumptions = parsed.Assumptions
	task.Risks = parsed.Risks

	// Convert question strings into Question structs.
	for _, q := range parsed.Questions {
		if strings.TrimSpace(q) == "" {
			continue
		}
		task.Questions = append(task.Questions, Question{
			ID:        uuid.New().String()[:8],
			Text:      q,
			CreatedAt: time.Now(),
		})
	}

	if len(task.Questions) > 0 {
		task.Phase = PhaseClarify
	} else {
		task.Phase = PhasePlan
	}
	task.PhaseStartedAt = time.Now()
	questionCount := len(task.Questions)
	assumptions := task.Assumptions
	nextPhase := task.Phase // capture inside lock before Unlock (C1)
	task.Unlock()

	a.saveTask(task)

	log.Info().
		Str("agent", a.cfg.ID).
		Str("task", taskID).
		Str("next_phase", string(nextPhase)).
		Int("questions", questionCount).
		Msg("phase: understand — done")

	if d != nil && d.Telegram != nil {
		if questionCount == 0 {
			d.Telegram.NotifyUnderstandDoneNoQuestions(ctx, taskID, taskTitle, assumptions)
		} else {
			d.Telegram.NotifyUnderstandDoneWithQuestions(ctx, taskID, taskTitle, questionCount)
		}
	}

	return nil
}

// understandResult is the JSON structure returned by the understand LLM call.
type understandResult struct {
	Understanding string   `json:"understanding"`
	Assumptions   []string `json:"assumptions"`
	Risks         []string `json:"risks"`
	Questions     []string `json:"questions"`
}

func parseUnderstandJSON(content string) (*understandResult, error) {
	clean := stripMarkdownFences(content)
	var r understandResult
	if err := json.Unmarshal([]byte(clean), &r); err != nil {
		return nil, fmt.Errorf("parseUnderstandJSON: %w", err)
	}
	return &r, nil
}

// ─── Phase 2: Clarify ─────────────────────────────────────────────────────────

// phaseClarify resolves all open questions via: cache → escalation chain → human.
// Returns (true, nil) when all questions are resolved and task can move to PhasePlan.
// Returns (false, nil) when task is suspended waiting for human (caller should not retry immediately).
// Returns (false, err) on hard failure (timeout, LLM error).
func (a *BaseAgent) phaseClarify(ctx context.Context, task *Task, mem MemoryContext) (resolved bool, err error) {
	// Take a single deep-copy snapshot of the questions under the lock (C3).
	// This prevents races between phaseClarify iterations and concurrent goroutines
	// that might reassign task.Questions (e.g. RecoverSuspendedTasks, ResumeTask).
	task.Lock()
	taskID := task.ID
	taskTitle := task.Title
	understanding := task.Understanding
	snapshot := make([]Question, len(task.Questions))
	copy(snapshot, task.Questions)
	task.Unlock()

	totalQs := len(snapshot)
	d := a.deps()
	resolvedCount := 0

	taskContext := fmt.Sprintf("Task: %s\nUnderstanding: %s", taskTitle, understanding)

	for i := 0; i < totalQs; i++ {
		q := snapshot[i] // work from the snapshot — never re-read task.Questions[i] directly

		if q.Resolved {
			resolvedCount++
			continue
		}

		// ── Step 0: Check ResolvedStore cache ─────────────────────────────
		if mem.Resolved != nil {
			matches, _ := mem.Resolved.Search(q.Text, a.cfg.Role)
			if len(matches) > 0 && matches[0].OccurrenceCount >= 2 {
				// High confidence cached answer — use directly.
				task.Lock()
				setQuestionAnswer(task, q.ID, matches[0].ResolutionSummary, "cache")
				task.Unlock()
				a.saveTask(task)
				snapshot[i].Resolved = true
				resolvedCount++

				if d != nil && d.Telegram != nil {
					d.Telegram.NotifyClarifyFromCache(ctx, taskID, q.Text, matches[0].OccurrenceCount)
				}
				log.Info().Str("task", taskID).Str("question", q.ID).Msg("phaseClarify: resolved from cache")
				continue
			}
			if len(matches) > 0 && matches[0].OccurrenceCount == 1 {
				// Inject as context hint but still verify.
				taskContext = taskContext + "\n\nHint from previous similar question:\n" + matches[0].ResolutionSummary
			}
		}

		// ── Step 1: Level-1 escalation attempt ────────────────────────────
		level1 := a.escalationLevel()
		answer, confident, err := a.tryAnswerAt(ctx, level1, q.Text, taskContext)
		if err != nil {
			return false, fmt.Errorf("phaseClarify: level1 (%s): %w", level1, err)
		}
		if confident {
			task.Lock()
			setQuestionAnswer(task, q.ID, answer, level1)
			task.Unlock()
			a.saveTask(task)
			snapshot[i].Resolved = true
			resolvedCount++

			a.saveToResolvedStore(mem.Resolved, q.Text, answer, task)
			if d != nil && d.Telegram != nil {
				d.Telegram.NotifyClarifyResolved(ctx, taskID, q.Text, level1)
			}
			log.Info().Str("task", taskID).Str("q", q.ID).Str("by", level1).Msg("phaseClarify: resolved at level1")
			continue
		}

		// ── Step 2: Level-2 escalation attempt ────────────────────────────
		level2 := nextEscalationLevel(level1)
		if level2 != "human" {
			answer, confident, err = a.tryAnswerAt(ctx, level2, q.Text, taskContext)
			if err != nil {
				return false, fmt.Errorf("phaseClarify: level2 (%s): %w", level2, err)
			}
			if confident {
				task.Lock()
				setQuestionAnswer(task, q.ID, answer, level2)
				task.Unlock()
				a.saveTask(task)
				snapshot[i].Resolved = true
				resolvedCount++

				a.saveToResolvedStore(mem.Resolved, q.Text, answer, task)
				if d != nil && d.Telegram != nil {
					d.Telegram.NotifyClarifyResolved(ctx, taskID, q.Text, level2)
				}
				log.Info().Str("task", taskID).Str("q", q.ID).Str("by", level2).Msg("phaseClarify: resolved at level2")
				continue
			}
		}

		// ── Step 3: Escalate to human ──────────────────────────────────────
		if d == nil || d.Telegram == nil || d.ReplyStore == nil {
			// No Telegram configured — fail the task rather than hanging forever.
			return false, fmt.Errorf("phaseClarify: question %q requires human input but Telegram is not configured", q.Text)
		}

		escalationPath := fmt.Sprintf("%s → %s", level1, level2)
		if level2 == "human" {
			escalationPath = level1
		}

		// Register BEFORE sending the Telegram message to eliminate the TOCTOU
		// window where a fast human reply could arrive before the channel is ready (C2).
		// Use a placeholder msgID=0; we update the mapping after we get the real msgID.
		answerCh := d.ReplyStore.Register(0, taskID, q.ID)

		msgID, askErr := d.Telegram.AskHumanEscalated(ctx, a.cfg.ID, taskID, taskTitle, q.Text, escalationPath)
		if askErr != nil {
			// Clean up the pre-registered placeholder and fail (E1).
			d.ReplyStore.Unregister(0, taskID)
			return false, fmt.Errorf("phaseClarify: AskHumanEscalated: %w", askErr)
		}

		// Re-register under the real msgID so Resolve(msgID) can route replies.
		if msgID != 0 {
			d.ReplyStore.Reregister(0, msgID, taskID, q.ID)
		}

		d.Telegram.NotifyClarifyEscalatedToHuman(ctx, taskID, q.Text, escalationPath)

		// Persist blocked state.
		a.writeBlockedState(task, resolvedCount, totalQs, level2, q.Text)

		waitCtx, waitCancel := context.WithTimeout(ctx, humanWaitTimeout)
		var humanAnswer string
		var waitErr error
		select {
		case ans, ok := <-answerCh:
			waitCancel()
			if !ok {
				waitErr = fmt.Errorf("question %s reply channel closed", q.ID)
			} else {
				humanAnswer = ans
			}
		case <-waitCtx.Done():
			waitCancel()
			waitErr = waitCtx.Err()
		}

		if waitErr != nil {
			// Clean up orphaned ReplyStore entry to prevent memory leak.
			if msgID != 0 {
				d.ReplyStore.Unregister(msgID, taskID)
			}
			d.Telegram.NotifyQuestionExpired(ctx, taskID, taskTitle, q.Text)
			return false, fmt.Errorf("phaseClarify: question %s: %w", q.ID, waitErr)
		}

		task.Lock()
		setQuestionAnswer(task, q.ID, humanAnswer, "human")
		task.Unlock()
		a.saveTask(task)
		snapshot[i].Resolved = true
		resolvedCount++

		a.saveToResolvedStore(mem.Resolved, q.Text, humanAnswer, task)
		a.writeRunningState(task, "clarify complete, moving to plan")

		d.Telegram.NotifyAnswerReceived(ctx, taskID, taskTitle)
		log.Info().Str("task", taskID).Str("q", q.ID).Msg("phaseClarify: resolved by human")
	}

	// All questions resolved — move to plan.
	task.Lock()
	task.Phase = PhasePlan
	task.Unlock()
	a.saveTask(task)

	a.writeRunningState(task, "phase=plan, submitting implementation plan for Opus review")
	return true, nil
}

// ─── Phase 3: Plan ───────────────────────────────────────────────────────────

// phasePlan has the agent write an implementation plan then Opus reviews it.
// Returns (true, nil) when Opus approves (task.Phase = PhaseImplement).
// Returns (false, nil) when Opus redirects and task is reset to PhaseUnderstand.
// Returns (false, err) on hard failure.
func (a *BaseAgent) phasePlan(ctx context.Context, task *Task, mem MemoryContext) (approved bool, err error) {
	task.Lock()
	taskID := task.ID
	taskTitle := task.Title
	understanding := task.Understanding
	assumptions := make([]string, len(task.Assumptions))
	copy(assumptions, task.Assumptions)
	risks := make([]string, len(task.Risks))
	copy(risks, task.Risks)
	redirectCount := task.RedirectCount
	// Deep-copy questions to avoid races with concurrent slice replacement.
	questions := make([]Question, len(task.Questions))
	copy(questions, task.Questions)
	task.Unlock()

	d := a.deps()

	// ── Step A: Agent writes implementation plan ───────────────────────────
	planSystem := `You are a senior engineer. Write a detailed implementation plan.
Do NOT write any code yet. Return ONLY valid JSON:
{
  "plan": "step by step what you will do",
  "files_to_change": ["path/to/file.go"],
  "approach": "why you chose this approach",
  "edge_cases": ["edge case 1"],
  "test_cases": ["test case 1"]
}`

	// Format resolved Q&A pairs.
	var qaLines []string
	for _, q := range questions {
		if q.Resolved {
			qaLines = append(qaLines, fmt.Sprintf("Q: %s\nA: %s (answered by %s)", q.Text, q.Answer, q.AnsweredBy))
		}
	}

	planUser := fmt.Sprintf(
		"Task: %s\nUnderstanding: %s\nAssumptions: %s\nResolved questions and answers:\n%s\nRisks: %s",
		taskTitle,
		understanding,
		strings.Join(assumptions, "; "),
		strings.Join(qaLines, "\n"),
		strings.Join(risks, "; "),
	)

	tCtx, cancel := context.WithTimeout(ctx, planTimeout)
	defer cancel()

	planContent, err := a.callModel(tCtx, a.cfg.Model, planSystem, planUser, 2048)
	if err != nil {
		return false, fmt.Errorf("phasePlan: plan llm call: %w", err)
	}

	planResult, err := parsePlanJSON(planContent)
	if err != nil {
		return false, fmt.Errorf("phasePlan: parse plan json: %w", err)
	}

	task.Lock()
	task.ImplementPlan = planResult.Plan
	task.Unlock()
	a.saveTask(task)

	if d != nil && d.Telegram != nil {
		d.Telegram.NotifyPlanSubmitted(ctx, a.cfg.ID, taskID, taskTitle, planResult.FilesToChange)
	}

	// ── Step B: Opus reviews the plan ─────────────────────────────────────
	opusSystem := `You are Opus, the senior engineering supervisor for AgentClaw.
Review this implementation plan. If it is sound, respond with APPROVED.
If it needs changes, respond with REDIRECT: followed by specific guidance.
Be concise.`

	opusUser := fmt.Sprintf(
		"Agent: %s (role: %s)\nTask: %s\nPlan: %s\nFiles to change: %s",
		a.cfg.ID, a.cfg.Role,
		taskTitle,
		planResult.Plan,
		strings.Join(planResult.FilesToChange, ", "),
	)

	tCtx2, cancel2 := context.WithTimeout(ctx, planTimeout)
	defer cancel2()

	opusResp, err := a.callModel(tCtx2, "opus", opusSystem, opusUser, 1024)
	if err != nil {
		return false, fmt.Errorf("phasePlan: opus review call: %w", err)
	}

	opusResp = strings.TrimSpace(opusResp)

	if strings.HasPrefix(opusResp, "APPROVED") {
		task.Lock()
		task.PlanApprovedBy = "opus"
		task.Phase = PhaseImplement
		task.Unlock()
		a.saveTask(task)

		if d != nil && d.Telegram != nil {
			d.Telegram.NotifyPlanApproved(ctx, a.cfg.ID, taskID, taskTitle, planResult.Plan)
		}

		log.Info().Str("agent", a.cfg.ID).Str("task", taskID).Msg("phase: plan — approved by Opus")
		return true, nil
	}

	// Treat anything that starts with REDIRECT: (or anything not APPROVED) as redirect.
	guidance := strings.TrimPrefix(opusResp, "REDIRECT:")
	guidance = strings.TrimSpace(guidance)
	if guidance == "" {
		guidance = opusResp // fallback: pass full response as guidance
	}

	newRedirectCount := redirectCount + 1

	if newRedirectCount >= maxRedirects {
		task.Lock()
		task.RedirectCount = newRedirectCount
		task.Unlock()
		a.saveTask(task)

		if d != nil && d.Telegram != nil {
			d.Telegram.NotifyPlanRejectedFinal(ctx, taskID, taskTitle, guidance)
		}

		log.Error().Str("task", taskID).Int("redirects", newRedirectCount).
			Msg("phase: plan — rejected 3 times by Opus, failing task")
		return false, fmt.Errorf("plan rejected %d times by Opus", newRedirectCount)
	}

	// Sanitise Opus guidance before injecting into Description (S1).
	// This prevents a compromised or adversarially-prompted Opus response from
	// injecting instruction text that alters agent behaviour in subsequent phases.
	guidance = sanitiseGuidance(guidance)

	// Append Opus guidance to description so next phaseUnderstand has more context.
	task.Lock()
	task.Description = task.Description + "\n\n[Opus guidance — attempt " + fmt.Sprintf("%d", newRedirectCount) + "]: " + guidance
	task.Phase = PhaseUnderstand
	task.Understanding = ""
	task.Questions = []Question{}
	task.ImplementPlan = ""
	task.RedirectCount = newRedirectCount
	task.Unlock()
	a.saveTask(task)

	if d != nil && d.Telegram != nil {
		d.Telegram.NotifyPlanRedirected(ctx, a.cfg.ID, taskID, taskTitle, guidance, newRedirectCount, maxRedirects)
	}

	log.Info().Str("agent", a.cfg.ID).Str("task", taskID).
		Int("redirect", newRedirectCount).Msg("phase: plan — redirected by Opus")
	return false, nil
}

// planResult is the JSON structure returned by the plan LLM call.
type planResult struct {
	Plan          string   `json:"plan"`
	FilesToChange []string `json:"files_to_change"`
	Approach      string   `json:"approach"`
	EdgeCases     []string `json:"edge_cases"`
	TestCases     []string `json:"test_cases"`
}

func parsePlanJSON(content string) (*planResult, error) {
	clean := stripMarkdownFences(content)
	var r planResult
	if err := json.Unmarshal([]byte(clean), &r); err != nil {
		return nil, fmt.Errorf("parsePlanJSON: %w", err)
	}
	return &r, nil
}

// ─── Escalation chain helpers ─────────────────────────────────────────────────

// escalationLevel returns the first escalation level for this agent's model.
//
//	GLM/MiniMax → sonnet
//	sonnet       → opus
//	opus         → human (handled in phaseClarify directly)
func (a *BaseAgent) escalationLevel() string {
	switch a.cfg.Model {
	case "glm5", "glm-flash", "minimax", "minimax-highspeed":
		return "sonnet"
	case "sonnet":
		return "opus"
	case "opus":
		return "human"
	default:
		return "sonnet"
	}
}

// nextEscalationLevel returns the next level in the chain.
func nextEscalationLevel(level string) string {
	switch level {
	case "sonnet":
		return "opus"
	case "opus":
		return "human"
	default:
		return "human"
	}
}

// tryAnswerAt attempts to answer a question using the given model (30s timeout).
// Returns (answer, true, nil) if confident.
// Returns ("", false, nil) if the model says ESCALATE or the answer is too long.
// Returns ("", false, err) on LLM failure.
func (a *BaseAgent) tryAnswerAt(ctx context.Context, model, question, taskContext string) (answer string, confident bool, err error) {
	if model == "human" {
		return "", false, nil
	}

	system := `You are a senior engineer reviewing a question from a junior agent.
Answer concisely and specifically.
If you are not confident enough to give a correct answer, respond with exactly: ESCALATE
Do not guess. Do not over-explain.`

	ctx600 := taskContext
	if len(ctx600) > 600 {
		ctx600 = ctx600[:600]
	}
	userMsg := fmt.Sprintf("Question: %s\nTask context: %s", question, ctx600)

	tCtx, cancel := context.WithTimeout(ctx, escalationTimeout)
	defer cancel()

	resp, err := a.callModel(tCtx, model, system, userMsg, 512)
	if err != nil {
		return "", false, fmt.Errorf("tryAnswerAt(%s): %w", model, err)
	}

	resp = strings.TrimSpace(resp)
	if strings.HasPrefix(resp, "ESCALATE") || len(resp) > 800 {
		return "", false, nil
	}
	return resp, true, nil
}

// ─── callModel helper ─────────────────────────────────────────────────────────

// callModel makes a single LLM call using the agent's router.
// maxTokens caps the response length.
// Automatically enables prompt caching for Anthropic models to reduce costs.
func (a *BaseAgent) callModel(ctx context.Context, model, system, userMsg string, maxTokens int) (string, error) {
	req := llm.Request{
		Model:     model,
		System:    system,
		Messages:  []llm.Message{{Role: "user", Content: userMsg}},
		MaxTokens: maxTokens,
	}

	// Enable prompt caching for Anthropic models. System prompts in
	// phaseUnderstand, phaseClarify, and phasePlan are stable text templates
	// that benefit from caching (90% cost reduction on cache hits).
	switch model {
	case "opus", "sonnet", "haiku":
		req.CacheControl = &llm.CacheControl{
			CacheSystem: true,
			TTL:         "ephemeral",
		}
	}

	resp, err := a.router.Call(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// ─── State persistence helpers ────────────────────────────────────────────────

func (a *BaseAgent) writeBlockedState(task *Task, resolvedCount, totalCount int, escalatingTo, questionText string) {
	d := a.deps()
	if d == nil || d.StateStore == nil {
		return
	}
	task.Lock()
	state := AgentState{
		AgentID:   a.cfg.ID,
		Role:      a.cfg.Role,
		Model:     a.cfg.Model,
		Status:    "blocked",
		TaskID:    task.ID,
		TaskTitle: task.Title,
		Progress: fmt.Sprintf("phase=clarify, %d/%d questions resolved",
			resolvedCount, totalCount),
		Blockers: fmt.Sprintf("waiting answer from %s\nQuestion: %s",
			escalatingTo, questionText),
		TimeStuck:  time.Since(task.PhaseStartedAt).String(),
		LastOutput: truncate(task.Understanding, 400),
	}
	task.Unlock()
	if err := d.StateStore.WriteAgentState(state); err != nil {
		log.Warn().Err(err).Str("agent", a.cfg.ID).Msg("writeBlockedState failed")
	}
}

func (a *BaseAgent) writeRunningState(task *Task, progress string) {
	d := a.deps()
	if d == nil || d.StateStore == nil {
		return
	}
	task.Lock()
	s := AgentState{
		AgentID:   a.cfg.ID,
		Role:      a.cfg.Role,
		Model:     a.cfg.Model,
		Status:    "running",
		TaskID:    task.ID,
		TaskTitle: task.Title,
		Progress:  progress,
	}
	task.Unlock()
	if err := d.StateStore.WriteAgentState(s); err != nil {
		log.Warn().Err(err).Str("agent", a.cfg.ID).Msg("writeRunningState failed")
	}
}

// ─── ResolvedStore helpers ────────────────────────────────────────────────────

func (a *BaseAgent) saveToResolvedStore(rs *state.ResolvedStore, question, answer string, task *Task) {
	if rs == nil {
		return
	}
	task.Lock()
	role := task.AgentRole
	task.Unlock()

	answerPreview := answer
	if len(answerPreview) > 200 {
		answerPreview = answerPreview[:200]
	}

	pattern := state.ErrorPattern{
		ErrorPattern:      question,
		Tags:              []string{role, "clarification", "escalation"},
		AgentRoles:        []string{role},
		ResolutionSummary: answerPreview,
		Severity:          "low",
	}
	if err := rs.Save(pattern, fmt.Sprintf("# Clarification Q&A\n\nQuestion: %s\n\nAnswer: %s", question, answer)); err != nil {
		log.Warn().Err(err).Str("agent", a.cfg.ID).Msg("saveToResolvedStore failed")
	}
}

// ─── String helpers ───────────────────────────────────────────────────────────

// sanitiseGuidance caps Opus guidance length and removes common prompt-injection
// markers before the text is injected back into task.Description (S1).
func sanitiseGuidance(s string) string {
	const maxLen = 800
	if len(s) > maxLen {
		s = s[:maxLen] + "... [truncated]"
	}
	// Strip known injection markers.
	injection := []string{
		"SYSTEM:", "System:", "<system>", "</system>",
		"Ignore previous instructions", "ignore all previous",
		"```", "```json",
	}
	for _, marker := range injection {
		s = strings.ReplaceAll(s, marker, "")
	}
	return strings.TrimSpace(s)
}

// stripMarkdownFences removes leading/trailing markdown code fences from LLM responses.
func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	// Remove ```json ... ``` or ``` ... ```
	if strings.HasPrefix(s, "```") {
		// Find the end of the opening fence line.
		nl := strings.Index(s, "\n")
		if nl != -1 {
			s = s[nl+1:]
		}
		if strings.HasSuffix(s, "```") {
			s = s[:len(s)-3]
		}
		s = strings.TrimSpace(s)
	}
	return s
}

// truncate returns the first max runes of s. Safe for multi-byte UTF-8.
func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// setQuestionAnswer finds the question by ID in task.Questions and marks it resolved.
// Uses ID-based lookup instead of index to prevent races when the Questions slice
// is replaced by a concurrent phaseUnderstand (after Opus redirect). Caller must hold task.mu.
func setQuestionAnswer(task *Task, questionID, answer, answeredBy string) {
	for i := range task.Questions {
		if task.Questions[i].ID == questionID {
			task.Questions[i].Answer = answer
			task.Questions[i].AnsweredBy = answeredBy
			task.Questions[i].Resolved = true
			return
		}
	}
}
