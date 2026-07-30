package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// loadIssue fetches issue metadata (title, labels, state) from GitHub via
// `gh issue view` with the dev token. The issue body is intentionally not
// loaded here: the dev/reviewer agents fetch the authoritative task spec at
// run time via `gm_slice_get`, which keeps the initial prompt
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

// writeIssueBodyFile fetches the issue body and writes it to a temp file so that
// lint-e2e-guard.sh can read it via GOLEMIC_ISSUE_BODY_FILE. Returns the file
// path, or "" when the body cannot be fetched (non-fatal).
func (r *Runner) writeIssueBodyFile() string {
	if r.creds == nil || r.executor == nil {
		return ""
	}
	body, err := r.fetchIssueBody()
	if err != nil {
		fmt.Fprintf(r.stderr, "Warning: could not fetch issue body for E2E guard: %v\n", err) //nolint:errcheck
		return ""
	}
	f, err := os.CreateTemp("", "golemic-issue-body-*.txt")
	if err != nil {
		fmt.Fprintf(r.stderr, "Warning: could not create issue body temp file: %v\n", err) //nolint:errcheck
		return ""
	}
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		os.Remove(f.Name())                                                               //nolint:errcheck
		fmt.Fprintf(r.stderr, "Warning: could not write issue body temp file: %v\n", err) //nolint:errcheck
		return ""
	}
	_ = f.Close()
	return f.Name()
}

// fetchIssueBody fetches the markdown body of the current issue via `gh issue view`.
func (r *Runner) fetchIssueBody() (string, error) {
	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		r.repoRoot,
		"gh", "issue", "view", fmt.Sprintf("%d", r.issueNum), "--json", "body",
	)
	if err != nil {
		return "", fmt.Errorf("gh issue view (body): %w", err)
	}
	var data struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		return "", fmt.Errorf("invalid gh response: %w", err)
	}
	return strings.TrimSpace(data.Body), nil
}
