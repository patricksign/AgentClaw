package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// maxResponseBytes caps LLM API response bodies to prevent OOM from malformed
// or adversarial upstream responses (10 MiB is well above any real completion).
const maxResponseBytes = 10 << 20 // 10 MiB

// ─── Request / Response ───────────────────────────────────────────────────────

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// CacheControl specifies prompt caching behaviour for Anthropic API calls.
type CacheControl struct {
	// CacheSystem caches the system prompt (stable across tasks for the same role).
	CacheSystem bool `json:"cache_system,omitempty"`
	// TTL is the cache time-to-live: "5m" (ephemeral) or "1h" (persistent).
	// Defaults to "5m" if CacheSystem is true and TTL is empty.
	TTL string `json:"ttl,omitempty"`
}

type Request struct {
	Model        string        `json:"model"`   // "opus"|"sonnet"|"minimax"|"glm5"|"glm-flash"
	System       string        `json:"system,omitempty"`
	Messages     []Message     `json:"messages"`
	MaxTokens    int           `json:"max_tokens"`
	TaskID       string        `json:"-"`                       // for logging only
	CacheControl *CacheControl `json:"cache_control,omitempty"` // prompt caching (Anthropic only)
	BatchMode    bool          `json:"batch_mode,omitempty"`    // use batch API (async, 50% cheaper)
}

