package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"golemic/internal/preflight"
)

// ---------------------------------------------------------------------------
// review-comment test helpers
// ---------------------------------------------------------------------------

// reviewCommentExec builds a fakeExecutor for the review-comment subcommand.
// It stubs repo view, discover (no pending), create, and addReviewThread.
// threadErr is returned by the addPullRequestReviewThread call (nil = success).
func reviewCommentExec(reviewID string, threadErr error) fakeExecutor { //nolint:cyclop
	return fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "repo" {
				return `{"owner":{"login":"o"},"name":"r"}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "viewer{login}") {
				return `{"data":{"viewer":{"login":"bot"},"repository":{"pullRequest":{"id":"PR_1","reviews":{"nodes":[]}}}}}`, nil
			}
			// Check thread BEFORE create ("addPullRequestReview" is a substring of "addPullRequestReviewThread").
			if name == "gh" && args[0] == "api" && containsArg(args, "addPullRequestReviewThread") {
				if threadErr != nil {
					return "", threadErr
				}
				return `{"data":{"addPullRequestReviewThread":{"thread":{"id":"PRRT_1"}}}}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "addPullRequestReview") {
				return fmt.Sprintf(`{"data":{"addPullRequestReview":{"pullRequestReview":{"id":%q}}}}`, reviewID), nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}
}

// ---------------------------------------------------------------------------
// AC-007: fail-fast on missing env (BR-004)
// ---------------------------------------------------------------------------

func TestReviewComment_MissingRunID_AC007(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "",
		"GOLEMIC_EVENT_LOG": logPath,
	}

	var called bool
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			called = true
			return "", fmt.Errorf("should not be called")
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "1", "--path", "foo.go", "--line", "10", "--body", "fix this"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "GOLEMIC_RUN_ID") {
		t.Errorf("stderr missing GOLEMIC_RUN_ID, got: %q", stderr.String())
	}
	if called {
		t.Error("gh should not be called when GOLEMIC_RUN_ID is missing")
	}
}

func TestReviewComment_MissingEventLog_AC007(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-1",
		"GOLEMIC_EVENT_LOG": "",
	}

	var called bool
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			called = true
			return "", fmt.Errorf("should not be called")
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "1", "--path", "foo.go", "--line", "10", "--body", "fix"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "GOLEMIC_EVENT_LOG") {
		t.Errorf("stderr missing GOLEMIC_EVENT_LOG, got: %q", stderr.String())
	}
	if called {
		t.Error("gh should not be called when GOLEMIC_EVENT_LOG is missing")
	}
}

// ---------------------------------------------------------------------------
// Flag validation
// ---------------------------------------------------------------------------

func TestReviewComment_MissingPR(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-1",
		"GOLEMIC_EVENT_LOG": filepath.Join(t.TempDir(), "events.jsonl"),
	}
	exec := fakeExecutor{}
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--path", "foo.go", "--line", "10", "--body", "fix"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)
	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "--pr") {
		t.Errorf("stderr missing --pr error, got: %q", stderr.String())
	}
}

func TestReviewComment_MissingPath(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-1",
		"GOLEMIC_EVENT_LOG": filepath.Join(t.TempDir(), "events.jsonl"),
	}
	exec := fakeExecutor{}
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "1", "--line", "10", "--body", "fix"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)
	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "--path") {
		t.Errorf("stderr missing --path error, got: %q", stderr.String())
	}
}

func TestReviewComment_MissingBody(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-1",
		"GOLEMIC_EVENT_LOG": filepath.Join(t.TempDir(), "events.jsonl"),
	}
	exec := fakeExecutor{}
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "1", "--path", "foo.go", "--line", "10"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)
	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "--body") {
		t.Errorf("stderr missing --body error, got: %q", stderr.String())
	}
}

