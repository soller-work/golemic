package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"golemic/internal/preflight"
)

// fakeGraphQLReviewComment returns a fake executor for review-comment tests.
// If anchorFail is true, addPullRequestReviewThread returns an anchor error.
// If otherFail is true, addPullRequestReviewThread returns a generic error.
func fakeGraphQLReviewComment(existingReviewID string, anchorFail, otherFail bool) fakeExecutor { //nolint:cyclop
	return fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name != "gh" {
				return "", fmt.Errorf("unexpected command: %s", name)
			}
			if args[0] == "repo" && args[1] == "view" {
				return `{"owner":{"login":"test-owner"},"name":"test-repo"}`, nil
			}
			if args[0] != "api" || args[1] != "graphql" {
				return "", fmt.Errorf("unexpected gh args: %v", args)
			}
			q := graphqlQueryArg(args)
			switch {
			case strings.Contains(q, "addPullRequestReviewThread"):
				if anchorFail {
					return "", &preflight.ErrExit{ExitCode: 1, Stderr: "Line is not part of the diff"}
				}
				if otherFail {
					return "", &preflight.ErrExit{ExitCode: 1, Stderr: "authentication required"}
				}
				return `{"data":{"addPullRequestReviewThread":{"thread":{"id":"T_01"}}}}`, nil

			case strings.Contains(q, "addPullRequestReview(") && !strings.Contains(q, "Thread"):
				return `{"data":{"addPullRequestReview":{"pullRequestReview":{"id":"PRR_new"}}}}`, nil

			default: // discover query
				if existingReviewID != "" {
					return fmt.Sprintf(`{"data":{"repository":{"pullRequest":{"id":"PR_01","reviews":{"nodes":[{"id":%q,"comments":{"totalCount":0}}]}}}}}`, existingReviewID), nil
				}
				return `{"data":{"repository":{"pullRequest":{"id":"PR_01","reviews":{"nodes":[]}}}}}`, nil
			}
		},
	}
}

// AC-007: review-comment fails fast on missing env var.
func TestRunReviewComment_MissingEnvVar_AC007(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "",
		"GOLEMIC_EVENT_LOG": filepath.Join(t.TempDir(), "events.jsonl"),
	}
	var called bool
	exec := fakeExecutor{runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
		called = true
		return "", fmt.Errorf("should not be called")
	}}
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "1", "--path", "foo.go", "--line", "10", "--body", "nit"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)
	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "GOLEMIC_RUN_ID") {
		t.Errorf("stderr missing GOLEMIC_RUN_ID, got: %q", stderr.String())
	}
	if called {
		t.Error("gh should not be called when env var is missing")
	}
}

func TestRunReviewComment_MissingEventLog_AC007(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-rc-1",
		"GOLEMIC_EVENT_LOG": "",
	}
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "1", "--path", "foo.go", "--line", "10", "--body", "nit"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, fakeExecutor{})
	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "GOLEMIC_EVENT_LOG") {
		t.Errorf("stderr missing GOLEMIC_EVENT_LOG, got: %q", stderr.String())
	}
}

// Happy path: pinnable finding on existing pending review.
func TestRunReviewComment_Success_ExistingReview(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-rc-2",
		"GOLEMIC_EVENT_LOG": filepath.Join(t.TempDir(), "events.jsonl"),
	}
	exec := fakeGraphQLReviewComment("PRR_existing", false, false)
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "42", "--path", "pkg/foo.go", "--line", "15", "--body", "use := instead"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)
	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got: %q", stderr.String())
	}
}

// Happy path: no existing pending review → create then add thread.
func TestRunReviewComment_Success_CreatesReview(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-rc-3",
		"GOLEMIC_EVENT_LOG": filepath.Join(t.TempDir(), "events.jsonl"),
	}
	createCalled := false
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if args[0] == "repo" {
				return `{"owner":{"login":"o"},"name":"r"}`, nil
			}
			q := graphqlQueryArg(args)
			if strings.Contains(q, "addPullRequestReviewThread") {
				return `{"data":{"addPullRequestReviewThread":{"thread":{"id":"T_01"}}}}`, nil
			}
			if strings.Contains(q, "addPullRequestReview(") && !strings.Contains(q, "Thread") {
				createCalled = true
				return `{"data":{"addPullRequestReview":{"pullRequestReview":{"id":"PRR_new"}}}}`, nil
			}
			return `{"data":{"repository":{"pullRequest":{"id":"PR_01","reviews":{"nodes":[]}}}}}`, nil
		},
	}
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "5", "--path", "a.go", "--line", "1", "--body", "fix me"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)
	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	if !createCalled {
		t.Error("expected pending review to be created (IC-001)")
	}
}

