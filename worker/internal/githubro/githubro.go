// Package githubro fetches GitHub issues with a read-only token. It performs
// GET requests only; this package must never hold write capability.
package githubro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Issue is the issue state the drafter needs.
type Issue struct {
	Repo   string
	Number int
	Title  string
	Body   string
	Labels []string
}

// Client is a read-only GitHub REST client.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

// New returns a read-only GitHub client. Empty token means unauthenticated;
// empty baseURL means https://api.github.com (set GITHUB_API_URL for GitHub
// Enterprise or a local stub).
func New(token, baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &Client{
		token:   token,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// FetchIssue fetches one issue. repo is "owner/name".
func (c *Client) FetchIssue(ctx context.Context, repo string, number int) (Issue, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Issue{}, fmt.Errorf("githubro: repo %q: want owner/name", repo)
	}
	u := fmt.Sprintf("%s/repos/%s/%s/issues/%d",
		c.baseURL, url.PathEscape(parts[0]), url.PathEscape(parts[1]), number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Issue{}, fmt.Errorf("githubro: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Issue{}, fmt.Errorf("githubro: fetch %s#%d: %w", repo, number, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Issue{}, fmt.Errorf("githubro: fetch %s#%d: status %d: %s", repo, number, resp.StatusCode, body)
	}
	var payload struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Issue{}, fmt.Errorf("githubro: decode %s#%d: %w", repo, number, err)
	}
	issue := Issue{Repo: repo, Number: number, Title: payload.Title, Body: payload.Body}
	for _, label := range payload.Labels {
		issue.Labels = append(issue.Labels, label.Name)
	}
	return issue, nil
}