type Response struct {
	Content      string   `json:"content"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	CacheTokens  int64    `json:"cache_tokens,omitempty"`  // tokens served from cache
	CostUSD      float64  `json:"cost_usd"`
	CostMode     CostMode `json:"cost_mode,omitempty"`     // pricing mode used
	ModelUsed    string   `json:"model_used"`
	DurationMs   int64    `json:"duration_ms"`
}

// ─── Router ──────────────────────────────────────────────────────────────────

// Router routes LLM calls to the appropriate provider.
// Keys in env override the corresponding environment variables, allowing
// per-agent API key configuration without touching the global environment.
type Router struct {
	client   *http.Client
	env      map[string]string // per-agent key overrides (optional)
	breakers *breakerRegistry  // per-provider circuit breakers
	stats    struct {
		mu    sync.Mutex
		calls map[string]int64 // provider → call count
	}
}

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
// Recognised keys: ANTHROPIC_API_KEY, MINIMAX_API_KEY, GLM_API_KEY.
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

func (r *Router) Call(ctx context.Context, req Request) (*Response, error) {
	start := time.Now()
	var resp *Response
	var err error

	provider := providerForModel(req.Model)

	// Circuit breaker: reject fast if the provider is known to be down.
	if cbErr := r.breakers.get(provider).allow(); cbErr != nil {
		return nil, fmt.Errorf("llm %s: %w", provider, cbErr)
	}

	switch req.Model {
	case "opus", "sonnet", "haiku":
		resp, err = r.callAnthropic(ctx, req)
	case "minimax":
		resp, err = r.callMinimax(ctx, req)
		if err != nil {
			// Only fall back on transient/server errors (5xx, network).
			// Permanent errors (4xx auth failures) are propagated as-is
			// so callers surface misconfiguration rather than silently
			// consuming a more expensive model.
			if !isPermanentError(err) {
				req.Model = "sonnet"
				resp, err = r.callAnthropic(ctx, req)
				if resp != nil {
					resp.ModelUsed = "sonnet(fallback)"
				}
			}
		}
	case "glm5", "glm-flash":
		resp, err = r.callGLM(ctx, req)
	default:
		return nil, fmt.Errorf("unknown model: %s", req.Model)
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

	// Use advanced cost calculation when cache or batch mode is active.
	// Cost calculation errors (unknown model) are logged but do not fail the call —
	// the response content is still valid.
	if req.BatchMode {
		resp.CostMode = CostModeBatch
	}
	if resp.CostMode != "" {
		cost, costErr := CalcCostAdvanced(req.Model, resp.InputTokens, resp.OutputTokens, resp.CacheTokens, resp.CostMode)
		if costErr != nil {
			log.Warn().Str("model", req.Model).Str("mode", string(resp.CostMode)).Msg("cost calculation failed — reporting $0")
			resp.CostUSD = 0
		} else {
			resp.CostUSD = cost
		}
	} else {
		cost, costErr := CalcCost(req.Model, resp.InputTokens, resp.OutputTokens)
		if costErr != nil {
			log.Warn().Str("model", req.Model).Msg("cost calculation failed — reporting $0")
			resp.CostUSD = 0
		} else {
			resp.CostUSD = cost
		}
	}
	return resp, nil
}

// providerForModel returns the provider name for circuit breaker grouping.
func providerForModel(model string) string {
	switch model {
	case "opus", "sonnet", "haiku":
		return "anthropic"
	case "minimax":
		return "minimax"
	case "glm5", "glm-flash":
		return "glm"
	default:
		return model
	}
}

// httpStatusError is a typed error that carries the HTTP status code.
// Used by provider call functions so isPermanentError can inspect the code
// directly instead of relying on fragile string matching.
type httpStatusError struct {
	StatusCode int
	Provider   string
	Body       string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("%s %d: %s", e.Provider, e.StatusCode, e.Body)
}

// isPermanentError returns true for 4xx HTTP errors (auth, bad request, etc.)
// which should not trigger a fallback. Uses typed httpStatusError when available,
// falls back to string matching for wrapped errors.
func isPermanentError(err error) bool {
	var httpErr *httpStatusError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode >= 400 && httpErr.StatusCode < 500
	}
	// Fallback for errors that don't carry status code (e.g. network errors).
	return false
}

// ─── Anthropic ───────────────────────────────────────────────────────────────

var anthropicModels = map[string]string{
	"opus":   "claude-opus-4-6",
	"sonnet": "claude-sonnet-4-6",
	"haiku":  "claude-haiku-4-5-20251001",
}

func (r *Router) callAnthropic(ctx context.Context, req Request) (*Response, error) {
	apiKey := r.getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is not set")
	}

	modelID, ok := anthropicModels[req.Model]
	if !ok {
		return nil, fmt.Errorf("unknown anthropic model alias: %s", req.Model)
	}

	body := map[string]any{
		"model":      modelID,
		"max_tokens": req.MaxTokens,
		"messages":   req.Messages,
	}
	if req.System != "" {
		if req.CacheControl != nil && req.CacheControl.CacheSystem {
			// Use structured system prompt with cache_control block.
			ttl := req.CacheControl.TTL
			if ttl == "" {
				ttl = "ephemeral"
			}
			body["system"] = []map[string]any{
				{
					"type": "text",
					"text": req.System,
					"cache_control": map[string]string{
						"type": ttl,
					},
				},
			}
		} else {
			body["system"] = req.System
		}
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.anthropic.com/v1/messages",
		bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("build anthropic request: %w", err)
	}
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("content-type", "application/json")

	httpResp, err := r.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic HTTP: %w", err)
	}
	defer httpResp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read anthropic response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{
			StatusCode: httpResp.StatusCode,
			Provider:   "anthropic",
			Body:       truncateErrorBody(raw),
		}
	}

	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			Input            int64 `json:"input_tokens"`
			Output           int64 `json:"output_tokens"`
			CacheCreation    int64 `json:"cache_creation_input_tokens"`
			CacheRead        int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse anthropic response: %w", err)
	}

	content := ""
	if len(out.Content) > 0 {
		content = out.Content[0].Text
	}

	cacheTokens := out.Usage.CacheRead + out.Usage.CacheCreation
	var costMode CostMode
	if cacheTokens > 0 {
		if out.Usage.CacheRead > out.Usage.CacheCreation {
			costMode = CostModeCacheHit
		} else {
			costMode = CostModeCacheWrite
		}
	}

	return &Response{
		Content:      content,
		InputTokens:  out.Usage.Input,
		OutputTokens: out.Usage.Output,
		CacheTokens:  cacheTokens,
		CostMode:     costMode,
		ModelUsed:    req.Model,
	}, nil
}

// ─── MiniMax ─────────────────────────────────────────────────────────────────

func (r *Router) callMinimax(ctx context.Context, req Request) (*Response, error) {
	apiKey := r.getenv("MINIMAX_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("MINIMAX_API_KEY is not set")
	}

	msgs := req.Messages
	if req.System != "" {
		msgs = append([]Message{{Role: "system", Content: req.System}}, msgs...)
	}
	body := map[string]any{
		"model":      "MiniMax-M2.5",
		"max_tokens": req.MaxTokens,
		"messages":   msgs,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal minimax request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.minimax.io/v1/text/chatcompletion_v2",
		bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("build minimax request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("content-type", "application/json")

	httpResp, err := r.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("minimax HTTP: %w", err)
	}
	defer httpResp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read minimax response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{
			StatusCode: httpResp.StatusCode,
			Provider:   "minimax",
			Body:       truncateErrorBody(raw),
		}
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int64 `json:"prompt_tokens"`
			Completion int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse minimax response: %w", err)
	}

	content := ""
	if len(out.Choices) > 0 {
		content = out.Choices[0].Message.Content
	}
	return &Response{
		Content:      content,
		InputTokens:  out.Usage.Prompt,
		OutputTokens: out.Usage.Completion,
		ModelUsed:    "minimax",
	}, nil
}

// ─── GLM ─────────────────────────────────────────────────────────────────────

func (r *Router) callGLM(ctx context.Context, req Request) (*Response, error) {
	apiKey := r.getenv("GLM_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("GLM_API_KEY is not set")
	}

	modelID := "glm-5"
	if req.Model == "glm-flash" {
		modelID = "glm-4.5-flash"
	}

	msgs := req.Messages
	if req.System != "" {
		msgs = append([]Message{{Role: "system", Content: req.System}}, msgs...)
	}
	body := map[string]any{
		"model":      modelID,
		"max_tokens": req.MaxTokens,
		"messages":   msgs,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal glm request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://open.bigmodel.cn/api/paas/v4/chat/completions",
		bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("build glm request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("content-type", "application/json")

	httpResp, err := r.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("glm HTTP: %w", err)
	}
	defer httpResp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read glm response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{
			StatusCode: httpResp.StatusCode,
			Provider:   "glm",
			Body:       truncateErrorBody(raw),
		}
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int64 `json:"prompt_tokens"`
			Completion int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse glm response: %w", err)
	}

	content := ""
	if len(out.Choices) > 0 {
		content = out.Choices[0].Message.Content
	}
	return &Response{
		Content:      content,
		InputTokens:  out.Usage.Prompt,
		OutputTokens: out.Usage.Completion,
		ModelUsed:    req.Model,
	}, nil
}

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

// truncateErrorBody caps the API error response body at 200 bytes to prevent
// leaking internal API details (account info, rate limit metadata) in error
// messages that may propagate to logs or HTTP responses.
func truncateErrorBody(raw []byte) string {
	const maxErrBytes = 200
	if len(raw) <= maxErrBytes {
		return string(raw)
	}
	return string(raw[:maxErrBytes]) + "...(truncated)"
}
