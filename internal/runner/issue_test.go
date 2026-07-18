package runner

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"golemic/internal/credentials"
)

func marshalString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func ghIssueViewResponse(title, body string) string {
	return fmt.Sprintf(`{"title":%s,"body":%s,"labels":[]}`,
		marshalString(title), marshalString(body))
}

func ghCommentsResponse(id int, body string) string {
	return fmt.Sprintf(`[{"id":%d,"body":%s}]`, id, marshalString(body))
}

func newIssueRunner(exec *fakeExecutor) *Runner {
	return &Runner{
		executor: exec,
		creds:    credentials.NewFromTokens("dev-token", "reviewer-token"),
		repoRoot: "/fake/repo",
		issueNum: 42,
	}
}

// issueExecutor builds a fakeExecutor that routes gh issue view and gh api comments calls.
// commentsResp is returned for comments API calls; pass "" to disallow comments calls.
func issueExecutor(issueViewResp, commentsResp string) *fakeExecutor { //nolint:cyclop // routing dispatcher; branches are inherent
	return &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "issue" && args[1] == "view" {
				return issueViewResp, nil
			}
			if commentsResp != "" && len(args) >= 2 && args[0] == "api" && strings.Contains(args[1], "comments") {
				return commentsResp, nil
			}
			return "", fmt.Errorf("unexpected call: %s %v", name, args)
		},
	}
}

const compactBody = "**Type:** command | **Risk:** medium\n\n## Summary\n\nTest summary\n\n" + sliceCommentMarker + "\n_Slice JSON is in the first bot comment._"

const sliceJSON = `{"schema_version":"1.1.0","title":"test"}`

const wellFormedComment = sliceCommentMarker + "\n\n```json\n" + sliceJSON + "\n```"

// TestLoadIssueInjectsSliceCommentJSON (AC-004): body with marker → fetch comment → inject JSON.
func TestLoadIssueInjectsSliceCommentJSON(t *testing.T) {
	exec := issueExecutor(
		ghIssueViewResponse("Test Issue", compactBody),
		ghCommentsResponse(99, wellFormedComment),
	)

	r := newIssueRunner(exec)
	issue, err := r.loadIssue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if issue.Number != 42 {
		t.Errorf("Number = %d, want 42", issue.Number)
	}
	if issue.Title != "Test Issue" {
		t.Errorf("Title = %q, want %q", issue.Title, "Test Issue")
	}
	if !strings.HasPrefix(issue.Body, compactBody) {
		t.Errorf("Body does not start with compact body\nBody: %q", issue.Body)
	}
	if !strings.Contains(issue.Body, "```json\n"+sliceJSON+"\n```") {
		t.Errorf("Body does not contain injected JSON block\nBody: %q", issue.Body)
	}
}

// TestLoadIssueSoftFallbackWhenNoMarker (AC-005): no marker in body → return body unchanged, no comments call.
func TestLoadIssueSoftFallbackWhenNoMarker(t *testing.T) {
	legacyBody := "Some legacy issue body without any marker."
	// commentsResp is "" so any api/comments call will error via issueExecutor.
	exec := issueExecutor(ghIssueViewResponse("Legacy Issue", legacyBody), "")

	r := newIssueRunner(exec)
	issue, err := r.loadIssue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if issue.Body != legacyBody {
		t.Errorf("Body = %q, want original %q", issue.Body, legacyBody)
	}
}

// TestLoadIssueSliceJSONMalformed (AC-006): marker present but json block malformed → SLICE_JSON_MALFORMED error.
func TestLoadIssueSliceJSONMalformed(t *testing.T) {
	issueViewResp := ghIssueViewResponse("Broken Issue", compactBody)

	tests := []struct {
		name    string
		comment string
	}{
		{
			name:    "no json block",
			comment: sliceCommentMarker + "\n\nno fenced block here",
		},
		{
			name:    "empty json block",
			comment: sliceCommentMarker + "\n\n```json\n\n```",
		},
		{
			name:    "invalid json",
			comment: sliceCommentMarker + "\n\n```json\n{not valid json\n```",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commentsResp := ghCommentsResponse(77, tt.comment)

			exec := issueExecutor(issueViewResp, commentsResp)

			r := newIssueRunner(exec)
			_, err := r.loadIssue()
			if err == nil {
				t.Fatal("expected SLICE_JSON_MALFORMED error, got nil")
			}
			if !strings.HasPrefix(err.Error(), "SLICE_JSON_MALFORMED") {
				t.Errorf("error should begin with SLICE_JSON_MALFORMED, got: %v", err)
			}
			if !strings.Contains(err.Error(), "77") {
				t.Errorf("error should contain comment id 77, got: %v", err)
			}
		})
	}
}
