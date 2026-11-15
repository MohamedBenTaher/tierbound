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
		if _, err := ForProvider(provider, "m", only); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s with only a Groq key: err = %v, want one naming %s", provider, err, want)
		}
	}
	if _, err := ForProvider("", "m", only); err != nil {
		t.Fatalf("default provider is groq: %v", err)
	}
	if _, err := ForProvider("mistral", "m", only); err == nil {
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
	c, err := ForProvider("anthropic", "claude-x", env(map[string]string{"ANTHROPIC_API_KEY": "k", "ANTHROPIC_API_URL": srv.URL + "/v1"}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.DraftComment(context.Background(), "sys", "user")
	if err != nil || out != `{"findings":[]}` {
		t.Fatalf("got %q, %v", out, err)
	}
}