// AC-002: BR-002 — anchor failure returns exit 2 with ANCHOR_FAILED on stderr.
func TestRunReviewComment_AnchorFailed_Exit2(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-rc-anchor",
		"GOLEMIC_EVENT_LOG": filepath.Join(t.TempDir(), "events.jsonl"),
	}
	exec := fakeGraphQLReviewComment("PRR_01", true /* anchorFail */, false)
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "7", "--path", "main.go", "--line", "99", "--body", "typo"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)
	if got != 2 {
		t.Fatalf("exit code: got %d, want 2 (ANCHOR_FAILED); stderr: %s", got, stderr.String())
	}
	msg := stderr.String()
	if !strings.Contains(msg, "ANCHOR_FAILED") {
		t.Errorf("stderr missing ANCHOR_FAILED prefix, got: %q", msg)
	}
	if !strings.Contains(msg, "path=main.go") {
		t.Errorf("stderr missing path=main.go, got: %q", msg)
	}
	if !strings.Contains(msg, "line=99") {
		t.Errorf("stderr missing line=99, got: %q", msg)
	}
	if !strings.Contains(msg, "side=RIGHT") {
		t.Errorf("stderr missing side=RIGHT (default), got: %q", msg)
	}
}

// DT-001: auth/network failure returns exit 1, not exit 2.
func TestRunReviewComment_OtherFailure_Exit1(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-rc-other",
		"GOLEMIC_EVENT_LOG": filepath.Join(t.TempDir(), "events.jsonl"),
	}
	exec := fakeGraphQLReviewComment("PRR_01", false, true /* otherFail */)
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "3", "--path", "x.go", "--line", "5", "--body", "fix"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)
	if got != 1 {
		t.Fatalf("exit code: got %d, want 1; stderr: %s", got, stderr.String())
	}
	if strings.Contains(stderr.String(), "ANCHOR_FAILED") {
		t.Errorf("auth error should not produce ANCHOR_FAILED, got: %q", stderr.String())
	}
}

// Flag validation: missing --pr.
func TestRunReviewComment_MissingPR(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-rc-val",
		"GOLEMIC_EVENT_LOG": filepath.Join(t.TempDir(), "events.jsonl"),
	}
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--path", "a.go", "--line", "1", "--body", "x"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, fakeExecutor{})
	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "--pr must be a positive integer") {
		t.Errorf("stderr: %q", stderr.String())
	}
}

// Flag validation: invalid --side.
func TestRunReviewComment_InvalidSide(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-rc-side",
		"GOLEMIC_EVENT_LOG": filepath.Join(t.TempDir(), "events.jsonl"),
	}
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "1", "--path", "a.go", "--line", "1", "--body", "x", "--side", "BOTH"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, fakeExecutor{})
	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "Invalid --side") {
		t.Errorf("stderr: %q", stderr.String())
	}
}

// --start-line is accepted without error (contract completeness).
func TestRunReviewComment_StartLineAccepted(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-rc-sl",
		"GOLEMIC_EVENT_LOG": filepath.Join(t.TempDir(), "events.jsonl"),
	}
	exec := fakeGraphQLReviewComment("PRR_01", false, false)
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "1", "--path", "a.go", "--line", "10", "--start-line", "5", "--body", "multi-line note"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)
	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
}

// Dispatch: run() routes review-comment to runReviewComment.
func TestRunReviewComment_Dispatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "1", "--path", "a.go", "--line", "1", "--body", "x"}
	got := run(args, &stdout, &stderr)
	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if strings.Contains(stderr.String(), "not implemented") {
		t.Errorf("review-comment should not say 'not implemented', got: %q", stderr.String())
	}
	// Should fail on missing env var, not "unknown command".
	if strings.Contains(stderr.String(), "Unknown command") {
		t.Errorf("review-comment should be recognized, got: %q", stderr.String())
	}
}
