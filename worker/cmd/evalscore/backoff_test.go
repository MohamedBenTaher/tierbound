package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MohamedBenTaher/tierbound/worker/internal/llm"
)

// Recording a golden set is a burst of calls; a 429 from OpenAI is retried
// with backoff exactly like Groq's, and other errors are not.
func TestDraftBackoffRetriesOpenAI429(t *testing.T) {
	backoffUnit = time.Millisecond
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"Rate limit reached"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	env := map[string]string{"OPENAI_API_KEY": "k", "OPENAI_API_URL": srv.URL}
	c, err := llm.ForProvider("openai", "m", func(k string) string { return env[k] }, llm.Options{JSON: true})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := draftWithBackoff(c, "p", "m"); err != nil || out != "{}" || calls.Load() != 3 {
		t.Fatalf("out %q err %v calls %d, want success on the 3rd call", out, err, calls.Load())
	}

	// A non-429 failure (bad key) is returned at once, not retried.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer bad.Close()
	env["OPENAI_API_URL"] = bad.URL
	c, _ = llm.ForProvider("openai", "m", func(k string) string { return env[k] }, llm.Options{})
	calls.Store(0)
	if _, err := draftWithBackoff(c, "p", "m"); err == nil || calls.Load() != 1 {
		t.Fatalf("401: err %v calls %d, want one call and an error", err, calls.Load())
	}
}
