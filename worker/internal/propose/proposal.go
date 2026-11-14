package propose

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MohamedBenTaher/tierbound/worker/internal/agentconfig"
)

// Issue draft limits (V2_ACTIONS 4.1). The effector re-checks the payload,
// and the labels against the repo, before it posts.
const (
	maxIssueTitle  = 80
	maxIssueLabels = 10
)

// proposal is one action envelope before hashing: what the human approves.
type proposal struct {
	kind    string
	target  map[string]any
	payload map[string]any
}

// IssueDraft is the JSON an issue-proposing agent must reply with.
type IssueDraft struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels"`
}

// kindFor maps an agent config to the one kind it proposes: the tool grant
// says so (and is part of config_hash), so a different kind is a different
// config that must pass the eval gate again.
func kindFor(cfg agentconfig.Config) string {
	if cfg.Grants(agentconfig.GrantProposeIssue) {
		return KindGitHubIssue
	}
	return KindGitHubComment
}

// buildProposal turns the model's draft into the action envelope for kind.
func buildProposal(kind string, job Job, draft string) (proposal, error) {
	if kind != KindGitHubIssue {
		return proposal{
			kind:    KindGitHubComment,
			target:  map[string]any{"repo": job.Repo, "issue_number": job.IssueNumber},
			payload: map[string]any{"body": draft},
		}, nil
	}
	d, err := ParseIssueDraft(draft)
	if err != nil {
		return proposal{}, err
	}
	labels := make([]any, len(d.Labels))
	for i, l := range d.Labels {
		labels[i] = l
	}
	return proposal{
		kind:    KindGitHubIssue,
		target:  map[string]any{"repo": job.Repo},
		payload: map[string]any{"title": d.Title, "body": d.Body, "labels": labels},
	}, nil
}

// ParseIssueDraft strictly decodes and validates an issue draft: one JSON
// object, no unknown fields, a one-line title of at most 80 characters, a
// non-empty body, and at most 10 non-empty labels. A bad draft is never
// proposed; the job retries and is dead-lettered after MaxAttempts.
func ParseIssueDraft(s string) (IssueDraft, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(strings.TrimSpace(s))))
	dec.DisallowUnknownFields()
	var d IssueDraft
	if err := dec.Decode(&d); err != nil {
		return IssueDraft{}, fmt.Errorf("issue draft is not the expected JSON object: %w", err)
	}
	if dec.More() {
		return IssueDraft{}, errors.New("issue draft has trailing content after the JSON object")
	}
	d.Title = strings.TrimSpace(d.Title)
	d.Body = strings.TrimSpace(d.Body)
	if d.Labels == nil {
		d.Labels = []string{}
	}
	return d, d.validate()
}

func (d IssueDraft) validate() error {
	n := utf8.RuneCountInString(d.Title)
	switch {
	case n == 0 || n > maxIssueTitle || strings.ContainsAny(d.Title, "\r\n"):
		return fmt.Errorf("issue draft title must be one line of 1-%d characters", maxIssueTitle)
	case d.Body == "":
		return errors.New("issue draft body is empty")
	case len(d.Labels) > maxIssueLabels:
		return fmt.Errorf("issue draft has more than %d labels", maxIssueLabels)
	}
	for _, l := range d.Labels {
		if strings.TrimSpace(l) == "" {
			return errors.New("issue draft has an empty label")
		}
	}
	return nil
}
