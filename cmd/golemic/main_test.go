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

// fakeExecutor implements preflight.Executor for testing.
type fakeExecutor struct {
	runFunc        func(name string, args ...string) (string, error)
	runWithEnvFunc func(env map[string]string, name string, args ...string) (string, error)
}

func (f fakeExecutor) Run(name string, args ...string) (string, error) {
	if f.runFunc != nil {
		return f.runFunc(name, args...)
	}
	return "", fmt.Errorf("not mocked: %s %v", name, args)
}

func (f fakeExecutor) RunWithEnv(env map[string]string, name string, args ...string) (string, error) {
	if f.runWithEnvFunc != nil {
		return f.runWithEnvFunc(env, name, args...)
	}
	if f.runFunc != nil {
		return f.runFunc(name, args...)
	}
	return "", fmt.Errorf("not mocked: %s %v", name, args)
}

func TestRun(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		wantExit       int
		wantStdoutSub  string // empty means stdout must be empty
		wantStderrSubs []string
	}{
		{
			name:           "no arguments prints usage to stderr",
			args:           []string{"golemic"},
			wantExit:       1,
			wantStderrSubs: []string{"Usage: golemic"},
		},
		{
			name:           "unknown command prints error to stderr",
			args:           []string{"golemic", "does-not-exist"},
			wantExit:       1,
			wantStderrSubs: []string{"Unknown command: does-not-exist", "Usage: golemic"},
		},

		{
			name:           "run without --issue prints usage error",
			args:           []string{"golemic", "run"},
			wantExit:       1,
			wantStderrSubs: []string{"--issue must be a positive integer"},
		},
		{
			name:           "emit dispatches to runEmit",
			args:           []string{"golemic", "emit"},
			wantExit:       1,
			wantStderrSubs: []string{"Missing required environment variable"},
		},
		{
			name:           "open-pr without flags fails with env var error",
			args:           []string{"golemic", "open-pr"},
			wantExit:       1,
			wantStderrSubs: []string{"Missing required environment variable"},
		},
		{
			name:           "submit-review not implemented",
			args:           []string{"golemic", "submit-review"},
			wantExit:       1,
			wantStderrSubs: []string{"not implemented"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := run(tc.args, &stdout, &stderr)
			if got != tc.wantExit {
				t.Errorf("exit code: got %d, want %d", got, tc.wantExit)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout must be empty for error states, got: %q", stdout.String())
			}
			for _, sub := range tc.wantStderrSubs {
				if !strings.Contains(stderr.String(), sub) {
					t.Errorf("stderr missing %q; got: %q", sub, stderr.String())
				}
			}
		})
	}
}

// -----------------------------------------------------------------------------------
// runEmit tests
// ---------------------------------------------------------------------------

func TestRunEmit_Success(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-1",
		"GOLEMIC_EVENT_LOG": logPath,
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "{}"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty, got: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got: %q", stderr.String())
	}

	// Verify exactly one event was written.
	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != "dev_started" {
		t.Errorf("Type: got %q, want %q", ev.Type, "dev_started")
	}
	if ev.RunID != "run-emit-1" {
		t.Errorf("RunID: got %q, want %q", ev.RunID, "run-emit-1")
	}
	// Verify ts is a valid RFC3339 timestamp.
	if _, err := time.Parse(time.RFC3339, ev.Ts); err != nil {
		t.Errorf("Ts is not valid RFC3339: %q (err: %v)", ev.Ts, err)
	}
	// Verify payload is the normalized empty object.
	if string(ev.Payload) != "{}" {
		t.Errorf("Payload: got %s, want %s", string(ev.Payload), "{}")
	}
}

func TestRunEmit_EmptyType(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-2",
		"GOLEMIC_EVENT_LOG": logPath,
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "", "--payload", "{}"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "--type must not be empty") {
		t.Errorf("stderr missing error message, got: %q", stderr.String())
	}

	// No event should be written.
	var r eventlog.Reader
	_, err := r.Read(logPath)
	if err == nil {
		t.Error("expected error reading log (no events should exist)")
	}
}

