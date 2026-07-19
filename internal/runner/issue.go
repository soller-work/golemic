package runner

import (
	"encoding/json"
	"fmt"
)

// loadIssue fetches issue metadata (title, labels, state) from GitHub via
// `gh issue view` with the dev token. The issue body is intentionally not
// loaded here: the dev/reviewer agents fetch the authoritative task spec at
// run time via `golemic slice --issue N`, which keeps the initial prompt
// small on large slices.
func (r *Runner) loadIssue() (*issueData, error) {
	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		r.repoRoot,
		"gh", "issue", "view", fmt.Sprintf("%d", r.issueNum), "--json", "title,labels,state",
	)
	if err != nil {
		return nil, fmt.Errorf("gh issue view: %w", err)
	}

	var data struct {
		Title  string       `json:"title"`
		Labels []issueLabel `json:"labels"`
		State  string       `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		return nil, fmt.Errorf("invalid gh response: %w", err)
	}
	return &issueData{
		Number: r.issueNum,
		Title:  data.Title,
		Labels: data.Labels,
		State:  data.State,
	}, nil
}
