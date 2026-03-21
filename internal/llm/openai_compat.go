package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ─── OpenAI-Compatible (MiniMax, GLM, Kimi) ──────────────────────────────────
// One function handles all OpenAI-compatible providers via the llmProvider
// config struct. To add a new provider, add an entry to LLMProviders in
// provider.go — no code changes needed here.

func (r *Router) callOpenAICompat(ctx context.Context, req Request, p llmProvider) (*Response, error) {
	apiKey := r.getenv(p.EnvKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%s is not set", p.EnvKey)
	}

	msgs := req.Messages
	if req.System != "" {
		msgs = append([]Message{{Role: "system", Content: req.System}}, msgs...)
	}

	body := map[string]any{
		"model":      p.ModelID,
		"max_tokens": req.MaxTokens,
		"messages":   msgs,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal %s request: %w", p.Name, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.BaseURL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", p.Name, err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("content-type", "application/json")

	httpResp, err := r.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s HTTP: %w", p.Name, err)
	}
	defer httpResp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", p.Name, err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{
			StatusCode: httpResp.StatusCode,
			Provider:   p.Name,
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
		return nil, fmt.Errorf("parse %s response: %w", p.Name, err)
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
