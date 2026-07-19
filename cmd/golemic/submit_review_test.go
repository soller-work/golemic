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

// fakeGraphQLSubmit returns a fake executor that handles the full GraphQL submit-review sequence.
// repoOwner, repoName, prNodeID, reviewNodeID are the fake IDs used in responses.
// commentCount is the count returned by the discover query.
// If failOn is non-empty, that stage fails: "discover", "create", or "submit".
func fakeGraphQLSubmit(prNodeID, reviewNodeID string, commentCount int, failOn string) fakeExecutor { //nolint:cyclop
	return fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name != "gh" {
				return "", fmt.Errorf("unexpected command: %s", name)
			}
			switch {
			case args[0] == "repo" && args[1] == "view":
				return `{"owner":{"login":"test-owner"},"name":"test-repo"}`, nil

			case args[0] == "api" && args[1] == "graphql":
				q := graphqlQueryArg(args)
				switch {
				case strings.Contains(q, "submitPullRequestReview"):
					if failOn == "submit" {
						return "", &preflight.ErrExit{ExitCode: 1, Stderr: "submit failed"}
					}
					return fmt.Sprintf(`{"data":{"submitPullRequestReview":{"pullRequestReview":{"id":%q,"state":"APPROVED"}}}}`, reviewNodeID), nil

				case strings.Contains(q, "addPullRequestReview(") && !strings.Contains(q, "Thread"):
					if failOn == "create" {
						return "", &preflight.ErrExit{ExitCode: 1, Stderr: "create failed"}
					}
					return fmt.Sprintf(`{"data":{"addPullRequestReview":{"pullRequestReview":{"id":%q}}}}`, reviewNodeID), nil

				default: // discover query
					if failOn == "discover" {
						return "", &preflight.ErrExit{ExitCode: 1, Stderr: "discover failed"}
					}
					if commentCount > 0 {
						// Return existing pending review with comments.
						return fmt.Sprintf(`{"data":{"repository":{"pullRequest":{"id":%q,"reviews":{"nodes":[{"id":%q,"comments":{"totalCount":%d}}]}}}}}`,
							prNodeID, reviewNodeID, commentCount), nil
					}
					// No existing pending review.
					return fmt.Sprintf(`{"data":{"repository":{"pullRequest":{"id":%q,"reviews":{"nodes":[]}}}}}`, prNodeID), nil
				}

			case args[0] == "label" && args[1] == "create":
				return "", nil
			case args[0] == "pr" && args[1] == "edit":
				return "", nil

			default:
				return "", fmt.Errorf("unexpected gh args: %v", args)
			}
		},
	}
}

// graphqlQueryArg extracts the value of the -f query=... flag from gh api graphql args.
func graphqlQueryArg(args []string) string {
	for i, a := range args {
		if a == "-f" && i+1 < len(args) && strings.HasPrefix(args[i+1], "query=") {
			return args[i+1][len("query="):]
		}
	}
	return ""
}

func TestRunSubmitReview_ApprovedSuccess(t *testing.T) { //nolint:funlen,cyclop
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-1",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	exec := fakeGraphQLSubmit("PR_01", "PRR_01", 0, "")

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "123", "--merge-confidence", "high"}
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
	ev := events[0]
	if ev.Type != eventlog.EventReviewSubmitted {
		t.Errorf("Type: got %q, want %q", ev.Type, eventlog.EventReviewSubmitted)
	}
	if ev.RunID != "run-review-1" {
		t.Errorf("RunID: got %q", ev.RunID)
	}
	if _, parseErr := time.Parse(time.RFC3339, ev.Ts); parseErr != nil {
		t.Errorf("Ts not valid RFC3339: %q", ev.Ts)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["verdict"] != "approved" {
		t.Errorf("verdict: got %v", payload["verdict"])
	}
	if payload["body"] != "LGTM" {
		t.Errorf("body: got %v", payload["body"])
	}
	if payload["prNumber"] != float64(123) {
		t.Errorf("prNumber: got %v", payload["prNumber"])
	}
	if payload["mergeConfidence"] != "high" {
		t.Errorf("mergeConfidence: got %v", payload["mergeConfidence"])
	}
	if payload["reviewId"] != "PRR_01" {
		t.Errorf("reviewId: got %v", payload["reviewId"])
	}
	if payload["inlineCommentCount"] != float64(0) {
		t.Errorf("inlineCommentCount: got %v", payload["inlineCommentCount"])
	}
}

func TestRunSubmitReview_ChangesRequestedSuccess(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-2",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	exec := fakeGraphQLSubmit("PR_02", "PRR_02", 3, "")

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "changes_requested", "--body", "Fix NPE", "--pr", "456", "--merge-confidence", "low"}
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
	if payload["verdict"] != "changes_requested" {
		t.Errorf("verdict: got %v", payload["verdict"])
	}
	if payload["mergeConfidence"] != "low" {
		t.Errorf("mergeConfidence: got %v", payload["mergeConfidence"])
	}
	// Existing pending review with 3 inline comments.
	if payload["inlineCommentCount"] != float64(3) {
		t.Errorf("inlineCommentCount: got %v, want 3", payload["inlineCommentCount"])
	}
}

