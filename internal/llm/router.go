package llm

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog/log"
)

// newTransport returns an isolated http.Transport that dials IPv4 only.
// Using a dedicated transport per Router prevents one agent's connection
// pool state from affecting another, and forces tcp4 to avoid IPv6
// connectivity issues on networks where AAAA records resolve but the
// path is broken.
func newTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", addr)
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   5,
		IdleConnTimeout:       90 * time.Second,
	}
}

func NewRouter() *Router {
	r := &Router{
		client: &http.Client{
			Timeout:   120 * time.Second,
			Transport: newTransport(),
		},
		breakers: newBreakerRegistry(),
	}
	r.stats.calls = make(map[string]int64)
	return r
}

// NewRouterWithEnv creates a Router that uses per-agent key overrides.
// Keys present in env take precedence over OS environment variables.
// Recognised keys: see Env* constants in provider.go.
func NewRouterWithEnv(env map[string]string) *Router {
	r := &Router{
		client: &http.Client{
			Timeout:   120 * time.Second,
			Transport: newTransport(),
		},
		env:      env,
		breakers: newBreakerRegistry(),
	}
	r.stats.calls = make(map[string]int64)
	return r
}

// getenv returns the value for key, preferring the per-agent env map over
// the OS environment.
func (r *Router) getenv(key string) string {
	if r.env != nil {
		if v, ok := r.env[key]; ok && v != "" {
			return v
		}
	}
	return os.Getenv(key)
}

// ─── Call (main entry point) ─────────────────────────────────────────────────

func (r *Router) Call(ctx context.Context, req Request) (*Response, error) {
	start := time.Now()

	provider := providerForModel(req.Model)

	// Circuit breaker: reject fast if the provider is known to be down.
	if cbErr := r.breakers.get(provider).allow(); cbErr != nil {
		log.Warn().Str("model", req.Model).Msg("llm circuit breaker rejecting request")
		return nil, fmt.Errorf("llm %s: %w", provider, cbErr)
	}

	var resp *Response
	var err error

	model, ok := LLMProviders[req.Model]
	if !ok {
		return nil, fmt.Errorf("unknown llm model alias: %s", req.Model)
	}

	if model.IsAnthropic {
		resp, err = r.callAnthropic(ctx, req, model)
	} else {
		resp, err = r.callOpenAICompat(ctx, req, model)
		if err != nil && !isPermanentError(err) {
			if chain := FallbackChainFrom(req.Model); len(chain) > 0 {
				resp, err = r.callWithFallback(ctx, req, chain)
			}
		}
	}

	// Record success/failure in the circuit breaker.
	cb := r.breakers.get(provider)
	if err != nil {
		if !isPermanentError(err) {
			cb.recordFailure(err)
		}
		return nil, err
	}
	cb.recordSuccess()

	// Track per-provider call counts.
	r.stats.mu.Lock()
	r.stats.calls[provider]++
	r.stats.mu.Unlock()

	resp.DurationMs = time.Since(start).Milliseconds()

	// Use the actual model that served the response for cost calculation.
	// When fallback occurs, resp.ModelUsed is "kimi(fallback)" etc. — strip
	// the "(fallback)" suffix to get the pricing alias.
	costModel := resp.ModelUsed
	if idx := len(costModel) - len("(fallback)"); idx > 0 && costModel[idx:] == "(fallback)" {
		costModel = costModel[:idx]
	}

	if req.BatchMode {
		resp.CostMode = CostModeBatch
	}
	resp.CostUSD = costCalc(costModel, resp)
	return resp, nil
}

func costCalc(costModel string, resp *Response) float64 {
	if resp.CostMode != "" {
		cost, costErr := CalcCostAdvanced(costModel, resp.InputTokens, resp.OutputTokens, resp.CacheTokens, resp.CostMode)
		if costErr != nil {
			log.Warn().Str("model", costModel).Str("mode", string(resp.CostMode)).Msg("cost calculation failed — reporting $0")
			return 0
		}
		return cost
	}

	cost, costErr := CalcCost(costModel, resp.InputTokens, resp.OutputTokens)
	if costErr != nil {
		log.Warn().Str("model", costModel).Msg("cost calculation failed — reporting $0")
		return 0
	}
	return cost
}

// providerForModel returns the provider name for circuit breaker grouping.
// All models in LLMProviders have Name set to the provider (e.g. "anthropic", "minimax", "glm").
func providerForModel(model string) string {
	if m, ok := LLMProviders[model]; ok {
		return m.Name
	}
	return model
}

// ─── Stats ───────────────────────────────────────────────────────────────────

// Stats returns per-provider call counts.
func (r *Router) Stats() map[string]int64 {
	r.stats.mu.Lock()
	defer r.stats.mu.Unlock()
	out := make(map[string]int64, len(r.stats.calls))
	for k, v := range r.stats.calls {
		out[k] = v
	}
	return out
}
