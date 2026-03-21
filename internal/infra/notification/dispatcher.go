package notification

import (
	"context"

	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/integrations/telegram"
	"github.com/patricksign/AgentClaw/internal/port"
)

// Compile-time check: TelegramDispatcher implements port.Notifier.
var _ port.Notifier = (*TelegramDispatcher)(nil)

// TelegramDispatcher routes domain events to the appropriate Telegram channel
// (status or human) based on the event's Channel field.
type TelegramDispatcher struct {
	tg *telegram.DualChannelClient
}

// NewTelegramDispatcher creates a dispatcher wrapping the dual-channel Telegram client.
// Returns a no-op dispatcher if tg is nil.
func NewTelegramDispatcher(tg *telegram.DualChannelClient) *TelegramDispatcher {
	return &TelegramDispatcher{tg: tg}
}

// Dispatch formats the event and sends it to the appropriate Telegram channel.
func (d *TelegramDispatcher) Dispatch(ctx context.Context, event domain.Event) error {
	if d.tg == nil || !d.tg.IsConfigured() {
		return nil
	}

	text, channel := Format(event)

	switch channel {
	case domain.StatusChannel:
		return d.tg.SendStatus(ctx, text)
	case domain.HumanChannel:
		return d.tg.SendHuman(ctx, text)
	default:
		return d.tg.SendStatus(ctx, text)
	}
}