func TestReviewComment_InvalidSide(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-1",
		"GOLEMIC_EVENT_LOG": filepath.Join(t.TempDir(), "events.jsonl"),
	}
	exec := fakeExecutor{}
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "1", "--path", "foo.go", "--line", "10", "--side", "BOTH", "--body", "fix"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)
	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "--side") {
		t.Errorf("stderr missing --side error, got: %q", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// AC-001 (review-comment): success path, no event written
// ---------------------------------------------------------------------------

func TestReviewComment_Success_NoEventWritten(t *testing.T) { //nolint:cyclop
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-rc-1",
		"GOLEMIC_EVENT_LOG": logPath,
	}

	rightSideSeen := false
	pathSeen := false
	lineSeen := false
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "repo" {
				return `{"owner":{"login":"o"},"name":"r"}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "viewer{login}") {
				return `{"data":{"viewer":{"login":"bot"},"repository":{"pullRequest":{"id":"PR_1","reviews":{"nodes":[]}}}}}`, nil
			}
			// Check thread BEFORE create ("addPullRequestReview" is a substring of "addPullRequestReviewThread").
			if name == "gh" && args[0] == "api" && containsArg(args, "addPullRequestReviewThread") {
				rightSideSeen = containsArg(args, "side=RIGHT")
				pathSeen = containsArg(args, "path=internal/foo.go")
				lineSeen = containsArg(args, "line=42")
				return `{"data":{"addPullRequestReviewThread":{"thread":{"id":"PRRT_1"}}}}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "addPullRequestReview") {
				return `{"data":{"addPullRequestReview":{"pullRequestReview":{"id":"PRR_1"}}}}`, nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "1", "--path", "internal/foo.go", "--line", "42", "--body", "This is wrong"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	if !rightSideSeen {
		t.Error("expected side=RIGHT in GraphQL args (default)")
	}
	if !pathSeen {
		t.Error("expected path=internal/foo.go in GraphQL args")
	}
	if !lineSeen {
		t.Error("expected line=42 in GraphQL args")
	}

	// No event written (review-comment never writes events).
	if n := countEventsInLog(t, logPath); n != 0 {
		t.Errorf("review-comment must not write events; got %d events", n)
	}
}

// countEventsInLog returns the number of events in the log, or 0 if it doesn't exist.
func countEventsInLog(t *testing.T, logPath string) int {
	t.Helper()
	events, err := readEventsForDedup(logPath)
	if err != nil {
		return 0
	}
	return len(events)
}

// ---------------------------------------------------------------------------
// AC-002: ANCHOR_FAILED → exit 2 with structured message (BR-002, DT-001)
// ---------------------------------------------------------------------------

func TestReviewComment_AnchorFailed_Exit2(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-anchor-1",
		"GOLEMIC_EVENT_LOG": filepath.Join(t.TempDir(), "events.jsonl"),
	}

	anchorErr := &preflight.ErrExit{
		ExitCode: 1,
		Stderr:   "Pull request review thread line must be part of the diff.",
	}
	exec := reviewCommentExec("PRR_1", anchorErr)

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "5", "--path", "src/main.go", "--line", "99", "--body", "Check this"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 2 {
		t.Fatalf("exit code: got %d, want 2 (ANCHOR_FAILED)", got)
	}
	msg := stderr.String()
	if !strings.Contains(msg, "ANCHOR_FAILED:") {
		t.Errorf("stderr missing ANCHOR_FAILED prefix, got: %q", msg)
	}
	if !strings.Contains(msg, "path=src/main.go") {
		t.Errorf("stderr missing path, got: %q", msg)
	}
	if !strings.Contains(msg, "line=99") {
		t.Errorf("stderr missing line, got: %q", msg)
	}
	if !strings.Contains(msg, "side=RIGHT") {
		t.Errorf("stderr missing side, got: %q", msg)
	}
	if !strings.Contains(msg, "reason=") {
		t.Errorf("stderr missing reason, got: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// AC-003: non-anchor GraphQL failure → exit 1 (DT-001)
// ---------------------------------------------------------------------------

func TestReviewComment_GraphQLFailure_Exit1(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-gqlfail",
		"GOLEMIC_EVENT_LOG": filepath.Join(t.TempDir(), "events.jsonl"),
	}

	authErr := &preflight.ErrExit{ExitCode: 1, Stderr: "401 Unauthorized"}
	exec := reviewCommentExec("PRR_1", authErr)

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "1", "--path", "foo.go", "--line", "1", "--body", "fix"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1 (non-anchor failure)", got)
	}
	if strings.Contains(stderr.String(), "ANCHOR_FAILED") {
		t.Error("stderr must not contain ANCHOR_FAILED for auth error")
	}
	if !strings.Contains(stderr.String(), "Failed to add review comment") {
		t.Errorf("stderr missing failure message, got: %q", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// AC-005: --start-line flag is passed through (D-007)
// ---------------------------------------------------------------------------

func TestReviewComment_WithStartLine(t *testing.T) { //nolint:cyclop
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-startline",
		"GOLEMIC_EVENT_LOG": filepath.Join(t.TempDir(), "events.jsonl"),
	}

	startLineSeen := false
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "repo" {
				return `{"owner":{"login":"o"},"name":"r"}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "viewer{login}") {
				return `{"data":{"viewer":{"login":"bot"},"repository":{"pullRequest":{"id":"PR_1","reviews":{"nodes":[]}}}}}`, nil
			}
			// Check thread BEFORE create ("addPullRequestReview" is a substring of "addPullRequestReviewThread").
			if name == "gh" && args[0] == "api" && containsArg(args, "addPullRequestReviewThread") {
				startLineSeen = containsArg(args, "startLine=10")
				return `{"data":{"addPullRequestReviewThread":{"thread":{"id":"PRRT_1"}}}}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "addPullRequestReview") {
				return `{"data":{"addPullRequestReview":{"pullRequestReview":{"id":"PRR_1"}}}}`, nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "1", "--path", "foo.go", "--line", "15", "--start-line", "10", "--body", "multi-line"}
	got := runReviewComment(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	if !startLineSeen {
		t.Error("expected startLine=10 in GraphQL args for --start-line flag")
	}
}

// ---------------------------------------------------------------------------
// Dispatch test — verifies the subcommand is registered and routed correctly.
// Env vars are not set, so it exits 1 with env var error before any gh call.
// ---------------------------------------------------------------------------

func TestReviewComment_Dispatch(t *testing.T) {
	t.Setenv("GOLEMIC_RUN_ID", "")
	t.Setenv("GOLEMIC_EVENT_LOG", "")

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "review-comment", "--pr", "1", "--path", "foo.go", "--line", "1", "--body", "x"}
	got := run(args, &stdout, &stderr)
	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if strings.Contains(stderr.String(), "not implemented") {
		t.Errorf("review-comment should not say 'not implemented', got: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "Unknown command") {
		t.Errorf("review-comment should be a known command, got: %q", stderr.String())
	}
}
