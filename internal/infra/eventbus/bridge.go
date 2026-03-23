package eventbus

import (
	"fmt"
	"sync"
	"time"

	"github.com/patricksign/AgentClaw/internal/adapter"
	"github.com/patricksign/AgentClaw/internal/domain"
	"github.com/patricksign/AgentClaw/internal/port"
	"log/slog"
)

// legacyMapping maps adapter.EventType → domain.EventType.
// Only events with a clean-arch equivalent are bridged.
var legacyMapping = map[adapter.EventType]domain.EventType{
	adapter.EvtTaskStarted:   domain.EventTaskStarted,
	adapter.EvtTaskDone:      domain.EventTaskDone,
	adapter.EvtTaskFailed:    domain.EventTaskFailed,
	adapter.EvtPRCreated:     domain.EventPRCreated,
	adapter.EvtDeployStarted: domain.EventDeployStarted,
	adapter.EvtDeployDone:    domain.EventDeployDone,
}

// LegacyBridge subscribes to the legacy adapter.EventBus, converts
// adapter.Event → domain.Event, and re-publishes on DomainEventBus.
// Temporary — remove once agent/ is fully migrated to domain events.
type LegacyBridge struct {
	legacy port.EventSubscriber
	domain port.DomainEventBus
	unsub  func()
	done   chan struct{}
	once   sync.Once
}

// NewLegacyBridge creates and starts the bridge.
func NewLegacyBridge(legacy port.EventSubscriber, domainBus port.DomainEventBus) *LegacyBridge {
	b := &LegacyBridge{
		legacy: legacy,
		domain: domainBus,
		done:   make(chan struct{}),
	}
	b.start()
	return b
}

// Stop shuts down the bridge goroutine and waits for it to exit.
func (b *LegacyBridge) Stop() {
	b.once.Do(func() {
		if b.unsub != nil {
			b.unsub() // closes the legacy channel, causing the goroutine to exit
		}
		<-b.done // wait for goroutine to finish
	})
}

func (b *LegacyBridge) start() {
	ch, unsub := b.legacy.Subscribe("domain-bridge")
	b.unsub = unsub

	go func() {
		defer close(b.done)
		for legacyEvt := range ch {
			domainType, ok := legacyMapping[legacyEvt.Type]
			if !ok {
				continue
			}

			payload := make(map[string]string)
			if legacyEvt.Payload != nil {
				payload["raw"] = fmt.Sprintf("%v", legacyEvt.Payload)
			}

			evt := domain.Event{
				Type:       domainType,
				Channel:    domain.StatusChannel,
				TaskID:     legacyEvt.TaskID,
				AgentID:    legacyEvt.AgentID,
				Payload:    payload,
				OccurredAt: legacyEvt.Timestamp,
			}
			if evt.OccurredAt.IsZero() {
				evt.OccurredAt = time.Now()
			}

			b.domain.Publish(evt)
			slog.Debug("bridge: converted legacy → domain event", "legacy_type", string(legacyEvt.Type), "domain_type", string(domainType), "task_id", legacyEvt.TaskID)
		}
	}()
}
