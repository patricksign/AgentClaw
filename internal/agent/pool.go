package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// deepCopyConfig returns a Config with independently copied slice and map fields
// so that mutations of the original do not affect the saved restart config.
func deepCopyConfig(c Config) Config {
	if c.Tags != nil {
		tags := make([]string, len(c.Tags))
		copy(tags, c.Tags)
		c.Tags = tags
	}
	if c.Env != nil {
		env := make(map[string]string, len(c.Env))
		for k, v := range c.Env {
			env[k] = v
		}
		c.Env = env
	}
	return c
}

// AgentFactory creates a fresh Agent from a Config.
// Injected into Pool so Restart() can create a new instance.
type AgentFactory func(cfg Config) Agent

// Pool manages the full lifecycle of agents.
// Acts like a process supervisor — auto-restarts on crash.
type Pool struct {
	mu      sync.Mutex // single mutex prevents TOCTOU on Spawn/Kill/Restart
	agents  map[string]Agent
	configs map[string]Config // saved for Restart
	stopCh  map[string]chan struct{}
	doneCh  map[string]chan struct{} // closed when supervise goroutine exits
	bus     *EventBus
	factory AgentFactory
}

func NewPool(bus *EventBus, factory AgentFactory) *Pool {
	return &Pool{
		agents:  make(map[string]Agent),
		configs: make(map[string]Config),
		stopCh:  make(map[string]chan struct{}),
		doneCh:  make(map[string]chan struct{}),
		bus:     bus,
		factory: factory,
	}
}

// Spawn adds an agent to the pool and starts its supervisor loop.
func (p *Pool) Spawn(a Agent) error {
	id := a.Config().ID
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.agents[id]; exists {
		return fmt.Errorf("agent %s already exists", id)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	p.agents[id] = a
	p.configs[id] = deepCopyConfig(*a.Config())
	p.stopCh[id] = stop
	p.doneCh[id] = done

	go p.supervise(id, stop, done)

	p.bus.Publish(Event{
		Type:      EvtAgentSpawned,
		AgentID:   id,
		Timestamp: time.Now(),
	})
	log.Info().Str("agent", id).Str("role", a.Config().Role).Msg("agent spawned")
	return nil
}

// Kill stops the agent and cleans up. Safe to call concurrently.
func (p *Pool) Kill(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killLocked(id)
}

// killLocked performs the actual kill. Caller must hold p.mu.
// It temporarily releases p.mu while waiting for the supervise goroutine
// to exit, then re-acquires it.
func (p *Pool) killLocked(id string) error {
	a, ok := p.agents[id]
	if !ok {
		return fmt.Errorf("agent %s not found", id)
	}

	close(p.stopCh[id])
	done := p.doneCh[id]

	// Release the lock while waiting for the supervisor goroutine to exit,
	// so it can acquire the lock if needed during its final tick.
	p.mu.Unlock()
	<-done
	p.mu.Lock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a.OnShutdown(ctx)

	delete(p.agents, id)
	delete(p.configs, id)
	delete(p.stopCh, id)
	delete(p.doneCh, id)

	p.bus.Publish(Event{
		Type:      EvtAgentKilled,
		AgentID:   id,
		Timestamp: time.Now(),
	})
	log.Info().Str("agent", id).Msg("agent killed")
	return nil
}

// Restart kills the agent and spawns a fresh instance from the saved config.
// The entire operation runs under the pool mutex to prevent TOCTOU races.
func (p *Pool) Restart(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	cfg, ok := p.configs[id]
	if !ok {
		return fmt.Errorf("agent %s not found", id)
	}

	if err := p.killLocked(id); err != nil {
		return fmt.Errorf("kill during restart: %w", err)
	}

	fresh := p.factory(cfg)
	stop := make(chan struct{})
	done := make(chan struct{})
	p.agents[id] = fresh
	p.configs[id] = cfg
	p.stopCh[id] = stop
	p.doneCh[id] = done

	go p.supervise(id, stop, done)

	p.bus.Publish(Event{
		Type:      EvtAgentSpawned,
		AgentID:   id,
		Timestamp: time.Now(),
	})
	log.Info().Str("agent", id).Msg("agent restarted")
	return nil
}

// SetStatus updates the in-pool status snapshot for an agent.
// BaseAgent manages its own status internally; this is kept for
// external overrides (e.g. marking an agent failed from the API).
func (p *Pool) SetStatus(id string, s Status) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if a, ok := p.agents[id]; ok {
		if ba, ok := a.(*BaseAgent); ok {
			ba.setStatus(s)
		}
	}
}

// StatusAll returns a snapshot of all agent statuses.
//
// Lock ordering: Pool.mu → Agent.mu. This is safe because Agent.mu is
// always acquired after Pool.mu and never in the reverse order. All pool
// methods that call agent methods (GetByRole, StatusAll, SetStatus) follow
// this ordering. Do not acquire Pool.mu while holding an Agent.mu.
func (p *Pool) StatusAll() map[string]Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]Status, len(p.agents))
	for id, a := range p.agents {
		out[id] = a.Status()
	}
	return out
}

// Get returns an agent by ID.
func (p *Pool) Get(id string) (Agent, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.agents[id]
	return a, ok
}

// GetByRole returns agents for a given role, idle agents first.
// Lock ordering: Pool.mu → Agent.mu (see StatusAll comment).
func (p *Pool) GetByRole(role string) []Agent {
	p.mu.Lock()
	defer p.mu.Unlock()
	var idle, busy []Agent
	for _, a := range p.agents {
		if a.Config().Role != role {
			continue
		}
		if a.Status() == StatusIdle {
			idle = append(idle, a)
		} else {
			busy = append(busy, a)
		}
	}
	return append(idle, busy...)
}

// supervise runs the health-check loop; auto-restarts on failure.
// It does NOT call Restart (which takes the lock) — it calls restartFromSupervisor
// which is the same logic but entered without re-acquiring the pool lock
// (since supervise reads agent state and calls Restart externally and safely).
func (p *Pool) supervise(id string, stop <-chan struct{}, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			p.mu.Lock()
			a, ok := p.agents[id]
			p.mu.Unlock()

			if !ok {
				return
			}

			if a.Status() == StatusFailed {
				log.Warn().Str("agent", id).Msg("agent failed, restarting...")
				if err := p.Restart(id); err != nil {
					log.Error().Err(err).Str("agent", id).Msg("restart failed")
				}
				// After Restart the old stop channel is closed; this goroutine
				// will exit on next iteration via <-stop.
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			healthy := a.HealthCheck(ctx)
			cancel()

			if !healthy {
				log.Warn().Str("agent", id).Msg("health check failed")
				p.bus.Publish(Event{
					Type:      EvtAgentFailed,
					AgentID:   id,
					Payload:   "health check failed",
					Timestamp: time.Now(),
				})
			} else {
				p.bus.Publish(Event{
					Type:      EvtAgentHealthy,
					AgentID:   id,
					Timestamp: time.Now(),
				})
			}
		}
	}
}
