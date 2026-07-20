package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golemic/internal/eventlog"
	"golemic/internal/preflight"
)

// ---------------------------------------------------------------------------
// GraphQL test helpers
// ---------------------------------------------------------------------------

// graphqlSubmitExec builds a fakeExecutor that stubs the full GraphQL submit-review
// sequence: repo view → discover (no pending) → create → submit → label ops.
// reviewID is the fake pending review node id; inlineCount is returned as totalCount.
func graphqlSubmitExec(prNum int, verdict, body, reviewID string, inlineCount int) fakeExecutor {
	h := graphqlSubmitHandler(prNum, verdict, body, reviewID, inlineCount)
	return fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return h(env, name, args)
		},
	}
}

// graphqlSubmitHandler returns a handler function for graphqlSubmitExec (reusable in lambdas).
func graphqlSubmitHandler(prNum int, verdict, body, reviewID string, inlineCount int) func(map[string]string, string, []string) (string, error) { //nolint:cyclop
	ghEvent := "APPROVE"
	if verdict == "changes_requested" {
		ghEvent = "REQUEST_CHANGES"
	}
	return func(env map[string]string, name string, args []string) (string, error) {
		if name != "gh" {
			return "", fmt.Errorf("unexpected non-gh call: %s %v", name, args)
		}
		switch {
		case args[0] == "repo" && args[1] == "view":
			return `{"owner":{"login":"testowner"},"name":"testrepo"}`, nil
		case args[0] == "api" && args[1] == "graphql" && containsArg(args, "viewer{login}"):
			// Discover query: no pending reviews.
			return `{"data":{"viewer":{"login":"reviewer-bot"},"repository":{"pullRequest":{"id":"PR_node123","reviews":{"nodes":[]}}}}}`, nil
		case args[0] == "api" && args[1] == "graphql" && containsArg(args, "addPullRequestReview"):
			// Create pending review (check after submitPullRequestReview to avoid substring collision).
			return fmt.Sprintf(`{"data":{"addPullRequestReview":{"pullRequestReview":{"id":%q}}}}`, reviewID), nil
		case args[0] == "api" && args[1] == "graphql" && containsArg(args, "submitPullRequestReview"):
			// Verify event matches verdict.
			if !containsArg(args, "event="+ghEvent) {
				return "", fmt.Errorf("expected event=%s in args, got %v", ghEvent, args)
			}
			if !containsArg(args, "body="+body) {
				return "", fmt.Errorf("expected body=%q in args, got %v", body, args)
			}
			return fmt.Sprintf(`{"data":{"submitPullRequestReview":{"pullRequestReview":{"id":%q,"comments":{"totalCount":%d}}}}}`, reviewID, inlineCount), nil
		case args[0] == "label" && args[1] == "create":
			return "", nil
		case args[0] == "pr" && args[1] == "edit":
			return "", nil
		default:
			return "", fmt.Errorf("unexpected gh call: %v", args)
		}
	}
}

