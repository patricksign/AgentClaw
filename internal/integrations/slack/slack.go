package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

const (
	maxResponseBytes = 4096
)

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

// NewSlackClient creates a SlackClient from the SLACK_WEBHOOK_URL env var.
// Returns an error if the var is empty.
func NewSlackClient() (*SlackClient, error) {
	webhookURL := os.Getenv("SLACK_WEBHOOK_URL")
	if webhookURL == "" {
		return nil, fmt.Errorf("slack: SLACK_WEBHOOK_URL is not set")
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
