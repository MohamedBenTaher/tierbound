// Package githubro is a read-only GitHub REST client. It performs GET
// requests only; this package must never hold write capability.
package githubro

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	return &Client{
		token:   token,
		baseURL: strings.TrimRight(cmp.Or(baseURL, "https://api.github.com"), "/"),
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// FetchIssue fetches one issue. repo is "owner/name".
func (c *Client) FetchIssue(ctx context.Context, repo string, number int) (Issue, error) {
	base, err := repoPath(repo)
	if err != nil {
		return Issue{}, err
	}
	body, err := c.get(ctx, fmt.Sprintf("%s/issues/%d", base, number), "application/vnd.github+json")
	if err != nil {
		return Issue{}, err
	}
	var payload struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Issue{}, fmt.Errorf("githubro: decode %s#%d: %w", repo, number, err)
	}
	issue := Issue{Repo: repo, Number: number, Title: payload.Title, Body: payload.Body}
	for _, label := range payload.Labels {
		issue.Labels = append(issue.Labels, label.Name)
	}
	return issue, nil
}