// containsArg returns true if args contains a string with the given substring.
func containsArg(args []string, substr string) bool {
	for _, a := range args {
		if strings.Contains(a, substr) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// AC-001: Approved verdict
// ---------------------------------------------------------------------------

func TestRunSubmitReview_ApprovedSuccess(t *testing.T) { //nolint:cyclop,gocognit,funlen
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-1",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	const reviewID = "PRR_approved123"
	exec := graphqlSubmitExec(123, "approved", "LGTM", reviewID, 0)

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "123", "--merge-confidence", "high"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got: %q", stderr.String())
	}

	// Verify exactly one review_submitted event with correct fields.
	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != eventlog.EventReviewSubmitted {
		t.Errorf("Type: got %q, want %q", ev.Type, eventlog.EventReviewSubmitted)
	}
	if ev.RunID != "run-review-1" {
		t.Errorf("RunID: got %q, want %q", ev.RunID, "run-review-1")
	}
	if _, err := time.Parse(time.RFC3339, ev.Ts); err != nil {
		t.Errorf("Ts is not valid RFC3339: %q (err: %v)", ev.Ts, err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload["verdict"] != "approved" {
		t.Errorf("verdict: got %q, want %q", payload["verdict"], "approved")
	}
	if payload["body"] != "LGTM" {
		t.Errorf("body: got %q, want %q", payload["body"], "LGTM")
	}
	if payload["prNumber"] != float64(123) {
		t.Errorf("prNumber: got %v, want %v", payload["prNumber"], 123)
	}
	if payload["mergeConfidence"] != "high" {
		t.Errorf("mergeConfidence: got %q, want %q", payload["mergeConfidence"], "high")
	}
	if payload["reviewId"] != reviewID {
		t.Errorf("reviewId: got %q, want %q", payload["reviewId"], reviewID)
	}
	if payload["inlineCommentCount"] != float64(0) {
		t.Errorf("inlineCommentCount: got %v, want 0", payload["inlineCommentCount"])
	}
}

// ---------------------------------------------------------------------------
// AC-002: changes_requested verdict
// ---------------------------------------------------------------------------

func TestRunSubmitReview_ChangesRequestedSuccess(t *testing.T) { //nolint:cyclop,gocognit
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-2",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	const reviewID = "PRR_cr456"
	exec := graphqlSubmitExec(456, "changes_requested", "Fix NPE", reviewID, 2)

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "changes_requested", "--body", "Fix NPE", "--pr", "456", "--merge-confidence", "low"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got: %q", stderr.String())
	}

	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload["verdict"] != "changes_requested" {
		t.Errorf("verdict: got %q, want %q", payload["verdict"], "changes_requested")
	}
	if payload["mergeConfidence"] != "low" {
		t.Errorf("mergeConfidence: got %q, want %q", payload["mergeConfidence"], "low")
	}
	if payload["reviewId"] != reviewID {
		t.Errorf("reviewId: got %q, want %q", payload["reviewId"], reviewID)
	}
	if payload["inlineCommentCount"] != float64(2) {
		t.Errorf("inlineCommentCount: got %v, want 2", payload["inlineCommentCount"])
	}
}

// ---------------------------------------------------------------------------
// Validation tests (unchanged behaviour)
// ---------------------------------------------------------------------------

func TestRunSubmitReview_InvalidVerdictNoGhCall(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-invalid",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	var called bool
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			called = true
			return "", fmt.Errorf("should not be called")
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "invalid", "--body", "text", "--pr", "123", "--merge-confidence", "high"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "Invalid verdict") {
		t.Errorf("stderr missing 'Invalid verdict', got: %q", stderr.String())
	}
	if called {
		t.Error("gh command should not have been called for invalid verdict")
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Error("log file should not exist when verdict validation fails")
	}
}

func TestRunSubmitReview_GhFailureNoEvent(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-gh-fail",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "repo" {
				return `{"owner":{"login":"o"},"name":"r"}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "viewer{login}") {
				return `{"data":{"viewer":{"login":"bot"},"repository":{"pullRequest":{"id":"PR_1","reviews":{"nodes":[]}}}}}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "addPullRequestReview") {
				return `{"data":{"addPullRequestReview":{"pullRequestReview":{"id":"PRR_1"}}}}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "submitPullRequestReview") {
				return "", &preflight.ErrExit{ExitCode: 1, Stderr: "PR not found or access denied"}
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "999", "--merge-confidence", "high"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got == 0 {
		t.Fatalf("exit code: got 0, want != 0")
	}
	if !strings.Contains(stderr.String(), "Failed to submit review") {
		t.Errorf("stderr missing 'Failed to submit review', got: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "PR not found or access denied") {
		t.Errorf("stderr should include gh error message, got: %q", stderr.String())
	}
	// No event written on failure.
	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatalf("failed to read event log: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func TestRunSubmitReview_MissingEnvVar_RunID(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "", fmt.Errorf("should not be called")
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "123"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "GOLEMIC_RUN_ID") {
		t.Errorf("stderr missing GOLEMIC_RUN_ID, got: %q", stderr.String())
	}
}

func TestRunSubmitReview_MissingEnvVar_EventLog(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-missing-el",
		"GOLEMIC_EVENT_LOG": "",
	}

	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "", fmt.Errorf("should not be called")
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "123"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "GOLEMIC_EVENT_LOG") {
		t.Errorf("stderr missing GOLEMIC_EVENT_LOG, got: %q", stderr.String())
	}
}

func TestRunSubmitReview_MissingBothEnvVars(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "",
		"GOLEMIC_EVENT_LOG": "",
	}

	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "", fmt.Errorf("should not be called")
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "123"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	msg := stderr.String()
	if !strings.Contains(msg, "GOLEMIC_RUN_ID") || !strings.Contains(msg, "GOLEMIC_EVENT_LOG") {
		t.Errorf("stderr should list both missing vars, got: %q", msg)
	}
}

func TestRunSubmitReview_EmptyBody(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-empty-body",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "", fmt.Errorf("should not be called")
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "", "--pr", "123", "--merge-confidence", "high"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "--body must not be empty") {
		t.Errorf("stderr missing '--body must not be empty', got: %q", stderr.String())
	}
}

func TestRunSubmitReview_InvalidPRNumber(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-invalid-pr",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "", fmt.Errorf("should not be called")
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "0", "--merge-confidence", "high"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "--pr must be a positive integer") {
		t.Errorf("stderr missing error message, got: %q", stderr.String())
	}
}

func TestRunSubmitReview_CaseSensitiveVerdict(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-case",
		"GOLEMIC_EVENT_LOG": filepath.Join(t.TempDir(), "events.jsonl"),
		"GOLEMIC_TURN_ID":   "1",
	}

	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "", fmt.Errorf("should not be called")
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "Approved", "--body", "LGTM", "--pr", "123", "--merge-confidence", "high"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "Invalid verdict") {
		t.Errorf("stderr should reject uppercase verdict, got: %q", stderr.String())
	}
}

func TestRunSubmitReview_Dispatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "123"}
	got := run(args, &stdout, &stderr)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	msg := stderr.String()
	if strings.Contains(msg, "not implemented") {
		t.Errorf("submit-review should no longer say 'not implemented', got: %q", msg)
	}
	if !strings.Contains(msg, "Missing required environment variable") && !strings.Contains(msg, "Invalid merge confidence") {
		t.Errorf("stderr should mention missing env vars or invalid merge confidence, got: %q", msg)
	}
}

func TestRunSubmitReview_EventLogPathInvalidAborts(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-invalid-log",
		"GOLEMIC_EVENT_LOG": t.TempDir(), // directory not file
		"GOLEMIC_TURN_ID":   "1",
	}

	var called bool
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			called = true
			return "", fmt.Errorf("should not be called")
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "123", "--merge-confidence", "high"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got == 0 {
		t.Fatalf("exit code: got 0, want != 0")
	}
	if !strings.Contains(stderr.String(), "Failed to write event") {
		t.Errorf("stderr missing 'Failed to write event', got: %q", stderr.String())
	}
	if called {
		t.Error("gh command should NOT have been called when event log path is invalid")
	}
}

// ---------------------------------------------------------------------------
// AC-009: --merge-confidence validation (BR-009)
// ---------------------------------------------------------------------------

func TestRunSubmitReview_MissingMergeConfidence_AC009(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-mc-missing",
		"GOLEMIC_EVENT_LOG": filepath.Join(dir, "events.jsonl"),
		"GOLEMIC_TURN_ID":   "1",
	}
	var called bool
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			called = true
			return "", fmt.Errorf("should not be called")
		},
	}
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "123"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)
	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "Invalid merge confidence") {
		t.Errorf("stderr missing 'Invalid merge confidence', got: %q", stderr.String())
	}
	if called {
		t.Error("gh should not be called when --merge-confidence is missing")
	}
}

func TestRunSubmitReview_InvalidMergeConfidence_AC009(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-mc-invalid",
		"GOLEMIC_EVENT_LOG": filepath.Join(dir, "events.jsonl"),
		"GOLEMIC_TURN_ID":   "1",
	}
	var called bool
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			called = true
			return "", fmt.Errorf("should not be called")
		},
	}
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "123", "--merge-confidence", "medium"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)
	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "Invalid merge confidence") {
		t.Errorf("stderr missing 'Invalid merge confidence', got: %q", stderr.String())
	}
	if called {
		t.Error("gh should not be called for invalid --merge-confidence")
	}
}

func TestRunSubmitReview_LabelSetFailed(t *testing.T) { //nolint:cyclop
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-label-fail",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "repo" {
				return `{"owner":{"login":"o"},"name":"r"}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "viewer{login}") {
				return `{"data":{"viewer":{"login":"bot"},"repository":{"pullRequest":{"id":"PR_1","reviews":{"nodes":[]}}}}}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "addPullRequestReview") {
				return `{"data":{"addPullRequestReview":{"pullRequestReview":{"id":"PRR_1"}}}}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "submitPullRequestReview") {
				return `{"data":{"submitPullRequestReview":{"pullRequestReview":{"id":"PRR_1","comments":{"totalCount":0}}}}}`, nil
			}
			if name == "gh" && args[0] == "label" && args[1] == "create" {
				return "", nil
			}
			if name == "gh" && args[0] == "pr" && args[1] == "edit" {
				return "", fmt.Errorf("label add failed")
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "123", "--merge-confidence", "high"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)
	if got != 1 {
		t.Fatalf("exit code: got %d, want 1; stderr: %s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "PR label could not be set") {
		t.Errorf("stderr missing label failure message, got: %q", stderr.String())
	}
}

func TestRunSubmitReview_MergeConfidenceLow_WrittenToPayload(t *testing.T) { //nolint:cyclop
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-mc-low",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}
	exec := graphqlSubmitExec(7, "approved", "LGTM", "PRR_mc_low", 0)
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "7", "--merge-confidence", "low"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)
	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["mergeConfidence"] != "low" {
		t.Errorf("mergeConfidence: got %q, want %q", payload["mergeConfidence"], "low")
	}
	if payload["reviewId"] != "PRR_mc_low" {
		t.Errorf("reviewId: got %q, want %q", payload["reviewId"], "PRR_mc_low")
	}
}

