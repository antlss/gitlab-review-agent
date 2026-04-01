package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/antlss/gitlab-review-agent/internal/shared"
)

type GoogleClient struct {
	apiKey        string
	model         string
	contextWindow int
	httpClient    *http.Client
}

func NewGoogleClient(apiKey, model string, contextWindow int) (*GoogleClient, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("Google API key is required")
	}
	return &GoogleClient{
		apiKey:        apiKey,
		model:         model,
		contextWindow: contextWindow,
		httpClient:    &http.Client{Timeout: 5 * time.Minute},
	}, nil
}

func (c *GoogleClient) ModelName() string      { return c.model }
func (c *GoogleClient) ContextWindowSize() int { return c.contextWindow }
func (c *GoogleClient) ClientCount() int       { return 1 }

func (c *GoogleClient) Chat(ctx context.Context, req shared.ChatRequest) (*shared.ChatResponse, error) {
	// Build Gemini request
	var contents []map[string]any
	var systemInstruction *map[string]any

	for _, msg := range req.Messages {
		switch msg.Role {
		case "system":
			si := map[string]any{
				"parts": []map[string]any{{"text": msg.Content}},
			}
			systemInstruction = &si
		case "user":
			contents = append(contents, map[string]any{
				"role":  "user",
				"parts": []map[string]any{{"text": msg.Content}},
			})
		case "assistant":
			var parts []map[string]any
			if msg.Content != "" {
				parts = append(parts, map[string]any{"text": msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				var args any
				json.Unmarshal([]byte(tc.InputJSON), &args)
				parts = append(parts, map[string]any{
					"functionCall": map[string]any{
						"name": tc.Name,
						"args": args,
					},
				})
			}
			contents = append(contents, map[string]any{
				"role":  "model",
				"parts": parts,
			})
		case "tool":
			var respData any
			json.Unmarshal([]byte(msg.Content), &respData)
			if respData == nil {
				respData = map[string]any{"result": msg.Content}
			}
			contents = append(contents, map[string]any{
				"role": "user",
				"parts": []map[string]any{{
					"functionResponse": map[string]any{
						"name":     msg.ToolCallID,
						"response": respData,
					},
				}},
			})
		}
	}

	body := map[string]any{
		"contents": contents,
	}
	if systemInstruction != nil {
		body["systemInstruction"] = *systemInstruction
	}

	genConfig := map[string]any{}
	if req.MaxTokens > 0 {
		genConfig["maxOutputTokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		genConfig["temperature"] = req.Temperature
	}
	if len(genConfig) > 0 {
		body["generationConfig"] = genConfig
	}

	if len(req.Tools) > 0 {
		var funcDecls []map[string]any
		for _, t := range req.Tools {
			funcDecls = append(funcDecls, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.InputSchema,
			})
		}
		body["tools"] = []map[string]any{{"functionDeclarations": funcDecls}}
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	apiURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		c.model, c.apiKey)

	respBody, err := doRequest(ctx, c.httpClient, "POST", apiURL, data,
		map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return nil, err
	}

	var apiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					FunctionCall *struct {
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	result := &shared.ChatResponse{
		StopReason: "stop",
		Usage: shared.TokenUsage{
			InputTokens:  apiResp.UsageMetadata.PromptTokenCount,
			OutputTokens: apiResp.UsageMetadata.CandidatesTokenCount,
		},
	}

	if len(apiResp.Candidates) > 0 {
		for _, part := range apiResp.Candidates[0].Content.Parts {
			if part.Text != "" {
				result.Content += part.Text
			}
			if part.FunctionCall != nil {
				result.ToolCalls = append(result.ToolCalls, shared.ToolCall{
					ID:        part.FunctionCall.Name,
					Name:      part.FunctionCall.Name,
					InputJSON: string(part.FunctionCall.Args),
				})
			}
		}
		if len(result.ToolCalls) > 0 {
			result.StopReason = "tool_use"
		}
	}

	return result, nil
}
