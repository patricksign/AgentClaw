package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DualChannelClient sends notifications to two separate Telegram channels:
//   - statusChatID — automated pipeline events, short one-liners only
//   - humanChatID  — Q&A requiring human attention or Opus review results (full content)
//
// Required env vars:
//
//	TELEGRAM_BOT_TOKEN      — shared bot token
//	TELEGRAM_STATUS_CHAT_ID — status / pipeline channel
//	TELEGRAM_HUMAN_CHAT_ID  — human Q&A channel
type DualChannelClient struct {
	token        string
	statusChatID string
	humanChatID  string
	http         *http.Client
}

// NewDualChannelClient creates a DualChannelClient from env vars.
// Returns nil (not an error) if any env var is absent so callers can check
// IsConfigured() and skip notifications gracefully.
func NewDualChannelClient() *DualChannelClient {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	statusID := os.Getenv("TELEGRAM_STATUS_CHAT_ID")
	humanID := os.Getenv("TELEGRAM_HUMAN_CHAT_ID")
	if token == "" || statusID == "" || humanID == "" {
		return nil
	}
	return &DualChannelClient{
		token:        token,
		statusChatID: statusID,
		humanChatID:  humanID,
		http:         &http.Client{Timeout: 10 * time.Second, Transport: newTransport()},
	}
}

// IsConfigured reports whether the client has all required fields.
func (c *DualChannelClient) IsConfigured() bool {
	return c != nil && c.token != "" && c.statusChatID != "" && c.humanChatID != ""
}

// ─── Send helpers ─────────────────────────────────────────────────────────────

// SendStatus sends a short one-liner to statusChatID.
func (c *DualChannelClient) SendStatus(ctx context.Context, htmlText string) error {
	if !c.IsConfigured() {
		return nil
	}
	return c.sendTo(ctx, c.statusChatID, htmlText, 0)
}

// SendHuman sends full content to humanChatID.
func (c *DualChannelClient) SendHuman(ctx context.Context, htmlText string) error {
	if !c.IsConfigured() {
		return nil
	}
	return c.sendTo(ctx, c.humanChatID, htmlText, 0)
}

// AskHuman sends a question to humanChatID and returns the Telegram message ID
// so the caller can register it in ReplyStore. Returns (0, nil) if not configured.
func (c *DualChannelClient) AskHuman(ctx context.Context, agentID, taskID, taskTitle, questionText string) (int, error) {
	if !c.IsConfigured() {
		return 0, nil
	}
	msg := fmt.Sprintf(
		"🤔 <b>Needs human input</b>\n"+
			"<b>Task:</b> [%s] %s\n"+
			"<b>Asked by:</b> <code>%s</code>\n"+
			"<b>Question:</b> %s\n\n"+
			"Reply to this message with your answer.\n"+
			"Or use: /answer %s &lt;your answer&gt;",
		htmlEscape(taskID), htmlEscape(taskTitle),
		htmlEscape(agentID),
		htmlEscape(questionText),
		htmlEscape(taskID),
	)
	return c.sendToWithMsgID(ctx, c.humanChatID, msg)
}

// AskHumanEscalated sends a question with full escalation chain context to humanChatID.
func (c *DualChannelClient) AskHumanEscalated(ctx context.Context, agentID, taskID, taskTitle, questionText, escalationPath string) (int, error) {
	if !c.IsConfigured() {
		return 0, nil
	}
	msg := fmt.Sprintf(
		"🤔 <b>Escalation chain cannot resolve — needs human input</b>\n"+
			"<b>Task:</b> [%s] %s\n"+
			"<b>Asked by:</b> <code>%s</code>\n"+
			"<b>Escalation path:</b> %s → human\n"+
			"<b>Question:</b> %s\n\n"+
			"Reply to this message with your answer.\n"+
			"Or use: /answer %s &lt;your answer&gt;",
		htmlEscape(taskID), htmlEscape(taskTitle),
		htmlEscape(agentID),
		htmlEscape(escalationPath),
		htmlEscape(questionText),
		htmlEscape(taskID),
	)
	return c.sendToWithMsgID(ctx, c.humanChatID, msg)
}

// ─── Phase notifications ──────────────────────────────────────────────────────

