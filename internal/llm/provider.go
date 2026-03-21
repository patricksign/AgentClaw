package llm

// provider.go — Single source of truth for all LLM provider configuration.
// Base URLs, model ID mappings, and API key names live here.
// No other file should hardcode these values.

// ─── Base URLs ───────────────────────────────────────────────────────────────

const (
	AnthropicBaseURL = "https://api.anthropic.com/v1/messages"
	MinimaxBaseURL   = "https://api.minimax.io/v1/text/chatcompletion_v2"
	GLMBaseURL       = "https://api.z.ai/api/coding/paas/v4"
	KimiBaseURL      = "https://api.moonshot.ai/v1/chat/completions"
)

// ─── API Key Environment Variable Names ──────────────────────────────────────

const (
	EnvAnthropicKey = "ANTHROPIC_API_KEY"
	EnvMinimaxKey   = "MINIMAX_API_KEY"
	EnvGLMKey       = "GLM_API_KEY"
	EnvKimiKey      = "KIMI_API_KEY"
)

// ─── Anthropic API version ────────────────────────────────────────────────────

const AnthropicAPIVersion = "2023-06-01"

// ─── Unified Provider Registry ───────────────────────────────────────────────
// All providers (Anthropic + OpenAI-compatible) are registered in LLMProviders.
// Anthropic entries have IsAnthropic=true → routed to callAnthropic().
// Others have IsAnthropic=false → routed to callOpenAICompat().
// To add a new provider: add one entry to LLMProviders — no router code changes.

type llmProvider struct {
	Name        string // provider name for logs and circuit breaker grouping
	BaseURL     string // API endpoint
	EnvKey      string // env var name for the API key
	ModelID     string // model ID sent in the request body
	IsAnthropic bool
}

var LLMProviders = map[string]llmProvider{
	// Anthropic (custom protocol)
	"opus":   {"anthropic", AnthropicBaseURL, EnvAnthropicKey, "claude-opus-4-6", true},
	"sonnet": {"anthropic", AnthropicBaseURL, EnvAnthropicKey, "claude-sonnet-4-6", true},
	"haiku":  {"anthropic", AnthropicBaseURL, EnvAnthropicKey, "claude-haiku-4-5-20251001", true},
	// MiniMax (OpenAI-compatible)
	"minimax": {"minimax", MinimaxBaseURL, EnvMinimaxKey, "MiniMax-M2.5", false},
	// MoonshotAI (OpenAI-compatible)
	"kimi": {"moonshot", KimiBaseURL, EnvKimiKey, "kimi-k2.5", false},
	// Z.AI / Zhipu (OpenAI-compatible)
	"glm5":      {"zhipu", GLMBaseURL, EnvGLMKey, "glm-5", false},
	"glm-flash": {"zhipu", GLMBaseURL, EnvGLMKey, "glm-4.5-flash", false},
}

// IsOpenAICompatModel returns true if the model alias is handled by callOpenAICompat.
func IsOpenAICompatModel(model string) bool {
	m, ok := LLMProviders[model]
	return ok && !m.IsAnthropic
}

// IsAnthropicModel returns true if the model alias is handled by callAnthropic.
func IsAnthropicModel(model string) bool {
	m, ok := LLMProviders[model]
	return ok && m.IsAnthropic
}

// ─── Fallback Chain ─────────────────────────────────────────────────────────
// Defines the model fallback order for worker tasks (implement + docs).
// When a model fails with a transient error, the next model in the chain
// takes over. MiniMax is always retried first for each new task (Rule 4).

// WorkerFallbackChain is the ordered fallback chain for worker-tier tasks.
// Each model is tried with retries before falling through to the next.
var WorkerFallbackChain = []string{"minimax", "kimi", "glm5"}

// FallbackChainFrom returns the fallback models to try after the given model fails.
// Returns nil if the model is not in the worker chain or is the last entry.
func FallbackChainFrom(model string) []string {
	for i, m := range WorkerFallbackChain {
		if m == model && i+1 < len(WorkerFallbackChain) {
			return WorkerFallbackChain[i+1:]
		}
	}
	return nil
}

// ─── Retry Policy ───────────────────────────────────────────────────────────

// RetryIntervals defines exponential backoff intervals (per K2.5 spec).
// 10s → 30s → 60s
var RetryIntervals = []int{10, 30, 60}

// MaxRetries is the maximum retry attempts per model before fallback.
const MaxRetries = 3
