package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureBodyClosesIssue(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		branch string
		want   string
	}{
		{
			name:   "golemic branch without keyword appends Closes",
			body:   "Implements issue #6",
			branch: "golemic/issue-6",
			want:   "Implements issue #6\n\nCloses #6\n",
		},
		{
			name:   "golemic branch already has Closes keyword unchanged",
			body:   "Body text\n\nCloses #6",
			branch: "golemic/issue-6",
			want:   "Body text\n\nCloses #6",
		},
		{
			name:   "golemic branch with Fixes keyword unchanged",
			body:   "Fixes #6 now",
			branch: "golemic/issue-6",
			want:   "Fixes #6 now",
		},
		{
			name:   "keyword for different issue still appends",
			body:   "Closes #7",
			branch: "golemic/issue-6",
			want:   "Closes #7\n\nCloses #6\n",
		},
		{
			name:   "non-golemic branch left untouched",
			body:   "Description",
			branch: "feature/my-branch",
			want:   "Description",
		},
		{
			name:   "malformed golemic branch left untouched",
			body:   "Description",
			branch: "golemic/issue-abc",
			want:   "Description",
		},
		{
			name:   "negative-signed branch left untouched",
			body:   "Description",
			branch: "golemic/issue--6",
			want:   "Description",
		},
		{name: "positive-signed branch left untouched", body: "Description", branch: "golemic/issue-+6", want: "Description"},
		{name: "issue zero left untouched", body: "Description", branch: "golemic/issue-0", want: "Description"},
		{name: "branch with trailing suffix left untouched", body: "Description", branch: "golemic/issue-6-fix", want: "Description"},
		{name: "empty body appends Closes", body: "", branch: "golemic/issue-6", want: "\n\nCloses #6\n"},
		{name: "prefix-collision issue number still appends", body: "Closes #16", branch: "golemic/issue-1", want: "Closes #16\n\nCloses #1\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ensureBodyClosesIssue(c.body, c.branch)
			if got != c.want {
				t.Errorf("ensureBodyClosesIssue(%q, %q) = %q, want %q", c.body, c.branch, got, c.want)
			}
		})
	}
}

func TestRunOpenPR_AppendsClosesToGhBody(t *testing.T) {
	dir := t.TempDir()
	makeTestConfig(t, dir)
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-pr-close",
		"GOLEMIC_EVENT_LOG": filepath.Join(dir, "events.jsonl"),
		"GOLEMIC_TURN_ID":    "1",
	}

	var sentBody string
	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "sh" {
				return "", nil
			}
			return "golemic/issue-6\n", nil
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[1] == "list" {
				return "[]", nil
			}
			for i, a := range args[:len(args)-1] {
				if a == "--body" {
					sentBody = args[i+1]
				}
			}
			return "https://github.com/owner/repo/pull/43\n", nil
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "open-pr", "--title", "My PR", "--body", "Implements issue #6"}
	if got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec, dir); got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	if !strings.Contains(sentBody, "Closes #6") {
		t.Errorf("gh pr create --body should contain %q, got: %q", "Closes #6", sentBody)
	}
}
