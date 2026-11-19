// Package llm drafts through a model provider: an OpenAI-compatible chat API
// (Groq in v1, OpenAI) or Anthropic's Messages API. It is pure transport:
// prompt construction stays in propose.
package llm

import (
	"bytes"
	"cmp"
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
	// jsonObject sets response_format {"type":"json_object"}: the reply is
	// one JSON object (OpenAI JSON mode).
	jsonObject bool
	// maxCompletionTokens caps output, reasoning tokens included; 0 omits it.
	maxCompletionTokens int
}

// New returns a chat client for model. Empty baseURL means
// https://api.groq.com/openai/v1 (set GROQ_API_URL for a local stub).
func New(apiKey, model, baseURL string) *Client {
	baseURL = cmp.Or(baseURL, "https://api.groq.com/openai/v1")
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
	if c.jsonObject {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}
	if c.maxCompletionTokens > 0 {
		payload["max_completion_tokens"] = c.maxCompletionTokens
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
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("llm: decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("llm: empty completion")
	}
	if out.Choices[0].Message.Content == "" {
		// "length": reasoning used the whole max_completion_tokens budget.
		return "", fmt.Errorf("llm: empty completion (finish_reason %q)", out.Choices[0].FinishReason)
	}
	return out.Choices[0].Message.Content, nil
}
