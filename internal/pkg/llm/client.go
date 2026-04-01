package llm

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/antlss/gitlab-review-agent/internal/config"
	"github.com/antlss/gitlab-review-agent/internal/shared"
)

// NewClient creates a single LLM client based on provider and model name.
func NewClient(cfg config.LLMConfig, provider, model string) (shared.LLMClient, error) {
	if provider == "" {
		provider = cfg.DefaultProvider
	}
	if model == "" {
		model = cfg.DefaultModel
	}

	contextWindow := resolveContextWindow(cfg, model)

	switch provider {
	case "openai":
		return NewOpenAIClient(cfg.OpenAIAPIKey, cfg.OpenAIBaseURL, model, contextWindow)
	case "anthropic":
		return NewAnthropicClient(cfg.AnthropicAPIKey, model, contextWindow)
	case "google":
		return NewGoogleClient(cfg.GoogleAPIKey, model, contextWindow)
	default:
		return nil, fmt.Errorf("unknown LLM provider: %s", provider)
	}
}

// NewBalancedClientFromConfig creates a load-balanced LLM client using all
// available API keys for the resolved provider.
//
// All results are wrapped in a BalancedClient regardless of key count so that
// rate-limit rotation and the global concurrency semaphore are always active.
func NewBalancedClientFromConfig(cfg config.LLMConfig, repoModelOverride *string) (shared.LLMClient, error) {
	provider, model := ResolveModel(cfg, repoModelOverride)
	keys := keysForProvider(cfg, provider)
	contextWindow := resolveContextWindow(cfg, model)

	var clients []shared.LLMClient

	if len(keys) == 0 {
		// No multi-key config — fall back to single-key defaults
		c, err := NewClient(cfg, provider, model)
		if err != nil {
			return nil, err
		}
		clients = append(clients, c)
	} else {
		for _, key := range keys {
			c, err := newClientForProvider(provider, key, cfg.OpenAIBaseURL, model, contextWindow)
			if err != nil {
				slog.Warn("balancer: skipping invalid key", "provider", provider, "error", err)
				continue
			}
			clients = append(clients, c)
		}
		if len(clients) == 0 {
			c, err := NewClient(cfg, provider, model)
			if err != nil {
				return nil, err
			}
			clients = append(clients, c)
		}
	}

	slog.Info("created balanced LLM client",
		"provider", provider,
		"model", model,
		"key_count", len(clients),
	)
	return NewBalancedClient(clients)
}

func keysForProvider(cfg config.LLMConfig, provider string) []string {
	switch provider {
	case "openai":
		return cfg.OpenAIAPIKeys
	case "anthropic":
		return cfg.AnthropicAPIKeys
	case "google":
		return cfg.GoogleAPIKeys
	default:
		return nil
	}
}

func newClientForProvider(provider, apiKey, openAIBaseURL, model string, contextWindow int) (shared.LLMClient, error) {
	switch provider {
	case "openai":
		return NewOpenAIClient(apiKey, openAIBaseURL, model, contextWindow)
	case "anthropic":
		return NewAnthropicClient(apiKey, model, contextWindow)
	case "google":
		return NewGoogleClient(apiKey, model, contextWindow)
	default:
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
}

func resolveContextWindow(cfg config.LLMConfig, model string) int {
	if cw, ok := cfg.ContextWindowSizes[model]; ok {
		return cw
	}
	return 128000
}

// ResolveModel determines which model to use based on repo override and defaults.
// Provider is auto-detected from the model name prefix when an override is set.
func ResolveModel(cfg config.LLMConfig, repoOverride *string) (provider, model string) {
	model = cfg.DefaultModel
	provider = cfg.DefaultProvider

	if repoOverride != nil && *repoOverride != "" {
		model = *repoOverride
		switch {
		case strings.HasPrefix(model, "claude-"):
			provider = "anthropic"
		case strings.HasPrefix(model, "gpt-"),
			strings.HasPrefix(model, "o1"),
			strings.HasPrefix(model, "o3"):
			provider = "openai"
		case strings.HasPrefix(model, "gemini-"):
			provider = "google"
		}
	}

	return provider, model
}
