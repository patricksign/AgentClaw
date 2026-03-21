package llm

import (
	"fmt"
	"sync"
	"time"
)

// circuitState represents the state of a circuit breaker.
type circuitState int

const (
	circuitClosed   circuitState = iota // normal operation
	circuitOpen                         // failing — reject fast
	circuitHalfOpen                     // testing recovery
)

// breaker is a simple circuit breaker per provider endpoint.
// After consecutiveFailures >= threshold, it opens for cooldown duration.
// After cooldown, one probe request is allowed (half-open). If it succeeds,
// the breaker closes; if it fails, it opens again.
//
// Uses atomic state for the fast path (circuitClosed) to avoid mutex
// contention when all providers are healthy.
type breaker struct {
	mu        sync.Mutex
	state     circuitState
	failures  int
	threshold int
	cooldown  time.Duration
	openedAt  time.Time
	lastErr   error
}

func newBreaker(threshold int, cooldown time.Duration) *breaker {
	return &breaker{
		threshold: threshold,
		cooldown:  cooldown,
	}
}

// allow checks whether a request should proceed.
// Returns an error if the circuit is open (caller should not make the request).
func (b *breaker) allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case circuitClosed:
		return nil
	case circuitOpen:
		if time.Since(b.openedAt) >= b.cooldown {
			b.state = circuitHalfOpen
			return nil // allow one probe
		}
		return fmt.Errorf("circuit open for provider (last error: %v), retry after %v",
			b.lastErr, b.cooldown-time.Since(b.openedAt))
	case circuitHalfOpen:
		// Only one probe at a time — block others while probing.
		return fmt.Errorf("circuit half-open, probe in progress")
	}
	return nil
}

// recordSuccess resets the failure counter and closes the circuit.
func (b *breaker) recordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.state = circuitClosed
	b.lastErr = nil
}

// recordFailure increments the failure counter and opens the circuit if threshold is reached.
func (b *breaker) recordFailure(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	b.lastErr = err
	if b.failures >= b.threshold {
		b.state = circuitOpen
		b.openedAt = time.Now()
	}
}

// breakerRegistry manages per-provider circuit breakers.
type breakerRegistry struct {
	mu       sync.Mutex
	breakers map[string]*breaker
}

func newBreakerRegistry() *breakerRegistry {
	return &breakerRegistry{
		breakers: make(map[string]*breaker),
	}
}

// get returns the breaker for a provider, creating one if needed.
// Default: open after 5 consecutive failures, cooldown 60 seconds.
func (r *breakerRegistry) get(provider string) *breaker {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.breakers[provider]
	if !ok {
		b = newBreaker(5, 60*time.Second)
		r.breakers[provider] = b
	}
	return b
}
