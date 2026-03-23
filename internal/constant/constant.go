package constant

import (
	"maps"

	"github.com/patricksign/AgentClaw/config"
	"github.com/patricksign/AgentClaw/internal/adapter"
)

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

const maxTaskRetries = 3

func DefaultAgentConfigs(cfg *config.Config) []adapter.Config {
	return defaultAgentConfigs(cfg)
}

func WorkerMapEnvconfig(cfg *config.Config) map[string]string {
	return workerMapEnvconfig(cfg)
}

func workerMapEnvconfig(cfg *config.Config) map[string]string {
	minimaxEnv := WorkerMinimaxEnvconfig(cfg)
	kimiEnv := WorkerKimiEnvconfig(cfg)
	glmEnv := WorkerGLMEnvconfig(cfg)

	maps.Copy(minimaxEnv, kimiEnv)
	maps.Copy(minimaxEnv, glmEnv)
	return minimaxEnv
}

func WorkerAnthropicEnvconfig(cfg *config.Config) map[string]string {
	return map[string]string{
		EnvAnthropicKey: cfg.LLM.AnthropicAPIKey,
	}
}

func WorkerMinimaxEnvconfig(cfg *config.Config) map[string]string {
	return map[string]string{
		EnvMinimaxKey: cfg.LLM.MinimaxAPIKey,
	}
}

func WorkerKimiEnvconfig(cfg *config.Config) map[string]string {
	return map[string]string{
		EnvKimiKey: cfg.LLM.KimiAPIKey,
	}
}

func WorkerGLMEnvconfig(cfg *config.Config) map[string]string {
	return map[string]string{
		EnvGLMKey: cfg.LLM.GLMAPIKey,
	}
}

func defaultAgentConfigs(cfg *config.Config) []adapter.Config {
	anthropicEnv := WorkerAnthropicEnvconfig(cfg)
	workerEnv := workerMapEnvconfig(cfg)
	glmEnv := WorkerGLMEnvconfig(cfg)

	return []adapter.Config{
		{ID: "idea-agent-01", Name: "Idea Agent", Role: "idea", Model: "opus", MaxRetries: maxTaskRetries, TimeoutSecs: 120, Env: anthropicEnv},
		{ID: "architect-01", Name: "Architect", Role: "architect", Model: "opus", MaxRetries: maxTaskRetries, TimeoutSecs: 180, Env: anthropicEnv},
		{ID: "breakdown-01", Name: "Breakdown", Role: "breakdown", Model: "opus", MaxRetries: maxTaskRetries, TimeoutSecs: 120, Env: anthropicEnv},
		{ID: "coding-agent-01", Name: "Coder A", Role: "coding", Model: "minimax", MaxRetries: maxTaskRetries, TimeoutSecs: 600, Env: workerEnv},
		{ID: "coding-agent-02", Name: "Coder B", Role: "coding", Model: "minimax", MaxRetries: maxTaskRetries, TimeoutSecs: 600, Env: workerEnv},
		{ID: "test-agent-01", Name: "Tester", Role: "test", Model: "sonnet", MaxRetries: maxTaskRetries, TimeoutSecs: 300, Env: anthropicEnv},
		{ID: "review-agent-01", Name: "Reviewer", Role: "review", Model: "sonnet", MaxRetries: maxTaskRetries, TimeoutSecs: 300, Env: anthropicEnv},
		{ID: "docs-agent-01", Name: "Docs Writer", Role: "docs", Model: "minimax", MaxRetries: maxTaskRetries, TimeoutSecs: 120, Env: workerEnv},
		{ID: "deploy-agent-01", Name: "Deployer", Role: "deploy", Model: "glm-flash", MaxRetries: maxTaskRetries, TimeoutSecs: 180, Env: glmEnv},
		{ID: "notify-agent-01", Name: "Notifier", Role: "notify", Model: "glm-flash", MaxRetries: maxTaskRetries, TimeoutSecs: 30, Env: glmEnv},
	}
}
