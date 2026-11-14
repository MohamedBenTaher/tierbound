package propose

import (
	"errors"
	"fmt"

	"github.com/MohamedBenTaher/tierbound/worker/internal/agentconfig"
	"github.com/MohamedBenTaher/tierbound/worker/internal/issuedraft"
)

// proposal is one action envelope before hashing: what the human approves.
type proposal struct {
	kind    string
	target  map[string]any
	payload map[string]any
}

// errAbstained: the agent read the input and declined to propose. The run
// is recorded as skipped and the job archived; it is not retried.
var errAbstained = errors.New("agent abstained")

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
	d, err := issuedraft.Parse(draft)
	if err != nil {
		return proposal{}, err
	}
	if d.Abstained() {
		return proposal{}, fmt.Errorf("%w: %s", errAbstained, d.Abstain)
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
