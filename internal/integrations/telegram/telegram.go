// Package telegram provides Telegram Bot API notifications and an optional
// Slack incoming-webhook client for the AgentClaw pipeline.
//
// Required env vars (Telegram):
//
//	TELEGRAM_BOT_TOKEN — bot token from @BotFather
//	TELEGRAM_CHAT_ID   — target chat / channel ID
//
// Optional env var (Slack):
//
//	SLACK_WEBHOOK_URL — incoming webhook URL
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	telegramAPIBase  = "https://api.telegram.org"
	maxResponseBytes = 4096
)

// Client sends notifications via the Telegram Bot API using HTML parse mode.
type Client struct {
	token  string
	chatID string
	http   *http.Client
}

// SlackClient posts messages to a Slack incoming webhook.
type SlackClient struct {
	webhookURL string
	http       *http.Client
}

func newTransport() *http.Transport {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp4", addr)
		},
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        5,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     60 * time.Second,
	}
}

// IsEnvPresent reports whether at least one Telegram env var is set.
func IsEnvPresent() bool {
	return os.Getenv("TELEGRAM_BOT_TOKEN") != "" || os.Getenv("TELEGRAM_CHAT_ID") != ""
}

// IsSlackEnvPresent reports whether the Slack webhook env var is set.
func IsSlackEnvPresent() bool {
	return os.Getenv("SLACK_WEBHOOK_URL") != ""
}

// New creates a Telegram Client from env vars.
// Returns an error if the required vars are missing.
func New() (*Client, error) {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")
	if token == "" {
		return nil, fmt.Errorf("telegram: TELEGRAM_BOT_TOKEN is not set")
	}
	if chatID == "" {
		return nil, fmt.Errorf("telegram: TELEGRAM_CHAT_ID is not set")
	}
	return &Client{
		token:  token,
		chatID: chatID,
		http:   &http.Client{Timeout: 10 * time.Second, Transport: newTransport()},
	}, nil
}

// IsConfigured reports whether the Telegram client has both required vars.
func (c *Client) IsConfigured() bool {
	return c != nil && c.token != "" && c.chatID != ""
}

// NewSlackClient creates a SlackClient from the SLACK_WEBHOOK_URL env var.
// Returns an error if the var is empty.
func NewSlackClient() (*SlackClient, error) {
	webhookURL := os.Getenv("SLACK_WEBHOOK_URL")
	if webhookURL == "" {
		return nil, fmt.Errorf("telegram: SLACK_WEBHOOK_URL is not set")
	}
	return &SlackClient{
		webhookURL: webhookURL,
		http:       &http.Client{Timeout: 10 * time.Second, Transport: newTransport()},
	}, nil
}

// IsConfigured reports whether the Slack webhook URL is set.
func (s *SlackClient) IsConfigured() bool {
	return s != nil && s.webhookURL != ""
}

// ─── Telegram notification methods ───────────────────────────────────────────

// SendRaw sends an arbitrary HTML-formatted message.
func (c *Client) SendRaw(ctx context.Context, htmlText string) error {
	params := url.Values{}
	params.Set("chat_id", c.chatID)
	params.Set("text", htmlText)
	params.Set("parse_mode", "HTML")

	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", telegramAPIBase, c.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(params.Encode()))
	if err != nil {
		return fmt.Errorf("telegram: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: HTTP: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram: API %d: %s", resp.StatusCode, raw)
	}
	return nil
}

// NotifyTaskStarted notifies that an agent has picked up a task.
func (c *Client) NotifyTaskStarted(ctx context.Context, agentName, taskID, taskTitle, model string) error {
	msg := fmt.Sprintf(
		"🚀 <b>Task Started</b>\n"+
			"Agent: <code>%s</code>\n"+
			"Task: <code>%s</code> — %s\n"+
			"Model: <code>%s</code>",
		htmlEscape(agentName), htmlEscape(taskID), htmlEscape(taskTitle), htmlEscape(model),
	)
	return c.SendRaw(ctx, msg)
}

