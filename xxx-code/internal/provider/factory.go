package provider

import (
	"strings"

	"github.com/caowenhua/x-agent/xxx-code/internal/config"
	"github.com/caowenhua/x-agent/xxx-code/internal/engine"
	"github.com/caowenhua/x-agent/xxx-code/internal/provider/anthropic"
	openaiprovider "github.com/caowenhua/x-agent/xxx-code/internal/provider/openai"
)

const (
	ProviderAnthropic   = "anthropic"
	ProviderOpenAI      = "openai"
	ProviderAzureOpenAI = "azure-openai"
	ProviderGemini      = "gemini"
	ProviderMiniMax     = "minimax"
	ProviderGLM         = "glm"
)

const (
	defaultOpenAIBaseURL  = "https://api.openai.com/v1"
	defaultGeminiBaseURL  = "https://generativelanguage.googleapis.com/v1beta/openai"
	defaultMiniMaxBaseURL = "https://api.minimaxi.com/v1"
	defaultGLMBaseURL     = "https://open.bigmodel.cn/api/coding/paas/v4"
)

func Normalize(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	switch name {
	case "", ProviderAnthropic:
		return ProviderAnthropic
	case "gpt", "chatgpt", ProviderOpenAI:
		return ProviderOpenAI
	case "azure", "azure_openai", "azure-openai":
		return ProviderAzureOpenAI
	case "gemini", "google":
		return ProviderGemini
	case "minimax", "mini-max", "mini_max":
		return ProviderMiniMax
	case "glm", "zhipu", "z-ai", "zai":
		return ProviderGLM
	default:
		return name
	}
}

func New(cfg config.Config) engine.Provider {
	switch Normalize(cfg.Provider) {
	case ProviderOpenAI:
		return openaiprovider.NewClient(cfg.APIKey, firstNonEmpty(cfg.BaseURL, defaultOpenAIBaseURL))
	case ProviderAzureOpenAI:
		return openaiprovider.NewAzureClient(cfg.APIKey, cfg.BaseURL)
	case ProviderGemini:
		return openaiprovider.NewClient(cfg.APIKey, firstNonEmpty(cfg.BaseURL, defaultGeminiBaseURL))
	case ProviderMiniMax:
		return openaiprovider.NewClient(cfg.APIKey, firstNonEmpty(cfg.BaseURL, defaultMiniMaxBaseURL))
	case ProviderGLM:
		return openaiprovider.NewClient(cfg.APIKey, firstNonEmpty(cfg.BaseURL, defaultGLMBaseURL))
	default:
		return anthropic.NewClient(cfg.APIKey, cfg.BaseURL, cfg.Version)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
