package githubro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrNotFound is a 404 from GitHub (a rule file absent at the base sha).
var ErrNotFound = errors.New("githubro: not found")

// maxFilePages bounds GET /pulls/{n}/files (100 per page; GitHub caps the
// list at 3000 files).
const maxFilePages = 30

// PRFile is one changed file of a pull request. Patch is empty for binary
// or very large files; such files cannot be commented on.
type PRFile struct {
	Path   string `json:"filename"`
	Status string `json:"status"`
	Patch  string `json:"patch"`
}

func repoPath(repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("githubro: repo %q: want owner/name", repo)
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

// get performs one authenticated GET and returns the body of a 200.
func (c *Client) get(ctx context.Context, path, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("githubro: %w", err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("githubro: GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: GET %s", ErrNotFound, path)
	case resp.StatusCode != http.StatusOK:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("githubro: GET %s: status %d: %s", path, resp.StatusCode, b)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// FetchPRFiles lists a pull request's changed files with their patches.
func (c *Client) FetchPRFiles(ctx context.Context, repo string, number int) ([]PRFile, error) {
	base, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	var all []PRFile
	for page := 1; page <= maxFilePages; page++ {
		b, err := c.get(ctx, fmt.Sprintf("%s/pulls/%d/files?per_page=100&page=%d", base, number, page), "application/vnd.github+json")
		if err != nil {
			return nil, err
		}
		var files []PRFile
		if err := json.Unmarshal(b, &files); err != nil {
			return nil, fmt.Errorf("githubro: decode files of %s#%d: %w", repo, number, err)
		}
		all = append(all, files...)
		if len(files) < 100 {
			break
		}
	}
	return all, nil
}

// FetchFile returns one file's raw content at ref (a commit sha).
func (c *Client) FetchFile(ctx context.Context, repo, path, ref string) (string, error) {
	base, err := repoPath(repo)
	if err != nil {
		return "", err
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	b, err := c.get(ctx, base+"/contents/"+strings.Join(segs, "/")+"?ref="+url.QueryEscape(ref), "application/vnd.github.raw+json")
	return string(b), err
}
