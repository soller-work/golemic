package runner

import (
	"encoding/json"
	"fmt"
	"strings"
)

const sliceCommentMarker = "<!-- golemic:slice-json v=1 -->"

// loadIssue fetches issue details from GitHub via gh issue view with the dev token.
// If the issue body contains the slice-comment marker, comments are fetched and the
// first matching comment's JSON block is injected into the returned Body (BR-001/BR-005/BR-006).
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

	// BR-005: soft fallback — the marker only triggers comment injection when it appears
	// as its own line (per BR-001, the comment must start with the marker on line 1).
	// Prose mentions of the marker inside backticks or fenced blocks (e.g. legacy issues
	// that document the feature or carry the slice JSON inline) must not trigger a fetch.
	if !bodyRequestsCommentInjection(data.Body) {
		return &issueData{
			Number: r.issueNum,
			Title:  data.Title,
			Body:   data.Body,
			Labels: data.Labels,
		}, nil
	}

	// Marker present in body — fetch comments to locate the slice JSON.
	commentsOut, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		r.repoRoot,
		"gh", "api", fmt.Sprintf("repos/:owner/:repo/issues/%d/comments", r.issueNum),
	)
	if err != nil {
		return nil, fmt.Errorf("SLICE_COMMENT_FETCH_FAILED: %w", err)
	}

	var comments []struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(commentsOut), &comments); err != nil {
		return nil, fmt.Errorf("SLICE_COMMENT_FETCH_FAILED: invalid comments response: %w", err)
	}

	for _, c := range comments {
		if !strings.HasPrefix(c.Body, sliceCommentMarker) {
			continue
		}
		extracted, err := extractFencedJSON(c.Body)
		if err != nil {
			return nil, fmt.Errorf("SLICE_JSON_MALFORMED: comment id=%d: %w", c.ID, err)
		}
		rebuilt := data.Body + "\n\n```json\n" + extracted + "\n```"
		return &issueData{
			Number: r.issueNum,
			Title:  data.Title,
			Body:   rebuilt,
			Labels: data.Labels,
		}, nil
	}

	// Marker in body but no matching comment — anomalous; fail per BR-006.
	return nil, fmt.Errorf("SLICE_JSON_MALFORMED: no comment with marker %q found", sliceCommentMarker)
}

// bodyRequestsCommentInjection reports whether the issue body contains the slice
// marker as its own (trimmed) line — the shape produced by the grill-me compact body.
func bodyRequestsCommentInjection(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == sliceCommentMarker {
			return true
		}
	}
	return false
}

// extractFencedJSON extracts and validates the JSON content from the first ```json
// fenced block in body.
func extractFencedJSON(body string) (string, error) {
	const fence = "```json\n"
	start := strings.Index(body, fence)
	if start == -1 {
		return "", fmt.Errorf("no ```json block found")
	}
	start += len(fence)
	rest := body[start:]
	end := strings.Index(rest, "\n```")
	if end == -1 {
		return "", fmt.Errorf("unclosed ```json block")
	}
	extracted := rest[:end]
	var dummy json.RawMessage
	if err := json.Unmarshal([]byte(extracted), &dummy); err != nil {
		return "", fmt.Errorf("invalid JSON: %w", err)
	}
	return extracted, nil
}