func TestRunEmit_InvalidPayloadJSON(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-3",
		"GOLEMIC_EVENT_LOG": logPath,
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "not-json"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "Invalid --payload:") {
		t.Errorf("stderr missing 'Invalid --payload:', got: %q", stderr.String())
	}

	// No event should be written.
	var r eventlog.Reader
	_, err := r.Read(logPath)
	if err == nil {
		t.Error("expected error reading log (no events should exist)")
	}
}

func TestRunEmit_PayloadNotObject(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-4",
		"GOLEMIC_EVENT_LOG": logPath,
	}

	tests := []struct {
		payload string
		desc    string
	}{
		{`[1,2,3]`, "array"},
		{`"string"`, "string"},
		{`42`, "number"},
		{`null`, "null"},
		{`true`, "boolean"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := []string{"golemic", "emit", "--type", "dev_started", "--payload", tt.payload}
			got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

			if got != 1 {
				t.Fatalf("exit code: got %d, want 1", got)
			}
			if !strings.Contains(stderr.String(), "Invalid --payload:") {
				t.Errorf("stderr missing 'Invalid --payload:', got: %q", stderr.String())
			}
		})
	}
}

func TestRunEmit_MissingRunID(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "",
		"GOLEMIC_EVENT_LOG": logPath,
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "{}"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "GOLEMIC_RUN_ID") {
		t.Errorf("stderr missing GOLEMIC_RUN_ID, got: %q", stderr.String())
	}
	// AC-003: no event written when env var check fails before any I/O.
	if _, err := os.Stat(logPath); err == nil {
		t.Error("log file should not exist when env var check fails before any I/O")
	}
}

func TestRunEmit_MissingEventLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-5",
		"GOLEMIC_EVENT_LOG": "",
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "{}"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "GOLEMIC_EVENT_LOG") {
		t.Errorf("stderr missing GOLEMIC_EVENT_LOG, got: %q", stderr.String())
	}
	// AC-003: no event written when env var check fails before any I/O.
	if _, err := os.Stat(logPath); err == nil {
		t.Error("log file should not exist when env var check fails before any I/O")
	}
}

func TestRunEmit_MissingBothEnvVars(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "",
		"GOLEMIC_EVENT_LOG": "",
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "{}"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	msg := stderr.String()
	if !strings.Contains(msg, "GOLEMIC_RUN_ID") || !strings.Contains(msg, "GOLEMIC_EVENT_LOG") {
		t.Errorf("stderr should list both missing vars, got: %q", msg)
	}
	// AC-003: no event written when env var check fails before any I/O.
	if _, err := os.Stat(logPath); err == nil {
		t.Error("log file should not exist when env var check fails before any I/O")
	}
}

func TestRunEmit_WriteFailure(t *testing.T) {
	// Use a directory path (not a regular file) so NewWriter's OpenFile fails
	// with "is a directory" on all platforms.
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-6",
		"GOLEMIC_EVENT_LOG": t.TempDir(),
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "{}"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "Failed to write event:") {
		t.Errorf("stderr missing 'Failed to write event:', got: %q", stderr.String())
	}
}

func TestRunEmit_NormalizesPayload(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-7",
		"GOLEMIC_EVENT_LOG": logPath,
	}

	var stdout, stderr bytes.Buffer
	// Payload with extra whitespace and different key spacing.
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", `{  "key" :  "value"  }`}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

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
	// Re-encoded payload should be compact JSON.
	if string(events[0].Payload) != `{"key":"value"}` {
		t.Errorf("Payload not normalized: got %s, want %s", string(events[0].Payload), `{"key":"value"}`)
	}
}

func TestRunEmit_UnknownEventTypeAllowed(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-8",
		"GOLEMIC_EVENT_LOG": logPath,
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "my_custom_progress", "--payload", `{"step":3}`}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

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
	if events[0].Type != "my_custom_progress" {
		t.Errorf("Type: got %q, want %q", events[0].Type, "my_custom_progress")
	}
}

