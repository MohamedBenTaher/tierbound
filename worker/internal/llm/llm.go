// Package llm drafts issue comments through an OpenAI-compatible chat API
// (Groq in v1). It is pure transport: prompt construction stays in propose.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is an OpenAI-compatible chat client.
type Client struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
}

// New returns a chat client for model. Empty baseURL means
// https://api.groq.com/openai/v1 (set GROQ_API_URL for a local stub).
func New(apiKey, model, baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://api.groq.com/openai/v1"
	}
	return &Client{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

// DraftComment asks the model for one comment body and returns it verbatim.
func (c *Client) DraftComment(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	payload := map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userMessage},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("llm: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("llm: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm: chat completion: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("llm: chat completion: status %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("llm: decode response: %w", err)
	}
	if len(out.Choices) == 0 || out.Choices[0].Message.Content == "" {
		return "", fmt.Errorf("llm: empty completion")
	}
	return out.Choices[0].Message.Content, nil
}