// NotifyUnderstandStart sends the phase-start status notification.
func (c *DualChannelClient) NotifyUnderstandStart(ctx context.Context, agentID, taskID, taskTitle string) {
	_ = c.SendStatus(ctx, fmt.Sprintf(
		"🔍 <b>Understanding task</b>\n"+
			"<b>Agent:</b> <code>%s</code>\n"+
			"<b>Task:</b> [%s] %s\n"+
			"<b>Phase:</b> Understand",
		htmlEscape(agentID), htmlEscape(taskID), htmlEscape(taskTitle),
	))
}

// NotifyUnderstandDoneNoQuestions sends status when phase ends cleanly.
func (c *DualChannelClient) NotifyUnderstandDoneNoQuestions(ctx context.Context, taskID, taskTitle string, assumptions []string) {
	assumed := strings.Join(assumptions, ", ")
	if len(assumed) > 200 {
		assumed = assumed[:200] + "..."
	}
	_ = c.SendStatus(ctx, fmt.Sprintf(
		"✅ <b>Understood — no questions, moving to plan</b>\n"+
			"<b>Task:</b> [%s] %s\n"+
			"<b>Assumptions:</b> %s",
		htmlEscape(taskID), htmlEscape(taskTitle), htmlEscape(assumed),
	))
}

// NotifyUnderstandDoneWithQuestions sends status when questions are found.
func (c *DualChannelClient) NotifyUnderstandDoneWithQuestions(ctx context.Context, taskID, taskTitle string, questionCount int) {
	_ = c.SendStatus(ctx, fmt.Sprintf(
		"⏸ <b>Task paused — clarification needed</b>\n"+
			"<b>Task:</b> [%s] %s\n"+
			"<b>Questions:</b> %d questions pending",
		htmlEscape(taskID), htmlEscape(taskTitle), questionCount,
	))
}

// NotifyClarifyResolved sends status when a question is resolved by a model or cache.
func (c *DualChannelClient) NotifyClarifyResolved(ctx context.Context, taskID, questionText, resolvedBy string) {
	q := questionText
	if len(q) > 80 {
		q = q[:80] + "..."
	}
	_ = c.SendStatus(ctx, fmt.Sprintf(
		"🤖 <b>Question resolved by %s</b>\n"+
			"<b>Task:</b> [%s] — %s",
		htmlEscape(resolvedBy), htmlEscape(taskID), htmlEscape(q),
	))
}

// NotifyClarifyFromCache sends status when a question is resolved from the resolved store cache.
func (c *DualChannelClient) NotifyClarifyFromCache(ctx context.Context, taskID, questionText string, occurrenceCount int) {
	q := questionText
	if len(q) > 80 {
		q = q[:80] + "..."
	}
	_ = c.SendStatus(ctx, fmt.Sprintf(
		"📚 <b>Question resolved from cache (seen %d times)</b>\n"+
			"<b>Task:</b> [%s] — %s",
		occurrenceCount, htmlEscape(taskID), htmlEscape(q),
	))
}

// NotifyClarifyEscalatedToHuman sends to both channels when escalating to human.
func (c *DualChannelClient) NotifyClarifyEscalatedToHuman(ctx context.Context, taskID, questionText, chainSummary string) {
	q := questionText
	if len(q) > 100 {
		q = q[:100] + "..."
	}
	_ = c.SendStatus(ctx, fmt.Sprintf(
		"⏸ <b>Escalated to human after full chain</b>\n"+
			"<b>Task:</b> [%s] — %s\n"+
			"<b>Chain:</b> %s → human",
		htmlEscape(taskID), htmlEscape(q), htmlEscape(chainSummary),
	))
}

// NotifyAnswerReceived sends answer-received notifications to both channels.
func (c *DualChannelClient) NotifyAnswerReceived(ctx context.Context, taskID, taskTitle string) {
	_ = c.SendHuman(ctx, fmt.Sprintf(
		"✅ <b>Answer received</b>\n"+
			"<b>Task:</b> [%s] %s\n"+
			"Agent will resume shortly.",
		htmlEscape(taskID), htmlEscape(taskTitle),
	))
	_ = c.SendStatus(ctx, fmt.Sprintf(
		"▶️ <b>Task resumed</b>\n"+
			"<b>Task:</b> [%s] %s\n"+
			"<b>Answered by:</b> human",
		htmlEscape(taskID), htmlEscape(taskTitle),
	))
}

