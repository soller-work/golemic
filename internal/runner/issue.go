package runner

import (
	"encoding/json"
	"fmt"
)

// loadIssue fetches issue details from GitHub via gh issue view with the dev token.
func (r *Runner) loadIssue() (*issueData, error) {
	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		r.repoRoot,
			"gh", "issue", "view", fmt.Sprintf("%d", r.issueNum), "--json", "title,body,labels",
	)
	if err != nil {
		return nil, fmt.Errorf("gh issue view: %w", err)
	}

	var data struct {
		Title  string       `json:"title"`
		Body   string       `json:"body"`
		Labels []issueLabel `json:"labels"`
	}
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		return nil, fmt.Errorf("invalid gh response: %w", err)
	}

	return &issueData{
		Number: r.issueNum,
		Title:  data.Title,
		Body:   data.Body,
		Labels: data.Labels,
	}, nil
}
