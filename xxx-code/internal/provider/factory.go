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
)

func Normalize(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	switch name {
	case "", ProviderAnthropic:
		return ProviderAnthropic
	case "azure", "azure_openai", "azure-openai":
		return ProviderAzureOpenAI
	default:
		return name
	}
}

func New(cfg config.Config) engine.Provider {
	switch Normalize(cfg.Provider) {
	case ProviderOpenAI:
		return openaiprovider.NewClient(cfg.APIKey, cfg.BaseURL)
	case ProviderAzureOpenAI:
		return openaiprovider.NewAzureClient(cfg.APIKey, cfg.BaseURL)
	default:
		return anthropic.NewClient(cfg.APIKey, cfg.BaseURL, cfg.Version)
	}
}
