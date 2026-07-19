// Package slice extracts the authoritative task specification for an issue.
//
// Priority (first match wins):
//  1. Bot-comment JSON: the issue body contains the marker on its own line, and
//     the first issue comment whose body begins with the marker carries the
//     slice JSON in a ```json fenced block. Returns that JSON verbatim.
//  2. Inline JSON: the issue body itself contains a ```json fenced block
//     (legacy grill-me format). Returns that JSON verbatim.
//  3. Prose fallback: neither of the above. Returns the issue body verbatim,
//     so hand-written issues remain usable.
package slice

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Marker is the well-known HTML comment that a compact issue body places on its
// own line to signal "the slice JSON lives in the first matching bot comment".
const Marker = "<!-- golemic:slice-json v=1 -->"

// Executor is the minimal contract required to invoke gh; matches
// preflight.Executor without a package dependency.
type Executor interface {
	RunWithEnvInDir(env map[string]string, dir string, name string, args ...string) (string, error)
}

// Extract returns the authoritative task text for issueNum.
//
// The returned string is either raw JSON (cases 1 and 2) or the issue body
// prose (case 3). Callers do not need to distinguish — both are valid inputs
// for a dev agent, and the agent detects the shape.
func Extract(executor Executor, repoRoot, ghToken string, issueNum int) (string, error) {
	env := map[string]string{"GH_TOKEN": ghToken}

	viewOut, err := executor.RunWithEnvInDir(env, repoRoot,
		"gh", "issue", "view", fmt.Sprintf("%d", issueNum), "--json", "body")
	if err != nil {
		return "", fmt.Errorf("gh issue view: %w", err)
	}
	var view struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(viewOut), &view); err != nil {
		return "", fmt.Errorf("invalid gh response: %w", err)
	}

	if bodyRequestsCommentInjection(view.Body) {
		return extractFromComments(executor, env, repoRoot, issueNum)
	}

	if inline, ok := tryExtractFencedJSON(view.Body); ok {
		return inline, nil
	}

	return view.Body, nil
}

func extractFromComments(executor Executor, env map[string]string, repoRoot string, issueNum int) (string, error) {
	commentsOut, err := executor.RunWithEnvInDir(env, repoRoot,
		"gh", "api", fmt.Sprintf("repos/:owner/:repo/issues/%d/comments", issueNum))
	if err != nil {
		return "", fmt.Errorf("SLICE_COMMENT_FETCH_FAILED: %w", err)
	}
	var comments []struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(commentsOut), &comments); err != nil {
		return "", fmt.Errorf("SLICE_COMMENT_FETCH_FAILED: invalid comments response: %w", err)
	}
	for _, c := range comments {
		if !strings.HasPrefix(c.Body, Marker) {
			continue
		}
		extracted, err := extractFencedJSONStrict(c.Body)
		if err != nil {
			return "", fmt.Errorf("SLICE_JSON_MALFORMED: comment id=%d: %w", c.ID, err)
		}
		return extracted, nil
	}
	return "", fmt.Errorf("SLICE_JSON_MALFORMED: no comment with marker %q found", Marker)
}

// bodyRequestsCommentInjection reports whether body contains Marker on its own
// (trimmed) line — the shape produced by the compact grill-me body. Prose
// mentions inside backticks or fences do not trigger a fetch.
func bodyRequestsCommentInjection(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == Marker {
			return true
		}
	}
	return false
}

// tryExtractFencedJSON returns the first ```json fenced block, validated as
// JSON, if one exists. ok=false when no valid block is found (used to fall
// through to the prose path, not to signal an error).
func tryExtractFencedJSON(body string) (string, bool) {
	extracted, err := extractFencedJSONStrict(body)
	if err != nil {
		return "", false
	}
	return extracted, true
}

// extractFencedJSONStrict extracts and validates the first ```json fenced
// block. Returns an error when the block is missing, unclosed, or invalid.
func extractFencedJSONStrict(body string) (string, error) {
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