// NotifyTaskDone notifies task completion with token and cost stats.
func (c *Client) NotifyTaskDone(ctx context.Context, agentName, taskID, taskTitle string, inputTok, outputTok int64, costUSD float64, durationMs int64) error {
	msg := fmt.Sprintf(
		"✅ <b>Task Done</b>\n"+
			"Agent: <code>%s</code>\n"+
			"Task: <code>%s</code> — %s\n"+
			"Tokens: %d in / %d out | Cost: $%.4f | Time: %dms",
		htmlEscape(agentName), htmlEscape(taskID), htmlEscape(taskTitle),
		inputTok, outputTok, costUSD, durationMs,
	)
	return c.SendRaw(ctx, msg)
}

// NotifyTaskFailed notifies a task failure with the reason.
func (c *Client) NotifyTaskFailed(ctx context.Context, agentName, taskID, taskTitle, reason string) error {
	msg := fmt.Sprintf(
		"❌ <b>Task Failed</b>\n"+
			"Agent: <code>%s</code>\n"+
			"Task: <code>%s</code> — %s\n"+
			"Reason: %s",
		htmlEscape(agentName), htmlEscape(taskID), htmlEscape(taskTitle), htmlEscape(reason),
	)
	return c.SendRaw(ctx, msg)
}

// NotifyPRCreated notifies that a draft PR has been created.
func (c *Client) NotifyPRCreated(ctx context.Context, taskID, prTitle, prURL string, prNumber int) error {
	msg := fmt.Sprintf(
		"🔀 <b>PR Created</b>\n"+
			"Task: <code>%s</code>\n"+
			"PR #%d: <a href=\"%s\">%s</a>",
		htmlEscape(taskID), prNumber, prURL, htmlEscape(prTitle),
	)
	return c.SendRaw(ctx, msg)
}

// NotifyPRReviewed notifies that a PR review has been submitted.
func (c *Client) NotifyPRReviewed(ctx context.Context, prNumber int, prTitle, verdict, summary string) error {
	msg := fmt.Sprintf(
		"🔍 <b>PR Reviewed</b>\n"+
			"PR #%d: %s\n"+
			"Verdict: <b>%s</b>\n"+
			"%s",
		prNumber, htmlEscape(prTitle), htmlEscape(verdict), htmlEscape(summary),
	)
	return c.SendRaw(ctx, msg)
}

// NotifyPRMerged notifies that a PR has been merged.
func (c *Client) NotifyPRMerged(ctx context.Context, prNumber int, prTitle, mergedBy string) error {
	msg := fmt.Sprintf(
		"🎉 <b>PR Merged</b>\n"+
			"PR #%d: %s\n"+
			"Merged by: <code>%s</code>",
		prNumber, htmlEscape(prTitle), htmlEscape(mergedBy),
	)
	return c.SendRaw(ctx, msg)
}

// NotifyDailySummary sends the daily usage summary.
func (c *Client) NotifyDailySummary(ctx context.Context, date string, totalTasks, doneTasks int, totalCostUSD float64, totalTokens int64) error {
	msg := fmt.Sprintf(
		"📊 <b>Daily Summary — %s</b>\n"+
			"Tasks: %d done / %d total\n"+
			"Tokens: %d | Cost: $%.4f",
		htmlEscape(date), doneTasks, totalTasks, totalTokens, totalCostUSD,
	)
	return c.SendRaw(ctx, msg)
}

// NotifyChecklistComplete notifies that all checklist items on a card are done.
func (c *Client) NotifyChecklistComplete(ctx context.Context, cardName, cardURL string, itemCount int) error {
	msg := fmt.Sprintf(
		"✅ <b>Pipeline Complete</b>\n"+
			"Card: <a href=\"%s\">%s</a>\n"+
			"%d tasks finished",
		cardURL, htmlEscape(cardName), itemCount,
	)
	return c.SendRaw(ctx, msg)
}

// ─── Slack ────────────────────────────────────────────────────────────────────

// Post sends a plain-text message to the Slack webhook.
func (s *SlackClient) Post(ctx context.Context, text string) error {
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return fmt.Errorf("slack: marshal payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL,
		bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("slack: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("slack: HTTP: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack: API %d: %s", resp.StatusCode, raw)
	}
	return nil
}

// htmlEscape escapes <, >, and & so they render correctly in HTML parse mode.
func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
