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

func TestRunSubmitReview_ApprovedSuccess(t *testing.T) { //nolint:cyclop,gocognit,funlen // moved verbatim; cyclomatic 23 and cognitive 32 exceed thresholds on the pre-existing table body
	// AC-001: Approved verdict calls gh --approve and writes event.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-1",
		"GOLEMIC_EVENT_LOG": logPath,
	}

	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 3 && args[0] == "pr" && args[1] == "review" && args[2] == "123" {
				// Verify --approve and --body are present, --request-changes is not.
				hasApprove := false
				hasBody := false
				hasRequestChanges := false
				for i, arg := range args {
					if arg == "--approve" {
						hasApprove = true
					}
					if arg == "--body" && i+1 < len(args) && args[i+1] == "LGTM" {
						hasBody = true
					}
					if arg == "--request-changes" {
						hasRequestChanges = true
					}
				}
				if !hasApprove {
					return "", fmt.Errorf("--approve not found in gh args")
				}
				if !hasBody {
					return "", fmt.Errorf("--body with 'LGTM' not found in gh args")
				}
				if hasRequestChanges {
					return "", fmt.Errorf("--request-changes should not be present for approved verdict")
				}
				return "Review submitted\n", nil
			}
			// label create (idempotent, ignore error)
			if name == "gh" && len(args) >= 2 && args[0] == "label" && args[1] == "create" {
				return "", nil
			}
			// label add to PR
			if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "edit" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "123", "--merge-confidence", "high"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty, got: %q", stdout.String())
	}

	// Verify exactly one review_submitted event was written with correct fields.
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
	// Verify ts is a valid RFC3339 timestamp.
	if _, err := time.Parse(time.RFC3339, ev.Ts); err != nil {
		t.Errorf("Ts is not valid RFC3339: %q (err: %v)", ev.Ts, err)
	}
	// Verify payload contains verdict, body, prNumber
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
}

func TestRunSubmitReview_ChangesRequestedSuccess(t *testing.T) { //nolint:cyclop,gocognit // moved verbatim; cyclomatic 22 and cognitive 35 exceed thresholds on the pre-existing body
	// AC-002: changes_requested verdict calls gh --request-changes and writes event.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-2",
		"GOLEMIC_EVENT_LOG": logPath,
	}

	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 3 && args[0] == "pr" && args[1] == "review" && args[2] == "456" { //nolint:nestif // moved verbatim; complexity pre-dates split
				// Verify --request-changes and --body are present.
				hasRequestChanges := false
				hasBody := false
				hasApprove := false
				for i, arg := range args {
					if arg == "--request-changes" {
						hasRequestChanges = true
					}
					if arg == "--body" && i+1 < len(args) && args[i+1] == "Fix NPE" {
						hasBody = true
					}
					if arg == "--approve" {
						hasApprove = true
					}
				}
				if !hasRequestChanges {
					return "", fmt.Errorf("--request-changes not found")
				}
				if !hasBody {
					return "", fmt.Errorf("--body with 'Fix NPE' not found")
				}
				if hasApprove {
					return "", fmt.Errorf("--approve should not be present for changes_requested")
				}
				return "Review submitted\n", nil
			}
			if name == "gh" && len(args) >= 2 && args[0] == "label" && args[1] == "create" {
				return "", nil
			}
			if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "edit" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "changes_requested", "--body", "Fix NPE", "--pr", "456", "--merge-confidence", "low"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got: %q", stderr.String())
	}

	// Verify exactly one review_submitted event was written.
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
	// Verify payload contains changes_requested verdict
	var payload map[string]interface{}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload["verdict"] != "changes_requested" {
		t.Errorf("verdict: got %q, want %q", payload["verdict"], "changes_requested")
	}
	if payload["mergeConfidence"] != "low" {
		t.Errorf("mergeConfidence: got %q, want %q", payload["mergeConfidence"], "low")
	}
}

func TestRunSubmitReview_InvalidVerdictNoGhCall(t *testing.T) {
	// AC-003: Invalid verdict returns error without gh call.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-invalid",
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
	args := []string{"golemic", "submit-review", "--verdict", "invalid", "--body", "text", "--pr", "123", "--merge-confidence", "high"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "Invalid verdict") {
		t.Errorf("stderr missing 'Invalid verdict', got: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "approved") || !strings.Contains(stderr.String(), "changes_requested") {
		t.Errorf("stderr should mention valid verdict values, got: %q", stderr.String())
	}
	if called {
		t.Error("gh command should not have been called for invalid verdict")
	}
	// No event should be written.
	if _, err := os.Stat(logPath); err == nil {
		t.Error("log file should not exist when verdict validation fails")
	}
}

func TestRunSubmitReview_GhFailureNoEvent(t *testing.T) {
	// AC-004: gh failure results in no event and non-zero exit.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-gh-fail",
		"GOLEMIC_EVENT_LOG": logPath,
	}

	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 3 && args[0] == "pr" && args[1] == "review" {
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
	// No event should be written (log file exists but is empty).
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
	// Test that verdict is case-sensitive: 'Approved' should fail.
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-case",
		"GOLEMIC_EVENT_LOG": filepath.Join(t.TempDir(), "events.jsonl"),
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
	// Test that run() dispatches to runSubmitReview (not "not implemented").
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
	// Dispatch reached runSubmitReview: expect env var error or merge-confidence error
	// (which error fires first depends on whether env vars are set in the test environment).
	if !strings.Contains(msg, "Missing required environment variable") && !strings.Contains(msg, "Invalid merge confidence") {
		t.Errorf("stderr should mention missing env vars or invalid merge confidence, got: %q", msg)
	}
}

func TestRunSubmitReview_EventLogPathInvalidAborts(t *testing.T) {
	// Atomic coupling: if event log path is not writable, abort before calling gh.
	// This ensures: either both (gh + event) succeed, or neither is attempted.
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-review-invalid-log",
		"GOLEMIC_EVENT_LOG": t.TempDir(), // directory, not a file — NewWriter should fail
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
	}
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "review" {
				return "Review submitted\n", nil
			}
			if name == "gh" && args[0] == "label" && args[1] == "create" {
				return "", nil // label create succeeds
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
	}
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "review" {
				return "Review submitted\n", nil
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
}
