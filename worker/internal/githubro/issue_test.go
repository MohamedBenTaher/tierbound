package githubro

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchIssue(t *testing.T) {
	var gotAuth, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		if r.Method != http.MethodGet || r.URL.Path != "/repos/o/r/issues/7" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"title":"T","body":"B","labels":[{"name":"bug"},{"name":"smoke-test"}]}`)
	}))
	defer srv.Close()

	issue, err := New("tok", srv.URL).FetchIssue(t.Context(), "o/r", 7)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if issue.Repo != "o/r" || issue.Number != 7 || issue.Title != "T" || issue.Body != "B" {
		t.Fatalf("issue: %+v", issue)
	}
	if len(issue.Labels) != 2 || issue.Labels[0] != "bug" || issue.Labels[1] != "smoke-test" {
		t.Fatalf("labels: %v", issue.Labels)
	}
	if gotAuth != "Bearer tok" || gotAccept != "application/vnd.github+json" {
		t.Fatalf("headers: auth %q accept %q", gotAuth, gotAccept)
	}

	if _, err := New("", srv.URL).FetchIssue(t.Context(), "not-a-repo", 7); err == nil {
		t.Fatal("bad repo accepted")
	}
	if _, err := New("", srv.URL).FetchIssue(t.Context(), "o/r", 9); err == nil {
		t.Fatal("missing issue accepted")
	}
}
