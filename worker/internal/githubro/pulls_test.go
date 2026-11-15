package githubro

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchPRFilesPagesAndFetchFileAtRef(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method != http.MethodGet:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case r.URL.Path == "/repos/o/r/pulls/7/files" && r.URL.Query().Get("page") == "1":
			files := make([]string, 100)
			for i := range files {
				files[i] = fmt.Sprintf(`{"filename":"f%d.go","status":"modified","patch":"@@ -1 +1 @@\n+x"}`, i)
			}
			_, _ = fmt.Fprintf(w, "[%s]", strings.Join(files, ","))
		case r.URL.Path == "/repos/o/r/pulls/7/files":
			_, _ = w.Write([]byte(`[{"filename":"last.go","status":"added","patch":"@@ -0,0 +1 @@\n+y"}]`))
		case r.URL.Path == "/repos/o/r/contents/docs/AGENTS.md" && r.URL.Query().Get("ref") == "abc" &&
			r.Header.Get("Accept") == "application/vnd.github.raw+json":
			_, _ = w.Write([]byte("# rules"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := New("", srv.URL)
	ctx := context.Background()

	files, err := c.FetchPRFiles(ctx, "o/r", 7)
	if err != nil || len(files) != 101 || files[100].Path != "last.go" {
		t.Fatalf("files: %d %v", len(files), err)
	}
	text, err := c.FetchFile(ctx, "o/r", "docs/AGENTS.md", "abc")
	if err != nil || text != "# rules" {
		t.Fatalf("file: %q %v", text, err)
	}
	if _, err := c.FetchFile(ctx, "o/r", "missing.md", "abc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file: %v, want ErrNotFound", err)
	}
	if _, err := c.FetchPRFiles(ctx, "not-a-repo", 7); err == nil {
		t.Fatal("bad repo accepted")
	}
}
