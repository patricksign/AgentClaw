package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// maxResponseBytes caps LLM API response bodies to prevent OOM from malformed
// or adversarial upstream responses (10 MiB is well above any real completion).
const maxResponseBytes = 10 << 20 // 10 MiB

// ─── Pricing ─────────────────────────────────────────────────────────────────

type pricing struct {
	InputPer1M  float64
	OutputPer1M float64
}

var modelPricing = map[string]pricing{
	"opus":      {5.00, 25.00},
	"sonnet":    {3.00, 15.00},
	"haiku":     {1.00, 5.00},
	"minimax":   {0.30, 1.20},
	"glm5":      {0.72, 2.30},
	"glm-flash": {0.00, 0.00},
}

func CalcCost(model string, in, out int64) float64 {
	p, ok := modelPricing[model]
	if !ok {
		return 0
	}
	return float64(in)/1e6*p.InputPer1M + float64(out)/1e6*p.OutputPer1M
}

// ─── Request / Response ───────────────────────────────────────────────────────

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Request struct {
	Model     string    `json:"model"`   // "opus"|"sonnet"|"minimax"|"glm5"|"glm-flash"
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens"`
	TaskID    string    `json:"-"` // for logging only
}

type Response struct {
	Content      string  `json:"content"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	ModelUsed    string  `json:"model_used"`
	DurationMs   int64   `json:"duration_ms"`
}

// ─── Router ──────────────────────────────────────────────────────────────────

// Router routes LLM calls to the appropriate provider.
// Keys in env override the corresponding environment variables, allowing
// per-agent API key configuration without touching the global environment.
type Router struct {
	client *http.Client
	env    map[string]string // per-agent key overrides (optional)
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
	return &Router{
		client: &http.Client{
			Timeout:   120 * time.Second,
			Transport: newTransport(),
		},
	}
}

// NewRouterWithEnv creates a Router that uses per-agent key overrides.
// Keys present in env take precedence over OS environment variables.
// Recognised keys: ANTHROPIC_API_KEY, MINIMAX_API_KEY, GLM_API_KEY.
func NewRouterWithEnv(env map[string]string) *Router {
	return &Router{
		client: &http.Client{
			Timeout:   120 * time.Second,
			Transport: newTransport(),
		},
		env: env,
	}
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

	if err != nil {
		return nil, err
	}
	resp.DurationMs = time.Since(start).Milliseconds()
	resp.CostUSD = CalcCost(req.Model, resp.InputTokens, resp.OutputTokens)
	return resp, nil
}

// isPermanentError returns true for 4xx HTTP errors (auth, bad request, etc.)
// which should not trigger a fallback.
func isPermanentError(err error) bool {
	msg := err.Error()
	for _, code := range []string{"400", "401", "403", "404", "422"} {
		if strings.Contains(msg, code) {
			return true
		}
	}
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
		body["system"] = req.System
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
		return nil, fmt.Errorf("anthropic %d: %s", httpResp.StatusCode, raw)
	}

	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			Input  int64 `json:"input_tokens"`
			Output int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse anthropic response: %w", err)
	}

	content := ""
	if len(out.Content) > 0 {
		content = out.Content[0].Text
	}
	return &Response{
		Content:      content,
		InputTokens:  out.Usage.Input,
		OutputTokens: out.Usage.Output,
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
		return nil, fmt.Errorf("minimax %d: %s", httpResp.StatusCode, raw)
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
		return nil, fmt.Errorf("glm %d: %s", httpResp.StatusCode, raw)
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
