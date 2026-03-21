package port

import (
	"context"

	"github.com/patricksign/AgentClaw/internal/domain"
)

// Notifier dispatches domain events to external channels (Telegram, Slack, etc.).
type Notifier interface {
	Dispatch(ctx context.Context, event domain.Event) error
}

// NoopNotifier is a no-op implementation used when no notification channel is configured.
type NoopNotifier struct{}

// Dispatch does nothing and always returns nil.
func (n *NoopNotifier) Dispatch(_ context.Context, _ domain.Event) error { return nil }