// ---------------------------------------------------------------------------
// AC-004 (GraphQL): existing pending review is discovered and reused
// ---------------------------------------------------------------------------

func TestRunSubmitReview_ExistingPendingReviewDiscovered(t *testing.T) { //nolint:cyclop,funlen
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-discover",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	createCalled := 0
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "repo" {
				return `{"owner":{"login":"o"},"name":"r"}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "viewer{login}") {
				// Return existing pending review authored by viewer.
				return `{"data":{"viewer":{"login":"reviewer-bot"},"repository":{"pullRequest":{"id":"PR_1","reviews":{"nodes":[{"id":"PRR_existing","author":{"login":"reviewer-bot"}}]}}}}}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "addPullRequestReview") {
				createCalled++
				return `{"data":{"addPullRequestReview":{"pullRequestReview":{"id":"PRR_new"}}}}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "submitPullRequestReview") {
				if !containsArg(args, "reviewId=PRR_existing") {
					return "", fmt.Errorf("expected existing review id, got %v", args)
				}
				return `{"data":{"submitPullRequestReview":{"pullRequestReview":{"id":"PRR_existing","comments":{"totalCount":3}}}}}`, nil
			}
			if name == "gh" && args[0] == "label" {
				return "", nil
			}
			if name == "gh" && args[0] == "pr" && args[1] == "edit" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "42", "--merge-confidence", "high"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)
	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	if createCalled != 0 {
		t.Errorf("addPullRequestReview should NOT be called when existing pending review found; called %d time(s)", createCalled)
	}

	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["reviewId"] != "PRR_existing" {
		t.Errorf("reviewId: got %q, want PRR_existing", payload["reviewId"])
	}
	if payload["inlineCommentCount"] != float64(3) {
		t.Errorf("inlineCommentCount: got %v, want 3", payload["inlineCommentCount"])
	}
}

// ---------------------------------------------------------------------------
// AC-009 (new): no gh pr review call in any code path (BR-008)
// ---------------------------------------------------------------------------

func TestRunSubmitReview_NoGhPrReviewCall(t *testing.T) { //nolint:cyclop
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-no-pr-review",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	ghPrReviewCalled := false
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "review" {
				ghPrReviewCalled = true
				return "", fmt.Errorf("gh pr review must not be called")
			}
			if name == "gh" && args[0] == "repo" {
				return `{"owner":{"login":"o"},"name":"r"}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "viewer{login}") {
				return `{"data":{"viewer":{"login":"bot"},"repository":{"pullRequest":{"id":"PR_1","reviews":{"nodes":[]}}}}}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "addPullRequestReview") {
				return `{"data":{"addPullRequestReview":{"pullRequestReview":{"id":"PRR_1"}}}}`, nil
			}
			if name == "gh" && args[0] == "api" && containsArg(args, "submitPullRequestReview") {
				return `{"data":{"submitPullRequestReview":{"pullRequestReview":{"id":"PRR_1","comments":{"totalCount":0}}}}}`, nil
			}
			if name == "gh" && args[0] == "label" {
				return "", nil
			}
			if name == "gh" && args[0] == "pr" && args[1] == "edit" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "1", "--merge-confidence", "high"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)
	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	if ghPrReviewCalled {
		t.Error("gh pr review must not be called; submit-review must use GraphQL exclusively (BR-001, BR-008)")
	}
}
