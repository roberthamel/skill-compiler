package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/roberthamel/skill-compiler/internal/config"
)

// GenerateRequest is the input to an LLM generation call.
type GenerateRequest struct {
	SystemPrompt string
	UserMessage  string
	MaxTokens    int
	Model        string
}

// GenerateResponse is the output from an LLM generation call.
type GenerateResponse struct {
	Content    string
	Model      string
	TokensIn   int
	TokensOut  int
}

// Provider is the interface for LLM providers.
type Provider interface {
	Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error)
	Name() string
}

// New creates a provider from resolved config.
func New(resolved *config.Resolved) (Provider, error) {
	name := strings.ToLower(resolved.Provider)
	baseURL := resolved.BaseURL
	apiKey := resolved.APIKey
	model := resolved.Model

	switch {
	case name == "anthropic" || (name == "" && baseURL == ""):
		if apiKey == "" {
			return nil, fmt.Errorf("API key required: set SC_API_KEY, ANTHROPIC_API_KEY, or run `sc config set api-key <key>`")
		}
		if model == "" {
			model = "claude-sonnet-4-6"
		}
		url := baseURL
		if url == "" {
			url = "https://api.anthropic.com"
		}
		return &Anthropic{apiKey: apiKey, model: model, baseURL: url}, nil

	case name == "openai":
		if apiKey == "" {
			return nil, fmt.Errorf("API key required: set SC_API_KEY, OPENAI_API_KEY, or run `sc config set api-key <key>`")
		}
		if model == "" {
			model = "gpt-4o"
		}
		url := baseURL
		if url == "" {
			url = "https://api.openai.com"
		}
		return &OpenAI{apiKey: apiKey, model: model, baseURL: url}, nil

	case baseURL != "":
		// Custom endpoint — determine protocol from provider name hint
		if apiKey == "" {
			return nil, fmt.Errorf("API key required for custom provider")
		}
		if strings.Contains(name, "anthropic") {
			if model == "" {
				model = "claude-sonnet-4-6"
			}
			return &Anthropic{apiKey: apiKey, model: model, baseURL: baseURL}, nil
		}
		// Default to OpenAI protocol for custom endpoints
		if model == "" {
			model = "gpt-4o"
		}
		return &OpenAI{apiKey: apiKey, model: model, baseURL: baseURL}, nil

	default:
		return nil, fmt.Errorf("unknown provider %q (supported: anthropic, openai, or set base-url for custom)", name)
	}
}
