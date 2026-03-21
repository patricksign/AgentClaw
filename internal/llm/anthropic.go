package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	body := map[string]any{
		"model":      modelID,
		"max_tokens": req.MaxTokens,
		"messages":   req.Messages,
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
			Text string `json:"text"`
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
