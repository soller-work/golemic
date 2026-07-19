package slice

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type fakeExecutor struct {
	fn func(env map[string]string, dir, name string, args ...string) (string, error)
}

func (f *fakeExecutor) RunWithEnvInDir(env map[string]string, dir, name string, args ...string) (string, error) {
	return f.fn(env, dir, name, args...)
}

func marshalString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func ghIssueViewBody(body string) string {
	return fmt.Sprintf(`{"body":%s}`, marshalString(body))
}

func ghCommentsResponse(id int, body string) string {
	return fmt.Sprintf(`[{"id":%d,"body":%s}]`, id, marshalString(body))
}

func routingExecutor(viewResp, commentsResp string) *fakeExecutor {
	return &fakeExecutor{
		fn: func(_ map[string]string, _, _ string, args ...string) (string, error) {
			if len(args) >= 3 && args[0] == "issue" && args[1] == "view" {
				return viewResp, nil
			}
			if commentsResp != "" && len(args) >= 2 && args[0] == "api" && strings.Contains(args[1], "comments") {
				return commentsResp, nil
			}
			return "", fmt.Errorf("unexpected call: %s %v", args[0], args)
		},
	}
}

const testJSON = `{"schema_version":"1.1.0","title":"test"}`

// Priority 1: body has marker on its own line → JSON comes from comments.
func TestExtractFromComment(t *testing.T) {
	body := "**Summary**\n\n" + Marker + "\n_See bot comment._"
	comment := Marker + "\n\n```json\n" + testJSON + "\n```"
	exec := routingExecutor(ghIssueViewBody(body), ghCommentsResponse(99, comment))

	got, err := Extract(exec, "/repo", "tok", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != testJSON {
		t.Errorf("got %q, want %q", got, testJSON)
	}
}

// Priority 2: no marker but body contains inline ```json block (legacy).
func TestExtractInlineJSON(t *testing.T) {
	body := "Some prose\n\n```json\n" + testJSON + "\n```\n\nMore prose."
	exec := routingExecutor(ghIssueViewBody(body), "")

	got, err := Extract(exec, "/repo", "tok", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != testJSON {
		t.Errorf("got %q, want %q", got, testJSON)
	}
}

// Priority 3: hand-written issue with prose body only.
func TestExtractProseFallback(t *testing.T) {
	body := "Please implement foo. Ping me if unclear."
	exec := routingExecutor(ghIssueViewBody(body), "")

	got, err := Extract(exec, "/repo", "tok", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != body {
		t.Errorf("got %q, want %q", got, body)
	}
}

// Marker mentioned in prose only (inside backticks) must not trigger a fetch.
func TestMarkerInProseIgnored(t *testing.T) {
	body := "See the marker `" + Marker + "` in the docs."
	exec := routingExecutor(ghIssueViewBody(body), "")

	got, err := Extract(exec, "/repo", "tok", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != body {
		t.Errorf("got %q, want body verbatim", got)
	}
}

// Marker on its own line but matching comment is malformed → structured error.
func TestMalformedCommentJSON(t *testing.T) {
	body := Marker
	tests := []struct {
		name    string
		comment string
	}{
		{"no fence", Marker + "\n\nno block"},
		{"empty block", Marker + "\n\n```json\n\n```"},
		{"invalid json", Marker + "\n\n```json\n{not valid\n```"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := routingExecutor(ghIssueViewBody(body), ghCommentsResponse(77, tt.comment))
			_, err := Extract(exec, "/repo", "tok", 42)
			if err == nil || !strings.HasPrefix(err.Error(), "SLICE_JSON_MALFORMED") {
				t.Fatalf("want SLICE_JSON_MALFORMED, got %v", err)
			}
			if !strings.Contains(err.Error(), "77") {
				t.Errorf("error should reference comment id 77, got: %v", err)
			}
		})
	}
}

// Marker on its own line but no matching comment at all.
func TestMarkerButNoMatchingComment(t *testing.T) {
	body := Marker
	// Comments list contains a comment that doesn't start with the marker.
	exec := routingExecutor(ghIssueViewBody(body), ghCommentsResponse(1, "unrelated comment"))

	_, err := Extract(exec, "/repo", "tok", 42)
	if err == nil || !strings.HasPrefix(err.Error(), "SLICE_JSON_MALFORMED") {
		t.Fatalf("want SLICE_JSON_MALFORMED, got %v", err)
	}
}
