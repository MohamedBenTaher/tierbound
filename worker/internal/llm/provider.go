package llm

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
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

// Options are per-agent request settings derived from the (hashed) config.
type Options struct {
	// JSON: the agent's reply must be one strict JSON object (issue and
	// review agents). Providers with a JSON mode turn it on.
	JSON bool
}

type providerSpec struct {
	keyEnv, urlEnv, defaultURL string
	build                      func(key, model, baseURL string, o Options) Completer
}

// groqClient: the v1 path, unchanged (no JSON mode, 60 s timeout), so the
// recorded triage and issue-drafter outputs stay representative.
func groqClient(k, m, u string, _ Options) Completer { return New(k, m, u) }

// OpenAI reasoning models spend output tokens on reasoning before the
// answer: OpenAI suggests reserving at least 25k. A review over a large diff
// can take minutes, so the timeout is longer than Groq's.
const (
	openAIMaxCompletionTokens = 25000
	openAITimeout             = 240 * time.Second
)

func openAIClient(k, m, u string, o Options) Completer {
	c := New(k, m, u)
	c.http = &http.Client{Timeout: openAITimeout}
	c.jsonObject = o.JSON
	c.maxCompletionTokens = openAIMaxCompletionTokens
	return c
}

func anthropicClient(k, m, u string, _ Options) Completer { return NewAnthropic(k, m, u) }

var providers = map[string]providerSpec{
	ProviderGroq: {
		keyEnv: "GROQ_API_KEY", urlEnv: "GROQ_API_URL",
		defaultURL: "https://api.groq.com/openai/v1", build: groqClient,
	},
	ProviderOpenAI: {
		keyEnv: "OPENAI_API_KEY", urlEnv: "OPENAI_API_URL",
		defaultURL: "https://api.openai.com/v1", build: openAIClient,
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
func ForProvider(provider, model string, getenv func(string) string, o Options) (Completer, error) {
	spec, ok := providers[normalize(provider)]
	if !ok {
		return nil, fmt.Errorf("llm: unknown provider %q", provider)
	}
	key := getenv(spec.keyEnv)
	if key == "" {
		return nil, fmt.Errorf("llm: %s is not set (provider %s)", spec.keyEnv, normalize(provider))
	}
	url := cmp.Or(getenv(spec.urlEnv), spec.defaultURL)
	return spec.build(key, model, strings.TrimRight(url, "/"), o), nil
}

func normalize(provider string) string {
	return cmp.Or(provider, ProviderGroq)
}
