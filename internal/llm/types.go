package llm

import (
	"net/http"
	"sync"
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
	Model        string        `json:"model"` // "opus"|"sonnet"|"haiku"|"minimax"|"kimi"|"glm5"|"glm-flash"
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
	CacheTokens  int64    `json:"cache_tokens,omitempty"` // tokens served from cache
	CostUSD      float64  `json:"cost_usd"`
	CostMode     CostMode `json:"cost_mode,omitempty"` // pricing mode used
	ModelUsed    string   `json:"model_used"`
	DurationMs   int64    `json:"duration_ms"`
}

// ─── Router ──────────────────────────────────────────────────────────────────

// FallbackEvent is emitted when a fallback or exhaustion occurs.
// The llm package cannot import domain/port (import cycle), so callers
// register a callback to bridge events to the notification system.
type FallbackEvent struct {
	TaskID    string // from Request.TaskID
	FromModel string // model that failed
	ToModel   string // model taking over ("" if exhausted)
	Err       string // last error message
	Exhausted bool   // true if entire chain failed
}

// FallbackNotifyFunc is called when a fallback event occurs.
// Implementations should be non-blocking (fire in goroutine if needed).
type FallbackNotifyFunc func(evt FallbackEvent)

// Router routes LLM calls to the appropriate provider.
// Keys in env override the corresponding environment variables, allowing
// per-agent API key configuration without touching the global environment.
type Router struct {
	client     *http.Client
	env        map[string]string  // per-agent key overrides (optional)
	breakers   *breakerRegistry   // per-provider circuit breakers
	onFallback FallbackNotifyFunc // called on fallback/exhaustion events (optional)
	stats      struct {
		mu    sync.Mutex
		calls map[string]int64 // provider → call count
	}
}

// ─── Error types ─────────────────────────────────────────────────────────────

// httpStatusError is a typed error that carries the HTTP status code.
// Used by provider call functions so isPermanentError can inspect the code
// directly instead of relying on fragile string matching.
type httpStatusError struct {
	StatusCode int
	Provider   string
	Body       string
}
