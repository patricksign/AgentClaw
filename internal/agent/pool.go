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

// ShutdownAll stops every supervised agent gracefully.
// It signals all supervisors to stop, waits for them to exit (up to ctx
// deadline), then calls OnShutdown on each agent.
func (p *Pool) ShutdownAll(ctx context.Context) {
	p.mu.Lock()
	ids := make([]string, 0, len(p.agents))
	for id := range p.agents {
		ids = append(ids, id)
	}
	// Signal all supervisors to stop.
	for _, id := range ids {
		close(p.stopCh[id])
	}
	// Snapshot done channels before releasing the lock.
	dones := make(map[string]chan struct{}, len(ids))
	for _, id := range ids {
		dones[id] = p.doneCh[id]
	}
	p.mu.Unlock()

	// Wait for every supervisor goroutine to exit, honouring the context deadline.
	for id, done := range dones {
		select {
		case <-done:
		case <-ctx.Done():
			log.Warn().Str("agent", id).Msg("shutdown: supervisor did not exit in time")
		}
	}

	// Call OnShutdown on each agent now that supervisors are gone.
	p.mu.Lock()
	agents := make(map[string]Agent, len(p.agents))
	for id, a := range p.agents {
		agents[id] = a
	}
	p.mu.Unlock()

	for id, a := range agents {
		a.OnShutdown(ctx)
		log.Info().Str("agent", id).Msg("shutdown: agent stopped")
	}
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

// All returns a snapshot of all agents currently in the pool.
func (p *Pool) All() []Agent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Agent, 0, len(p.agents))
	for _, a := range p.agents {
		out = append(out, a)
	}
	return out
}

// supervise runs the health-check loop; auto-restarts on failure.
//
// Deadlock-safe restart: supervise must NOT call p.Restart() because Restart
// calls killLocked(), which closes stopCh[id] and then blocks on <-doneCh[id].
// doneCh[id] is the very channel this goroutine closes via defer — so it would
// never close, causing a deadlock.
//
// Instead, supervise calls restartLocked() which performs the restart inline
// while already holding the pool mutex, then returns new stop/done channels
// so this goroutine can loop on the new agent without spawning a nested call.
func (p *Pool) supervise(id string, stop <-chan struct{}, done chan struct{}) {
	// Use a pointer so the defer always closes the current done channel even
	// after it is replaced during a supervisor-initiated restart.
	donep := &done
	defer func() { close(*donep) }()

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
				newStop, newDone, err := p.restartFromSupervisor(id)
				if err != nil {
					log.Error().Err(err).Str("agent", id).Msg("restart failed")
					return
				}
				// Hand off to the new agent's channels and continue the loop.
				// donep still points to done so defer will close the latest channel.
				stop = newStop
				done = newDone
				continue
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

// restartFromSupervisor replaces the failed agent with a fresh instance without
// calling killLocked (which would deadlock by waiting on this goroutine's done
// channel). It shuts down the old agent directly, then registers the new one.
// Returns the new stop and done channels for the caller (supervise) to adopt.
func (p *Pool) restartFromSupervisor(id string) (newStop <-chan struct{}, newDone chan struct{}, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	old, ok := p.agents[id]
	if !ok {
		return nil, nil, fmt.Errorf("agent %s not found", id)
	}
	cfg, ok := p.configs[id]
	if !ok {
		return nil, nil, fmt.Errorf("config for agent %s not found", id)
	}

	// Shutdown the old agent directly — do NOT close stopCh[id] here because
	// that is the channel supervise is currently selecting on; closing it would
	// cause supervise to exit before we finish the restart.
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	old.OnShutdown(shutCtx)

	// Replace pool entries with the fresh agent.
	fresh := p.factory(cfg)
	stop := make(chan struct{})
	done := make(chan struct{})

	p.agents[id] = fresh
	p.configs[id] = cfg
	p.stopCh[id] = stop
	p.doneCh[id] = done

	p.bus.Publish(Event{
		Type:      EvtAgentSpawned,
		AgentID:   id,
		Timestamp: time.Now(),
	})
	log.Info().Str("agent", id).Msg("agent restarted (from supervisor)")

	return stop, done, nil
}