func TestRunEmit_WithPayload(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-9",
		"GOLEMIC_EVENT_LOG": logPath,
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "run_finished", "--payload", `{"outcome":"success"}`}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

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
	ev := events[0]
	if ev.Type != "run_finished" {
		t.Errorf("Type: got %q, want %q", ev.Type, "run_finished")
	}
	if ev.RunID != "run-emit-9" {
		t.Errorf("RunID: got %q, want %q", ev.RunID, "run-emit-9")
	}
	if string(ev.Payload) != `{"outcome":"success"}` {
		t.Errorf("Payload: got %s, want %s", string(ev.Payload), `{"outcome":"success"}`)
	}
}

func TestRunEmit_ArbitraryOrderFlags(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-10",
		"GOLEMIC_EVENT_LOG": logPath,
	}

	var stdout, stderr bytes.Buffer
	// --payload before --type
	args := []string{"golemic", "emit", "--payload", `{"x":1}`, "--type", "test_type"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

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
	if events[0].Type != "test_type" {
		t.Errorf("Type: got %q, want %q", events[0].Type, "test_type")
	}
}

func TestRunEmit_EnvVarsCheckedBeforePayloadParse(t *testing.T) {
	// Missing env var should fail before payload validation.
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "",
		"GOLEMIC_EVENT_LOG": "",
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "not-json"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	// Error should mention env vars, not payload.
	msg := stderr.String()
	if !strings.Contains(msg, "Missing required environment variable") {
		t.Errorf("stderr should mention env vars first, got: %q", msg)
	}
}

func TestRunEmit_EmptyTypeCheckedBeforePayloadParse(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-11",
		"GOLEMIC_EVENT_LOG": logPath,
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "", "--payload", "not-json"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	// Error should mention empty type, not payload.
	msg := stderr.String()
	if !strings.Contains(msg, "--type must not be empty") {
		t.Errorf("stderr should mention empty type first, got: %q", msg)
	}
}

func TestRunEmit_ErrorsToStderrOnly(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "",
		"GOLEMIC_EVENT_LOG": filepath.Join(dir, "events.jsonl"),
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "{}"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout must be empty for error states, got: %q", stdout.String())
	}
}

func TestRunEmitDispatch(t *testing.T) {
	// Test that run() dispatches to runEmit with env vars set.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	t.Setenv("GOLEMIC_RUN_ID", "run-dispatch")
	t.Setenv("GOLEMIC_EVENT_LOG", logPath)

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "{}"}
	got := run(args, &stdout, &stderr)

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
	if events[0].Type != "dev_started" {
		t.Errorf("Type: got %q, want %q", events[0].Type, "dev_started")
	}
}

// -----------------------------------------------------------------------------------
// runOpenPR tests
// ---------------------------------------------------------------------------

