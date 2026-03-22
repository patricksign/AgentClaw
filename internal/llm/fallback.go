package llm

import (
	"context"
	"fmt"
	"time"
)

// ─── Fallback Chain ──────────────────────────────────────────────────────────

// callWithFallback tries each model in the fallback chain with retries.
// Per K2.5 spec: each model gets MaxRetries attempts with exponential backoff
// (10s → 30s → 60s). Non-retryable errors (4xx) skip retries and move to
// the next model immediately.
func (r *Router) callWithFallback(ctx context.Context, req Request, chain []string) (*Response, error) {
	var lastErr error
	prevModel := req.Model

	for _, model := range chain {
		p, ok := LLMProviders[model]
		if !ok {
			continue
		}

		// Notify: fallback triggered (async — non-blocking).
		r.notifyFallback(FallbackEvent{
			TaskID:    req.TaskID,
			FromModel: prevModel,
			ToModel:   model,
		})

		for attempt := range MaxRetries {
			if attempt > 0 {
				delay := time.Duration(RetryIntervals[attempt-1]) * time.Second
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, ctx.Err()
				case <-timer.C:
				}
			}

			fallbackReq := req
			fallbackReq.Model = model

			resp, err := r.callOpenAICompat(ctx, fallbackReq, p)
			if err == nil {
				if resp != nil {
					resp.ModelUsed = model + "(fallback)"
				}
				return resp, nil
			}

			lastErr = err

			// Non-retryable (4xx) → skip to next model.
			if isPermanentError(err) {
				break
			}
		}

		prevModel = model
	}

	// Notify: entire fallback chain exhausted — critical, must reach human.
	r.notifyFallback(FallbackEvent{
		TaskID:    req.TaskID,
		FromModel: prevModel,
		Err:       lastErr.Error(),
		Exhausted: true,
	})

	return nil, fmt.Errorf("fallback chain exhausted: %w", lastErr)
}

// notifyFallback calls the registered callback if set.
// Thread-safe: reads fn under RLock.
func (r *Router) notifyFallback(evt FallbackEvent) {
	r.fallback.mu.RLock()
	fn := r.fallback.fn
	r.fallback.mu.RUnlock()
	if fn != nil {
		fn(evt)
	}
}

// SetFallbackNotifier registers a callback for fallback events.
// Thread-safe: writes fn under Lock.
func (r *Router) SetFallbackNotifier(fn FallbackNotifyFunc) {
	r.fallback.mu.Lock()
	r.fallback.fn = fn
	r.fallback.mu.Unlock()
}