// NotifyQuestionExpired sends expiry notifications to both channels.
func (c *DualChannelClient) NotifyQuestionExpired(ctx context.Context, taskID, taskTitle, questionText string) {
	_ = c.SendHuman(ctx, fmt.Sprintf(
		"⚠️ <b>Question expired — task failed</b>\n"+
			"<b>Task:</b> [%s] %s\n"+
			"<b>Unanswered question:</b> %s",
		htmlEscape(taskID), htmlEscape(taskTitle), htmlEscape(questionText),
	))
	_ = c.SendStatus(ctx, fmt.Sprintf(
		"❌ <b>Task failed — clarification timeout 24h</b>\n"+
			"<b>Task:</b> [%s] %s",
		htmlEscape(taskID), htmlEscape(taskTitle),
	))
}

// NotifyPlanSubmitted sends status when plan is submitted to Opus.
func (c *DualChannelClient) NotifyPlanSubmitted(ctx context.Context, agentID, taskID, taskTitle string, filesToChange []string) {
	files := strings.Join(filesToChange, ", ")
	if len(files) > 150 {
		files = files[:150] + "..."
	}
	_ = c.SendStatus(ctx, fmt.Sprintf(
		"📋 <b>Plan submitted for Opus review</b>\n"+
			"<b>Agent:</b> <code>%s</code>\n"+
			"<b>Task:</b> [%s] %s\n"+
			"<b>Files to change:</b> %s",
		htmlEscape(agentID), htmlEscape(taskID), htmlEscape(taskTitle), htmlEscape(files),
	))
}

// NotifyPlanApproved sends notifications to both channels when Opus approves.
func (c *DualChannelClient) NotifyPlanApproved(ctx context.Context, agentID, taskID, taskTitle, plan string) {
	planPreview := plan
	if len(planPreview) > 300 {
		planPreview = planPreview[:300] + "..."
	}
	_ = c.SendHuman(ctx, fmt.Sprintf(
		"✅ <b>Plan Approved by Opus</b>\n"+
			"<b>Agent:</b> <code>%s</code>\n"+
			"<b>Task:</b> [%s] %s\n"+
			"<b>Plan:</b> %s\n\n"+
			"Implementation will begin now.",
		htmlEscape(agentID), htmlEscape(taskID), htmlEscape(taskTitle), htmlEscape(planPreview),
	))
	_ = c.SendStatus(ctx, fmt.Sprintf(
		"▶️ <b>Moving to implementation</b>\n"+
			"<b>Task:</b> [%s] %s",
		htmlEscape(taskID), htmlEscape(taskTitle),
	))
}

// NotifyPlanRedirected sends notifications to both channels when Opus rejects.
func (c *DualChannelClient) NotifyPlanRedirected(ctx context.Context, agentID, taskID, taskTitle, guidance string, attempt, maxAttempts int) {
	_ = c.SendHuman(ctx, fmt.Sprintf(
		"🔄 <b>Plan Redirected by Opus</b>\n"+
			"<b>Agent:</b> <code>%s</code>\n"+
			"<b>Task:</b> [%s] %s\n"+
			"<b>Opus guidance:</b> %s\n\n"+
			"Agent will restart from understand phase.\n"+
			"Attempt: %d/%d",
		htmlEscape(agentID), htmlEscape(taskID), htmlEscape(taskTitle),
		htmlEscape(guidance), attempt, maxAttempts,
	))
	_ = c.SendStatus(ctx, fmt.Sprintf(
		"🔄 <b>Plan rejected — restarting (attempt %d/%d)</b>\n"+
			"<b>Task:</b> [%s] %s",
		attempt, maxAttempts, htmlEscape(taskID), htmlEscape(taskTitle),
	))
}

// NotifyPlanRejectedFinal sends to humanChatID when 3-redirect limit is hit.
func (c *DualChannelClient) NotifyPlanRejectedFinal(ctx context.Context, taskID, taskTitle, lastGuidance string) {
	_ = c.SendHuman(ctx, fmt.Sprintf(
		"🚨 <b>Task failed — plan rejected 3 times</b>\n"+
			"<b>Task:</b> [%s] %s\n"+
			"<b>Last Opus guidance:</b> %s\n\n"+
			"Manual intervention required.\n"+
			"Use /opus: to investigate.",
		htmlEscape(taskID), htmlEscape(taskTitle), htmlEscape(lastGuidance),
	))
}

// NotifyImplementStart sends status when implementation begins.
func (c *DualChannelClient) NotifyImplementStart(ctx context.Context, agentID, taskID, taskTitle, model string) {
	_ = c.SendStatus(ctx, fmt.Sprintf(
		"⚙️ <b>Implementing</b>\n"+
			"<b>Agent:</b> <code>%s</code> (%s)\n"+
			"<b>Task:</b> [%s] %s",
		htmlEscape(agentID), htmlEscape(model),
		htmlEscape(taskID), htmlEscape(taskTitle),
	))
}

