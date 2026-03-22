package port

import "context"

// LLMMessage represents a single message in a conversation with an LLM.
type LLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// LLMCacheControl specifies prompt caching behaviour.
type LLMCacheControl struct {
	CacheSystem bool   `json:"cache_system,omitempty"`
	TTL         string `json:"ttl,omitempty"` // "5m" or "1h"
}

// ThinkingConfig controls extended thinking (chain-of-thought reasoning)
// for models that support it (Opus, Sonnet).
type ThinkingConfig struct {
	Enabled      bool `json:"enabled"`
	BudgetTokens int  `json:"budget_tokens"` // max tokens for thinking
}

// LLMRequest is the input to an LLM call.
type LLMRequest struct {
	Model        string           `json:"model"`
	System       string           `json:"system,omitempty"`
	Messages     []LLMMessage     `json:"messages"`
	MaxTokens    int              `json:"max_tokens"`
	TaskID       string           `json:"task_id"`
	CacheControl *LLMCacheControl `json:"cache_control,omitempty"` // prompt caching
	BatchMode    bool             `json:"batch_mode,omitempty"`    // batch API (50% cheaper, async)
	Thinking     *ThinkingConfig  `json:"thinking,omitempty"`      // extended thinking (Anthropic)
}

// LLMResponse is the output from an LLM call.
type LLMResponse struct {
	Content         string  `json:"content"`
	ThinkingContent string  `json:"thinking_content,omitempty"` // model's reasoning trace
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ThinkingTokens  int64   `json:"thinking_tokens,omitempty"` // tokens used for thinking
	CacheTokens     int64   `json:"cache_tokens,omitempty"`
	CostUSD         float64 `json:"cost_usd"`
	CostMode        string  `json:"cost_mode,omitempty"` // "", "cache_hit", "cache_write", "batch"
	DurationMs      int64   `json:"duration_ms"`
	ModelUsed       string  `json:"model_used"`
}

// LLMRouter abstracts the routing of LLM calls to different providers.
type LLMRouter interface {
	Call(ctx context.Context, req LLMRequest) (*LLMResponse, error)
	Stats() map[string]int64
}
