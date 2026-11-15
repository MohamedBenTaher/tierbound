package llm

import (
	"context"
	"fmt"
	"strings"
)

// Provider names (agent config `provider`, docs/V2_ACTIONS.md section 11).
const (
	ProviderGroq      = "groq"      // default; OpenAI-compatible API at Groq
	ProviderOpenAI    = "openai"    // OpenAI chat completions
	ProviderAnthropic = "anthropic" // Anthropic Messages API
)

// Completer is the one call every provider implements: a system prompt and
// one user message in, the model's text out.
type Completer interface {
	DraftComment(ctx context.Context, systemPrompt, userMessage string) (string, error)
}

type providerSpec struct {
	keyEnv, urlEnv, defaultURL string
	build                      func(key, model, baseURL string) Completer
}

func openAICompatible(k, m, u string) Completer { return New(k, m, u) }

func anthropicClient(k, m, u string) Completer { return NewAnthropic(k, m, u) }

var providers = map[string]providerSpec{
	ProviderGroq: {
		keyEnv: "GROQ_API_KEY", urlEnv: "GROQ_API_URL",
		defaultURL: "https://api.groq.com/openai/v1", build: openAICompatible,
	},
	ProviderOpenAI: {
		keyEnv: "OPENAI_API_KEY", urlEnv: "OPENAI_API_URL",
		defaultURL: "https://api.openai.com/v1", build: openAICompatible,
	},
	ProviderAnthropic: {
		keyEnv: "ANTHROPIC_API_KEY", urlEnv: "ANTHROPIC_API_URL",
		defaultURL: "https://api.anthropic.com/v1", build: anthropicClient,
	},
}

// KeyEnv is the environment variable holding the API key for provider.
func KeyEnv(provider string) (string, error) {
	spec, ok := providers[normalize(provider)]
	if !ok {
		return "", fmt.Errorf("llm: unknown provider %q", provider)
	}
	return spec.keyEnv, nil
}

// ForProvider returns the client for provider and model, reading its key and
// optional base URL through getenv. A missing key is an error naming the
// variable: there is no fallback to another provider.
func ForProvider(provider, model string, getenv func(string) string) (Completer, error) {
	spec, ok := providers[normalize(provider)]
	if !ok {
		return nil, fmt.Errorf("llm: unknown provider %q", provider)
	}
	key := getenv(spec.keyEnv)
	if key == "" {
		return nil, fmt.Errorf("llm: %s is not set (provider %s)", spec.keyEnv, normalize(provider))
	}
	url := getenv(spec.urlEnv)
	if url == "" {
		url = spec.defaultURL
	}
	return spec.build(key, model, strings.TrimRight(url, "/")), nil
}

func normalize(provider string) string {
	if provider == "" {
		return ProviderGroq
	}
	return provider
}
