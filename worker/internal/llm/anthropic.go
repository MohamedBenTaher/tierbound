package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	anthropicVersion   = "2023-06-01"
	anthropicMaxTokens = 4096
)

// Anthropic is a client for the Anthropic Messages API.
type Anthropic struct {
	apiKey, baseURL, model string
	http                   *http.Client
}

// NewAnthropic returns a Messages API client; baseURL like
// https://api.anthropic.com/v1 (ANTHROPIC_API_URL for a local stub).
func NewAnthropic(apiKey, model, baseURL string) *Anthropic {
	return &Anthropic{
		apiKey:  apiKey,
		model:   model,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

// DraftComment sends one system prompt and one user message and returns the
// concatenated text blocks of the reply.
func (a *Anthropic) DraftComment(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model":      a.model,
		"max_tokens": anthropicMaxTokens,
		"system":     systemPrompt,
		"messages":   []map[string]string{{"role": "user", "content": userMessage}},
	})
	if err != nil {
		return "", fmt.Errorf("llm: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("llm: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	resp, err := a.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm: messages: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("llm: messages: status %d: %s", resp.StatusCode, b)
	}
	return anthropicText(resp.Body)
}

func anthropicText(r io.Reader) (string, error) {
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		return "", fmt.Errorf("llm: decode response: %w", err)
	}
	var b strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	if b.Len() == 0 {
		return "", errors.New("llm: empty completion")
	}
	return b.String(), nil
}
