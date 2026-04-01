package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/antlss/gitlab-review-agent/internal/shared"
)

type AnthropicClient struct {
	apiKey        string
	model         string
	contextWindow int
	httpClient    *http.Client
}

func NewAnthropicClient(apiKey, model string, contextWindow int) (*AnthropicClient, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("Anthropic API key is required")
	}
	return &AnthropicClient{
		apiKey:        apiKey,
		model:         model,
		contextWindow: contextWindow,
		httpClient:    &http.Client{Timeout: 5 * time.Minute},
	}, nil
}

func (c *AnthropicClient) ModelName() string      { return c.model }
func (c *AnthropicClient) ContextWindowSize() int { return c.contextWindow }
func (c *AnthropicClient) ClientCount() int       { return 1 }

func (c *AnthropicClient) Chat(ctx context.Context, req shared.ChatRequest) (*shared.ChatResponse, error) {
	// Build request body
	var systemContent string
	var messages []map[string]any
	for _, msg := range req.Messages {
		switch msg.Role {
		case "system":
			systemContent = msg.Content
		case "user":
			messages = append(messages, map[string]any{
				"role":    "user",
				"content": msg.Content,
			})
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				var content []map[string]any
				if msg.Content != "" {
					content = append(content, map[string]any{"type": "text", "text": msg.Content})
				}
				for _, tc := range msg.ToolCalls {
					var input any
					json.Unmarshal([]byte(tc.InputJSON), &input)
					content = append(content, map[string]any{
						"type":  "tool_use",
						"id":    tc.ID,
						"name":  tc.Name,
						"input": input,
					})
				}
				messages = append(messages, map[string]any{"role": "assistant", "content": content})
			} else {
				messages = append(messages, map[string]any{"role": "assistant", "content": msg.Content})
			}
		case "tool":
			messages = append(messages, map[string]any{
				"role": "user",
				"content": []map[string]any{{
					"type":        "tool_result",
					"tool_use_id": msg.ToolCallID,
					"content":     msg.Content,
				}},
			})
		}
	}

	body := map[string]any{
		"model":      c.model,
		"messages":   messages,
		"max_tokens": req.MaxTokens,
	}
	// Use prompt caching on the system prompt to reduce cost and latency on repeated calls.
	if systemContent != "" {
		body["system"] = []map[string]any{{
			"type":          "text",
			"text":          systemContent,
			"cache_control": map[string]any{"type": "ephemeral"},
		}}
	}
	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}

	// Build tools
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.InputSchema,
			})
		}
		body["tools"] = tools
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	respBody, err := doRequest(ctx, c.httpClient, "POST", "https://api.anthropic.com/v1/messages", data,
		map[string]string{
			"Content-Type":      "application/json",
			"x-api-key":         c.apiKey,
			"anthropic-version": "2023-06-01",
			"anthropic-beta":    "prompt-caching-2024-07-31",
		})
	if err != nil {
		return nil, err
	}

	var apiResp struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	result := &shared.ChatResponse{
		StopReason: apiResp.StopReason,
		Usage: shared.TokenUsage{
			InputTokens:  apiResp.Usage.InputTokens,
			OutputTokens: apiResp.Usage.OutputTokens,
		},
	}

	for _, block := range apiResp.Content {
		switch block.Type {
		case "text":
			result.Content += block.Text
		case "tool_use":
			result.ToolCalls = append(result.ToolCalls, shared.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				InputJSON: string(block.Input),
			})
		}
	}

	return result, nil
}
