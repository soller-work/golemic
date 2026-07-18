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

func TestRunOpenPR_Success(t *testing.T) { //nolint:cyclop // moved verbatim; cyclomatic complexity 21 exceeds threshold
	// AC-001: Successful open-pr writes event with prNumber, url, branch (exit 0).
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-pr-1",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":    "1",
	}

	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && len(args) == 2 && args[0] == "branch" && args[1] == "--show-current" {
				return "feature/my-branch\n", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "create" {
				return "https://github.com/owner/repo/pull/42\n", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "open-pr", "--title", "My PR", "--body", "Description"}
	got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got: %q", stderr.String())
	}
	// stdout should contain the PR URL
	if !strings.Contains(stdout.String(), "https://github.com/owner/repo/pull/42") {
		t.Errorf("stdout should contain PR URL, got: %q", stdout.String())
	}

	// Verify exactly one pr_opened event was written with correct fields.
	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != eventlog.EventPROpened {
		t.Errorf("Type: got %q, want %q", ev.Type, eventlog.EventPROpened)
	}
	if ev.RunID != "run-pr-1" {
		t.Errorf("RunID: got %q, want %q", ev.RunID, "run-pr-1")
	}
	// Verify ts is a valid RFC3339 timestamp.
	if _, err := time.Parse(time.RFC3339, ev.Ts); err != nil {
		t.Errorf("Ts is not valid RFC3339: %q (err: %v)", ev.Ts, err)
	}
	// Verify payload contains prNumber, url, branch
	var payload map[string]string
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload["prNumber"] != "42" {
		t.Errorf("prNumber: got %q, want %q", payload["prNumber"], "42")
	}
	if payload["url"] != "https://github.com/owner/repo/pull/42" {
		t.Errorf("url: got %q, want %q", payload["url"], "https://github.com/owner/repo/pull/42")
	}
	if payload["branch"] != "feature/my-branch" {
		t.Errorf("branch: got %q, want %q", payload["branch"], "feature/my-branch")
	}
}

func TestRunOpenPR_Success_StdoutPRURL(t *testing.T) {
	// Verify stdout contains the PR URL text.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-pr-prurl",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":    "1",
	}

	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			return "feature/branch", nil
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "https://github.com/owner/repo/pull/123\n", nil
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "open-pr", "--title", "Title", "--body", "Body"}
	got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	expectedURL := "https://github.com/owner/repo/pull/123\n"
	if stdout.String() != expectedURL {
		t.Errorf("stdout: got %q, want %q", stdout.String(), expectedURL)
	}
}

func TestRunOpenPR_GhFailure(t *testing.T) {
	// AC-002: gh failure results in no event and non-zero exit.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-pr-2",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":    "1",
	}

	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			return "feature/branch", nil
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "", &preflight.ErrExit{ExitCode: 1, Stderr: "pull request create failed: graphql error"}
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "open-pr", "--title", "My PR", "--body", "Description"}
	got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got == 0 {
		t.Fatalf("exit code: got 0, want != 0")
	}
	if !strings.Contains(stderr.String(), "Failed to create PR:") {
		t.Errorf("stderr missing 'Failed to create PR:', got: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "graphql error") {
		t.Errorf("stderr should contain gh stderr, got: %q", stderr.String())
	}
	// AC-002: No event should be written.
	var r eventlog.Reader
	_, err := r.Read(logPath)
	if err == nil {
		t.Error("expected error reading log (no events should exist)")
	}
}

func TestRunOpenPR_MissingEnvVar_RunID(t *testing.T) {
	// AC-003: Missing GOLEMIC_RUN_ID causes non-zero exit before any gh call.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":    "1",
	}

	exec := fakeExecutor{
		runFunc:        func(name string, args ...string) (string, error) { return "", fmt.Errorf("should not be called") },
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) { return "", fmt.Errorf("should not be called") },
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "open-pr", "--title", "My PR", "--body", "Description"}
	got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "GOLEMIC_RUN_ID") {
		t.Errorf("stderr missing GOLEMIC_RUN_ID, got: %q", stderr.String())
	}
	// No event should be written.
	if _, err := os.Stat(logPath); err == nil {
		t.Error("log file should not exist when env var check fails")
	}
}

func TestRunOpenPR_MissingEnvVar_EventLog(t *testing.T) {
	// AC-003: Missing GOLEMIC_EVENT_LOG causes non-zero exit before any gh call.
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-pr-missing-el",
		"GOLEMIC_EVENT_LOG": "",
	}

	var called bool
	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			called = true
			return "", fmt.Errorf("should not be called")
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			called = true
			return "", fmt.Errorf("should not be called")
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "open-pr", "--title", "My PR", "--body", "Description"}
	got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "GOLEMIC_EVENT_LOG") {
		t.Errorf("stderr missing GOLEMIC_EVENT_LOG, got: %q", stderr.String())
	}
	if called {
		t.Error("external command should not have been called when env var is missing")
	}
}

func TestRunOpenPR_MissingBothEnvVars(t *testing.T) {
	// AC-003: Both missing env vars listed in error.
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "",
		"GOLEMIC_EVENT_LOG": "",
	}

	exec := fakeExecutor{
		runFunc:        func(name string, args ...string) (string, error) { return "", fmt.Errorf("should not be called") },
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) { return "", fmt.Errorf("should not be called") },
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "open-pr", "--title", "My PR", "--body", "Description"}
	got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	msg := stderr.String()
	if !strings.Contains(msg, "GOLEMIC_RUN_ID") || !strings.Contains(msg, "GOLEMIC_EVENT_LOG") {
		t.Errorf("stderr should list both missing vars, got: %q", msg)
	}
}