func TestRunOpenPR_Success(t *testing.T) {
	// AC-001: Successful open-pr writes event with prNumber, url, branch (exit 0).
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-pr-1",
		"GOLEMIC_EVENT_LOG": logPath,
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

func TestRunPreflight(t *testing.T) {
	tests := []struct {
		name          string
		exec          preflight.Executor
		homeDir       string // empty = create temp
		repoRoot      string // empty = create temp
		wantExit      int
		wantStdout    string // exact expected stdout
	}{
		{
			name: "all checks pass",
			exec: fakeExecutor{
				runFunc: func(name string, args ...string) (string, error) {
					switch name {
					case "gh":
						if len(args) >= 1 && args[0] == "api" && args[1] == "user" {
							return `{"login":"dev-bot"}`, nil
						}
						return "gh version 2.0.0", nil
					case "pi":
						return "pi version 1.0.0", nil
					case "git":
						switch {
						case len(args) >= 1 && args[0] == "config":
							return "https://github.com/owner/repo.git", nil
						case len(args) >= 1 && args[0] == "worktree":
							return "/tmp/repo (main)\n", nil
						default:
							return "git version 2.0.0", nil
						}
					}
					return "", fmt.Errorf("unknown: %s", name)
				},
				runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
					if name == "gh" && len(args) >= 1 && args[0] == "api" && args[1] == "user" {
						token := env["GH_TOKEN"]
						if strings.Contains(token, "dev") {
							return `{"login":"dev-bot"}`, nil
						}
						if strings.Contains(token, "rev") {
							return `{"login":"reviewer-bot"}`, nil
						}
						return `{"login":"unknown"}`, nil
					}
					return "", fmt.Errorf("not mocked")
				},
			},
			wantExit: 0,
			wantStdout: "OK: gh installiert\n" +
				"OK: pi installiert\n" +
				"OK: git\n" +
				"OK: .golemic/ Scaffolding\n" +
				"OK: config.json valide\n" +
				"OK: Credentials\n" +
				"SUCCESS\n",
		},
		{
			name: "gh missing",
			exec: fakeExecutor{
				runFunc: func(name string, args ...string) (string, error) {
					return "", fmt.Errorf("executable file not found")
				},
				runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
					return "", fmt.Errorf("not found")
				},
			},
			wantExit: 1,
			wantStdout: "FEHLT: gh installiert — gh not found: executable file not found\n" +
				"FEHLT: pi installiert — pi not found: executable file not found\n" +
				"FEHLT: git — git not found: executable file not found\n",
			// Remaining lines (scaffolding, config, credentials) depend on temp dir
			// path and are checked via prefix/contains below.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			homeDir := tt.homeDir
			if homeDir == "" {
				homeDir = t.TempDir()
			}
			repoRoot := tt.repoRoot
			if repoRoot == "" {
				repoRoot = t.TempDir()
			}

			// For success case: pre-create valid config and credentials
			if tt.wantExit == 0 {
				// Valid config
				golemicDir := filepath.Join(repoRoot, ".golemic")
				if err := os.MkdirAll(golemicDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(golemicDir, "config.json"), []byte(`{
					"project": "test-project",
					"verify_command": "go test"
				}`), 0644); err != nil {
					t.Fatal(err)
				}
				// Valid credentials (must be 0600)
				credDir := filepath.Join(homeDir, ".golemic", "test-project")
				if err := os.MkdirAll(credDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(`{
					"dev_token": "ghp_dev_token",
					"reviewer_token": "ghp_rev_token"
				}`), 0600); err != nil {
					t.Fatal(err)
				}
			}

			var stdout, stderr bytes.Buffer
			got := runPreflight(tt.exec, homeDir, repoRoot, &stdout, &stderr)

			if got != tt.wantExit {
				t.Errorf("exit code: got %d, want %d", got, tt.wantExit)
			}

			if tt.wantExit == 0 {
				// Success case: exact match
				if stdout.String() != tt.wantStdout {
					t.Errorf("stdout:\n  got:  %q\n  want: %q", stdout.String(), tt.wantStdout)
				}
			} else {
				// Failure case: check prefix (first 3 FEHLT lines are predictable)
				out := stdout.String()
				if !strings.HasPrefix(out, tt.wantStdout) {
					t.Errorf("stdout prefix mismatch:\n  got:  %q\n  want prefix: %q", out, tt.wantStdout)
				}
				// Verify the remaining lines are FEHLT
				if !strings.Contains(out, "FEHLT: .golemic/ Scaffolding") {
					t.Errorf("stdout missing FEHLT: .golemic/ Scaffolding\n  got: %q", out)
				}
				if !strings.Contains(out, "FEHLT: config.json valide") {
					t.Errorf("stdout missing FEHLT: config.json valide\n  got: %q", out)
				}
				if !strings.Contains(out, "FEHLT: Credentials") {
					t.Errorf("stdout missing FEHLT: Credentials\n  got: %q", out)
				}
				// Must NOT contain SUCCESS
				if strings.Contains(out, "SUCCESS") {
					t.Errorf("stdout must not contain SUCCESS when checks fail, got: %q", out)
				}
			}

			if stderr.Len() > 0 {
				t.Errorf("stderr should be empty, got: %q", stderr.String())
			}
		})
	}
}