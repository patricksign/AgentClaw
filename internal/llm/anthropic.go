package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ─── Anthropic (custom protocol) ─────────────────────────────────────────────
// Anthropic uses a unique API format: x-api-key auth, system as separate field
// with optional cache_control, and content[].text response structure.
// This CANNOT be merged into callOpenAICompat.

func (r *Router) callAnthropic(ctx context.Context, req Request, p llmProvider) (*Response, error) {
	apiKey := r.getenv(p.EnvKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%s is not set", p.EnvKey)
	}

	modelID := p.ModelID
	// When extended thinking is enabled, max_tokens must exceed budget_tokens.
	maxTokens := req.MaxTokens
	thinkingEnabled := req.Thinking != nil && req.Thinking.Enabled && req.Thinking.BudgetTokens > 0
	if thinkingEnabled && maxTokens <= req.Thinking.BudgetTokens {
		maxTokens = req.Thinking.BudgetTokens + req.MaxTokens
	}

	body := map[string]any{
		"model":      modelID,
		"max_tokens": maxTokens,
		"messages":   req.Messages,
	}

	// Extended thinking: instruct the model to reason before answering.
	if thinkingEnabled {
		body["thinking"] = map[string]any{
			"type":          "enabled",
			"budget_tokens": req.Thinking.BudgetTokens,
		}
	}

	if req.System != "" {
		if req.CacheControl != nil && req.CacheControl.CacheSystem {
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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("build anthropic request: %w", err)
	}
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", AnthropicAPIVersion)
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
			Type     string `json:"type"`     // "text" or "thinking"
			Text     string `json:"text"`     // present when type=="text"
			Thinking string `json:"thinking"` // present when type=="thinking"
		} `json:"content"`
		Usage struct {
			Input         int64 `json:"input_tokens"`
			Output        int64 `json:"output_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse anthropic response: %w", err)
	}

	// Separate thinking blocks from text blocks. Concatenate multiple blocks
	// of the same type to avoid silently discarding content.
	var contentParts, thinkingParts []string
	for _, block := range out.Content {
		switch block.Type {
		case "thinking":
			if block.Thinking != "" {
				thinkingParts = append(thinkingParts, block.Thinking)
			}
		case "text", "":
			if block.Text != "" {
				contentParts = append(contentParts, block.Text)
			}
		}
	}
	content := strings.Join(contentParts, "\n")
	thinkingContent := strings.Join(thinkingParts, "\n")

	// Estimate thinking tokens from content byte length (bytes / 4 approximation).
	// Avoids rune-slice allocation for large thinking outputs.
	var thinkingTokens int64
	if thinkingContent != "" {
		thinkingTokens = int64(len(thinkingContent)) / 4
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
		Content:         content,
		ThinkingContent: thinkingContent,
		InputTokens:     out.Usage.Input,
		OutputTokens:    out.Usage.Output,
		ThinkingTokens:  thinkingTokens,
		CacheTokens:     cacheTokens,
		CostMode:        costMode,
		ModelUsed:       req.Model,
	}, nil
}