func TestRunOpenPR_EmptyTitle(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-pr-empty-title",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":    "1",
	}

	exec := fakeExecutor{
		runFunc:        func(name string, args ...string) (string, error) { return "", fmt.Errorf("should not be called") },
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) { return "", fmt.Errorf("should not be called") },
	}

	var stdout, stderr bytes.Buffer
	// Empty --title, valid --body
	args := []string{"golemic", "open-pr", "--title", "", "--body", "Body"}
	got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "--title must not be empty") {
		t.Errorf("stderr missing '--title must not be empty', got: %q", stderr.String())
	}
	// No event should be written.
	if _, err := os.Stat(logPath); err == nil {
		t.Error("log file should not exist when validation fails")
	}
}

func TestRunOpenPR_EmptyBody(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-pr-empty-body",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":    "1",
	}

	exec := fakeExecutor{
		runFunc:        func(name string, args ...string) (string, error) { return "", fmt.Errorf("should not be called") },
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) { return "", fmt.Errorf("should not be called") },
	}

	var stdout, stderr bytes.Buffer
	// Valid --title, empty --body
	args := []string{"golemic", "open-pr", "--title", "Title", "--body", ""}
	got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "--body must not be empty") {
		t.Errorf("stderr missing '--body must not be empty', got: %q", stderr.String())
	}
}

func TestRunOpenPR_BranchResolutionFailure(t *testing.T) {
	// git branch --show-current fails.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-pr-branch-fail",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":    "1",
	}

	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			return "", fmt.Errorf("fatal: not a git repository")
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "", fmt.Errorf("should not be called")
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "open-pr", "--title", "My PR", "--body", "Description"}
	got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "Failed to determine current branch") {
		t.Errorf("stderr missing branch error, got: %q", stderr.String())
	}
	// No event should be written.
	var r eventlog.Reader
	_, err := r.Read(logPath)
	if err == nil {
		t.Error("expected error reading log (no events should exist)")
	}
}

func TestRunOpenPR_DetachedHead(t *testing.T) {
	// git branch --show-current returns empty (detached HEAD).
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-pr-detached",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":    "1",
	}

	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			return "", nil
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "", fmt.Errorf("should not be called")
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "open-pr", "--title", "My PR", "--body", "Description"}
	got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "detached HEAD") {
		t.Errorf("stderr should mention detached HEAD, got: %q", stderr.String())
	}
}

func TestRunOpenPR_PRParseFailure_EmptyOutput(t *testing.T) {
	// gh returns empty output.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-pr-parse",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":    "1",
	}

	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			return "feature/branch", nil
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "", nil // empty output
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "open-pr", "--title", "My PR", "--body", "Description"}
	got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "Failed to parse PR number/URL from gh output") {
		t.Errorf("stderr missing parse error, got: %q", stderr.String())
	}
}

func TestRunOpenPR_PRParseFailure_NoNumericSuffix(t *testing.T) {
	// gh returns a URL without a numeric last path segment.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-pr-parse2",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":    "1",
	}

	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			return "feature/branch", nil
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "https://github.com/owner/repo/pull/abc\n", nil
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "open-pr", "--title", "My PR", "--body", "Description"}
	got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "Failed to parse PR number/URL from gh output") {
		t.Errorf("stderr missing parse error, got: %q", stderr.String())
	}
}

func TestRunOpenPR_ArbitraryFlagOrder(t *testing.T) {
	// Flags can be in any order.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-pr-order",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":    "1",
	}

	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			return "feature/branch", nil
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "https://github.com/owner/repo/pull/7\n", nil
		},
	}

	var stdout, stderr bytes.Buffer
	// --body before --title
	args := []string{"golemic", "open-pr", "--body", "Body text", "--title", "Title text"}
	got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

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
	if events[0].Type != eventlog.EventPROpened {
		t.Errorf("Type: got %q, want %q", events[0].Type, eventlog.EventPROpened)
	}
}

func TestRunOpenPR_ErrorsToStderrOnly(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "",
		"GOLEMIC_EVENT_LOG": filepath.Join(dir, "events.jsonl"),
	}

	exec := fakeExecutor{
		runFunc:        func(name string, args ...string) (string, error) { return "", fmt.Errorf("should not be called") },
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) { return "", fmt.Errorf("should not be called") },
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "open-pr", "--title", "My PR", "--body", "Description"}
	got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout must be empty for error states, got: %q", stdout.String())
	}
}

func TestRunOpenPR_EnvVarsCheckedBeforeValidation(t *testing.T) {
	// Missing env var should fail before --title/--body validation.
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "",
		"GOLEMIC_EVENT_LOG": "",
	}

	var called bool
	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			called = true
			return "", nil
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			called = true
			return "", nil
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "open-pr", "--title", "", "--body", ""}
	got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	// Error should mention env vars, not empty flags.
	msg := stderr.String()
	if !strings.Contains(msg, "Missing required environment variable") {
		t.Errorf("stderr should mention env vars first, got: %q", msg)
	}
	if called {
		t.Error("external command should not have been called")
	}
}

func TestRunOpenPR_Dispatch(t *testing.T) {
	// Test that run() dispatches to runOpenPR (not "not implemented").
	// We can't fully mock osExecutor here, so we verify the dispatch path
	// by checking it fails with a meaningful open-pr error, not "not implemented".
	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "open-pr", "--title", "Title", "--body", "Body"}
	got := run(args, &stdout, &stderr)

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	msg := stderr.String()
	if strings.Contains(msg, "not implemented") {
		t.Errorf("open-pr should no longer say 'not implemented', got: %q", msg)
	}
	// The dispatch should reach runOpenPR which will hit env var validation
	// (no env vars set in test) and fail there.
	if !strings.Contains(msg, "Missing required environment variable") {
		t.Errorf("stderr should mention missing env vars in default env, got: %q", msg)
	}
}