func TestRunSubmitReview_ApprovedNoPendingReview_CreatesOne(t *testing.T) { //nolint:funlen,cyclop
	// AC-004: approved without prior review-comment calls → IC-003 empty → IC-001 creates → IC-004 submits.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-3",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	createCalled := false
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "repo" {
				return `{"owner":{"login":"o"},"name":"r"}`, nil
			}
			if name == "gh" && args[0] == "api" {
				q := graphqlQueryArg(args)
				if strings.Contains(q, "submitPullRequestReview") {
					return `{"data":{"submitPullRequestReview":{"pullRequestReview":{"id":"PRR_new","state":"APPROVED"}}}}`, nil
				}
				if strings.Contains(q, "addPullRequestReview(") && !strings.Contains(q, "Thread") {
					createCalled = true
					return `{"data":{"addPullRequestReview":{"pullRequestReview":{"id":"PRR_new"}}}}`, nil
				}
				// discover: no pending review
				return `{"data":{"repository":{"pullRequest":{"id":"PR_01","reviews":{"nodes":[]}}}}}`, nil
			}
			if args[0] == "label" || (args[0] == "pr" && args[1] == "edit") {
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %v", args)
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "10", "--merge-confidence", "high"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	if !createCalled {
		t.Error("expected createReview mutation to be called (IC-001), but it wasn't")
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
	if payload["inlineCommentCount"] != float64(0) {
		t.Errorf("inlineCommentCount: got %v, want 0", payload["inlineCommentCount"])
	}
}

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
		t.Error("gh should not be called for invalid verdict")
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Error("log file should not exist when verdict validation fails")
	}
}

func TestRunSubmitReview_GhFailureNoEvent(t *testing.T) {
	// GraphQL submit fails → no event written.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-gh-fail",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	exec := fakeGraphQLSubmit("PR_01", "PRR_01", 0, "submit")

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "999", "--merge-confidence", "high"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got == 0 {
		t.Fatalf("exit code: got 0, want != 0")
	}
	if !strings.Contains(stderr.String(), "Failed to submit review") {
		t.Errorf("stderr missing 'Failed to submit review', got: %q", stderr.String())
	}

	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatal(err)
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

	exec := fakeExecutor{runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
		return "", fmt.Errorf("should not be called")
	}}

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

	exec := fakeExecutor{runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
		return "", fmt.Errorf("should not be called")
	}}

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

	exec := fakeExecutor{runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
		return "", fmt.Errorf("should not be called")
	}}

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

	exec := fakeExecutor{runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
		return "", fmt.Errorf("should not be called")
	}}

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

	exec := fakeExecutor{runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
		return "", fmt.Errorf("should not be called")
	}}

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

	exec := fakeExecutor{runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
		return "", fmt.Errorf("should not be called")
	}}

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
		t.Errorf("submit-review should not say 'not implemented', got: %q", msg)
	}
	if !strings.Contains(msg, "Missing required environment variable") && !strings.Contains(msg, "Invalid merge confidence") {
		t.Errorf("stderr should mention missing env vars or invalid merge confidence, got: %q", msg)
	}
}

func TestRunSubmitReview_EventLogPathInvalidAborts(t *testing.T) {
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-invalid-log",
		"GOLEMIC_EVENT_LOG": t.TempDir(), // directory, not a file
		"GOLEMIC_TURN_ID":   "1",
	}

	var called bool
	exec := fakeExecutor{runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
		called = true
		return "", fmt.Errorf("should not be called")
	}}

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
		t.Error("gh should NOT be called when event log path is invalid")
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
	exec := fakeExecutor{runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
		called = true
		return "", fmt.Errorf("should not be called")
	}}
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
	exec := fakeExecutor{runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
		called = true
		return "", fmt.Errorf("should not be called")
	}}
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
			if name == "gh" && args[0] == "api" {
				q := graphqlQueryArg(args)
				if strings.Contains(q, "submitPullRequestReview") {
					return `{"data":{"submitPullRequestReview":{"pullRequestReview":{"id":"PRR_01","state":"APPROVED"}}}}`, nil
				}
				if strings.Contains(q, "addPullRequestReview(") && !strings.Contains(q, "Thread") {
					return `{"data":{"addPullRequestReview":{"pullRequestReview":{"id":"PRR_01"}}}}`, nil
				}
				return `{"data":{"repository":{"pullRequest":{"id":"PR_01","reviews":{"nodes":[]}}}}}`, nil
			}
			if args[0] == "label" && args[1] == "create" {
				return "", nil
			}
			if args[0] == "pr" && args[1] == "edit" {
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

func TestRunSubmitReview_MergeConfidenceLow_WrittenToPayload(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-mc-low",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}
	exec := fakeGraphQLSubmit("PR_07", "PRR_07", 0, "")
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
		t.Errorf("mergeConfidence: got %v", payload["mergeConfidence"])
	}
}

// AC-006: EMPTY_FINDINGS fail-closed: changes_requested with empty body rejects before any gh call.
func TestRunSubmitReview_ChangesRequestedEmptyBody_AC006(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-empty-findings",
		"GOLEMIC_EVENT_LOG": filepath.Join(dir, "events.jsonl"),
		"GOLEMIC_TURN_ID":   "1",
	}
	var called bool
	exec := fakeExecutor{runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
		called = true
		return "", fmt.Errorf("should not be called")
	}}
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "changes_requested", "--body", "", "--pr", "1", "--merge-confidence", "low"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)
	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "--body must not be empty") {
		t.Errorf("stderr: %q", stderr.String())
	}
	if called {
		t.Error("gh should not be called when body is empty")
	}
}

// AC-009 / BR-008: grep-guard — no .go file outside this test must contain `gh pr review`.
// The check looks for the executor arg pattern that would invoke `gh pr review`.
func TestGhPrReviewAbsent(t *testing.T) {
	// The forbidden pattern as seen in Go executor arg slices.
	forbidden := `"pr", "review"`
	// Walk the Go source tree from the module root.
	root := "../.."
	self, _ := filepath.Abs("submit_review_test.go")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		abs, _ := filepath.Abs(path)
		if abs == self {
			return nil // skip this guard file itself
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(content), forbidden) {
			t.Errorf("BR-008: found %q in %s — remove it (submit-review must use GraphQL, not gh pr review)", forbidden, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk error: %v", err)
	}
}
