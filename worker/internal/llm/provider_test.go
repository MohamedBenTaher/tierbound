package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestForProviderNeedsItsOwnKey(t *testing.T) {
	// Only a Groq key present: anthropic and openai must refuse, naming
	// their variable, never fall back to Groq.
	only := env(map[string]string{"GROQ_API_KEY": "g"})
	for provider, want := range map[string]string{"anthropic": "ANTHROPIC_API_KEY", "openai": "OPENAI_API_KEY"} {
		if _, err := ForProvider(provider, "m", only, Options{}); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s with only a Groq key: err = %v, want one naming %s", provider, err, want)
		}
	}
	if _, err := ForProvider("", "m", only, Options{}); err != nil {
		t.Fatalf("default provider is groq: %v", err)
	}
	if _, err := ForProvider("mistral", "m", only, Options{}); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestAnthropicMessagesRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model    string `json:"model"`
			System   string `json:"system"`
			Messages []struct{ Role, Content string }
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		ok := r.URL.Path == "/v1/messages" && r.Header.Get("x-api-key") == "k" &&
			r.Header.Get("anthropic-version") == anthropicVersion && in.Model == "claude-x" &&
			in.System == "sys" && len(in.Messages) == 1 && in.Messages[0].Content == "user"
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{\"findings\":"},{"type":"text","text":"[]}"}]}`))
	}))
	defer srv.Close()
	c, err := ForProvider("anthropic", "claude-x", env(map[string]string{"ANTHROPIC_API_KEY": "k", "ANTHROPIC_API_URL": srv.URL + "/v1"}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.DraftComment(context.Background(), "sys", "user")
	if err != nil || out != `{"findings":[]}` {
		t.Fatalf("got %q, %v", out, err)
	}
}

// The OpenAI path: bearer key, chat completions, JSON mode when the agent
// needs strict JSON, a reasoning-sized output cap; Groq keeps its v1 body.
func TestOpenAIChatRequest(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer ok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		got = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"findings\":[]}"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	vars := env(map[string]string{"OPENAI_API_KEY": "ok", "OPENAI_API_URL": srv.URL + "/v1", "GROQ_API_KEY": "ok", "GROQ_API_URL": srv.URL + "/v1"})

	c, err := ForProvider("openai", "gpt-x", vars, Options{JSON: true})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := c.DraftComment(context.Background(), "sys", "user"); err != nil || out != `{"findings":[]}` {
		t.Fatalf("openai: %q %v", out, err)
	}
	rf, _ := got["response_format"].(map[string]any)
	if got["model"] != "gpt-x" || rf["type"] != "json_object" || got["max_completion_tokens"] != float64(openAIMaxCompletionTokens) {
		t.Fatalf("openai request body: %v", got)
	}

	g, _ := ForProvider("groq", "m", vars, Options{JSON: true})
	if _, err := g.DraftComment(context.Background(), "sys", "user"); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["response_format"]; ok || got["max_completion_tokens"] != nil {
		t.Fatalf("groq body changed: %v", got)
	}
}

// A reasoning model that spends its whole budget returns empty content;
// the error says why instead of "empty completion".
func TestOpenAIEmptyContentNamesFinishReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""},"finish_reason":"length"}]}`))
	}))
	defer srv.Close()
	c, _ := ForProvider("openai", "m", env(map[string]string{"OPENAI_API_KEY": "k", "OPENAI_API_URL": srv.URL}), Options{})
	if _, err := c.DraftComment(context.Background(), "s", "u"); err == nil || !strings.Contains(err.Error(), `"length"`) {
		t.Fatalf("err = %v", err)
	}
}