// NotifyImplementDone sends status when implementation completes successfully.
func (c *DualChannelClient) NotifyImplementDone(ctx context.Context, agentID, taskID, taskTitle string, inputTok, outputTok int64, costUSD float64, duration string) {
	_ = c.SendStatus(ctx, fmt.Sprintf(
		"✅ <b>Done</b>\n"+
			"<b>Agent:</b> <code>%s</code>\n"+
			"<b>Task:</b> [%s] %s\n"+
			"<b>Tokens:</b> %d in / %d out\n"+
			"<b>Cost:</b> $%.4f\n"+
			"<b>Duration:</b> %s",
		htmlEscape(agentID), htmlEscape(taskID), htmlEscape(taskTitle),
		inputTok, outputTok, costUSD, htmlEscape(duration),
	))
}

// NotifyImplementFailed sends status when implementation fails.
func (c *DualChannelClient) NotifyImplementFailed(ctx context.Context, agentID, taskID, taskTitle, errMsg string) {
	if len(errMsg) > 200 {
		errMsg = errMsg[:200] + "..."
	}
	_ = c.SendStatus(ctx, fmt.Sprintf(
		"❌ <b>Implementation failed</b>\n"+
			"<b>Agent:</b> <code>%s</code>\n"+
			"<b>Task:</b> [%s] %s\n"+
			"<b>Error:</b> %s",
		htmlEscape(agentID), htmlEscape(taskID), htmlEscape(taskTitle), htmlEscape(errMsg),
	))
}

// NotifyResumeAfterRestart sends to humanChatID when recovering a suspended task after restart.
func (c *DualChannelClient) NotifyResumeAfterRestart(ctx context.Context, taskID, taskTitle, questionText string, phaseStartedAt string) (int, error) {
	if !c.IsConfigured() {
		return 0, nil
	}
	msg := fmt.Sprintf(
		"🔁 <b>Resuming after restart — still waiting for answer</b>\n"+
			"<b>Task:</b> [%s] %s\n"+
			"<b>Question:</b> %s\n"+
			"<b>Stuck since:</b> %s\n\n"+
			"Reply to this message with your answer.\n"+
			"Or use: /answer %s &lt;your answer&gt;",
		htmlEscape(taskID), htmlEscape(taskTitle),
		htmlEscape(questionText),
		htmlEscape(phaseStartedAt),
		htmlEscape(taskID),
	)
	return c.sendToWithMsgID(ctx, c.humanChatID, msg)
}

// ─── Low-level send ───────────────────────────────────────────────────────────

func (c *DualChannelClient) sendTo(ctx context.Context, chatID, htmlText string, replyToMsgID int) error {
	_, err := c.sendToWithMsgIDReply(ctx, chatID, htmlText, replyToMsgID)
	return err
}

func (c *DualChannelClient) sendToWithMsgID(ctx context.Context, chatID, htmlText string) (int, error) {
	return c.sendToWithMsgIDReply(ctx, chatID, htmlText, 0)
}

// sendMessageResponse is the minimal Telegram sendMessage API response we care about.
type sendMessageResponse struct {
	OK     bool `json:"ok"`
	Result struct {
		MessageID int `json:"message_id"`
	} `json:"result"`
	Description string `json:"description"`
}

func (c *DualChannelClient) sendToWithMsgIDReply(ctx context.Context, chatID, htmlText string, replyToMsgID int) (int, error) {
	params := url.Values{}
	params.Set("chat_id", chatID)
	params.Set("text", htmlText)
	params.Set("parse_mode", "HTML")
	if replyToMsgID != 0 {
		params.Set("reply_to_message_id", fmt.Sprintf("%d", replyToMsgID))
	}

	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", telegramAPIBase, c.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(params.Encode()))
	if err != nil {
		return 0, fmt.Errorf("telegram dual: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("telegram dual: HTTP: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("telegram dual: API %d: %s", resp.StatusCode, raw)
	}

	var result sendMessageResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return 0, fmt.Errorf("telegram dual: parse response: %w", err)
	}
	if !result.OK {
		return 0, fmt.Errorf("telegram dual: API error: %s", result.Description)
	}
	return result.Result.MessageID, nil
}
