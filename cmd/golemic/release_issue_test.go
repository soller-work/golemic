package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// releaseIssueRun runs runReleaseIssue with a controlled environment and executor.
func releaseIssueRun( //nolint:cyclop
	t *testing.T,
	homeDir, repoRoot string,
	envOverrides map[string]string,
	ghResponses func(env map[string]string, name string, args []string) (string, error),
	extraArgs ...string,
) (int, string, string, string) { // code, stdout, stderr, eventLogPath
	t.Helper()

	origHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", homeDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Setenv("HOME", origHome) }()

	for _, k := range []string{"GOLEMIC_DEV_TOKEN", "GOLEMIC_REVIEWER_TOKEN"} {
		orig := os.Getenv(k)
		_ = os.Unsetenv(k)
		defer func(key, val string) { _ = os.Setenv(key, val) }(k, orig)
	}

	eventLogPath := filepath.Join(t.TempDir(), "events.jsonl")

	envDefaults := map[string]string{
		"GOLEMIC_RUN_ID":    "run-test-001",
		"GOLEMIC_EVENT_LOG": eventLogPath,
		"GOLEMIC_TURN_ID":   "0",
	}
	for k, v := range envOverrides {
		envDefaults[k] = v
	}

	getenv := func(key string) string { return envDefaults[key] }

	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
				return repoRoot + "\n", nil
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
				return repoRoot + "\n", nil
			}
			if ghResponses != nil {
				return ghResponses(env, name, args)
			}
			return "", fmt.Errorf("unexpected RunWithEnv: %s %v", name, args)
		},
	}

	defaultArgs := []string{"golemic", "release-issue", "--number", "42", "--reason", "done"}
	args := append(defaultArgs[:4:4], extraArgs...)
	var stdout, stderr bytes.Buffer
	code := runReleaseIssue(args, &stdout, &stderr, getenv, exec)
	return code, stdout.String(), stderr.String(), eventLogPath
}

// releaseIssueViewJSON returns a minimal gh issue view JSON response.
func releaseIssueViewJSON(labels []string, assignees []string) string {
	lblParts := make([]string, len(labels))
	for i, l := range labels {
		lblParts[i] = fmt.Sprintf(`{"name":%q}`, l)
	}
	asgParts := make([]string, len(assignees))
	for i, a := range assignees {
		asgParts[i] = fmt.Sprintf(`{"login":%q}`, a)
	}
	return fmt.Sprintf(`{"labels":[%s],"assignees":[%s]}`,
		strings.Join(lblParts, ","), strings.Join(asgParts, ","))
}

// AC-001: Happy path reason=done — lock cleared, event written, exit 0.
func TestReleaseIssue_AC001_HappyPathDone(t *testing.T) { //nolint:cyclop,gocognit
	homeDir, repoRoot := claimIssueFixture(t, testProject, testToken)

	issueView := releaseIssueViewJSON([]string{"in-progress"}, []string{testDevLogin})

	var capturedGHToken string
	var capturedEditArgs []string

	code, stdout, stderr, eventLogPath := releaseIssueRun(t, homeDir, repoRoot, nil,
		func(env map[string]string, name string, args []string) (string, error) {
			if tok := env["GH_TOKEN"]; tok != "" {
				capturedGHToken = tok
			}
			switch {
			case name == "git" && args[0] == "rev-parse":
				return repoRoot + "\n", nil
			case name == "gh" && args[0] == "api" && args[1] == "user":
				return userAPIResponse(testDevLogin), nil
			case name == "gh" && args[0] == "issue" && args[1] == "view":
				return issueView, nil
			case name == "gh" && args[0] == "issue" && args[1] == "edit":
				capturedEditArgs = args
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
		"--reason", "done",
	)

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "released issue #42 as done") {
		t.Errorf("stdout should contain 'released issue #42 as done', got: %q", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr should be empty, got: %q", stderr)
	}
	if capturedGHToken != testToken {
		t.Errorf("GH_TOKEN: got %q, want %q", capturedGHToken, testToken)
	}
	if strings.Contains(stdout, testToken) || strings.Contains(stderr, testToken) {
		t.Error("dev_token must not appear in stdout or stderr")
	}

	// done: should not add any extra label.
	for _, a := range capturedEditArgs {
		if a == "needs-human" || a == "ready-for-agent" {
			t.Errorf("reason=done must not add label %q", a)
		}
	}
	// should remove in-progress.
	found := false
	for i, a := range capturedEditArgs {
		if a == "--remove-label" && i+1 < len(capturedEditArgs) && capturedEditArgs[i+1] == "in-progress" {
			found = true
		}
	}
	if !found {
		t.Error("edit args must include --remove-label in-progress")
	}

	// Verify issue_released event was written.
	data, err := os.ReadFile(eventLogPath)
	if err != nil {
		t.Fatalf("event log not written: %v", err)
	}
	var ev map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(data), &ev); err != nil {
		t.Fatalf("event log JSON invalid: %v", err)
	}
	if ev["type"] != "issue_released" {
		t.Errorf("event type: got %v, want issue_released", ev["type"])
	}
	payload, _ := ev["payload"].(map[string]interface{})
	if payload["issue_number"] != float64(42) {
		t.Errorf("event payload issue_number: got %v, want 42", payload["issue_number"])
	}
	if payload["reason"] != "done" {
		t.Errorf("event payload reason: got %v, want done", payload["reason"])
	}
}

// AC-002: reason=failed adds needs-human label.
func TestReleaseIssue_AC002_ReasonFailed(t *testing.T) { //nolint:cyclop
	homeDir, repoRoot := claimIssueFixture(t, testProject, testToken)

	issueView := releaseIssueViewJSON([]string{"in-progress"}, []string{testDevLogin})
	var capturedEditArgs []string

	code, stdout, stderr, eventLogPath := releaseIssueRun(t, homeDir, repoRoot, nil,
		func(_ map[string]string, name string, args []string) (string, error) {
			switch {
			case name == "git" && args[0] == "rev-parse":
				return repoRoot + "\n", nil
			case name == "gh" && args[0] == "api" && args[1] == "user":
				return userAPIResponse(testDevLogin), nil
			case name == "gh" && args[0] == "issue" && args[1] == "view":
				return issueView, nil
			case name == "gh" && args[0] == "issue" && args[1] == "edit":
				capturedEditArgs = args
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
		"--reason", "failed",
	)

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "released issue #42 as failed") {
		t.Errorf("stdout: got %q", stdout)
	}

	// should add needs-human.
	found := false
	for i, a := range capturedEditArgs {
		if a == "--add-label" && i+1 < len(capturedEditArgs) && capturedEditArgs[i+1] == "needs-human" {
			found = true
		}
	}
	if !found {
		t.Error("reason=failed must add --add-label needs-human; args: " + strings.Join(capturedEditArgs, " "))
	}

	// Verify event reason=failed.
	data, _ := os.ReadFile(eventLogPath)
	var ev map[string]interface{}
	_ = json.Unmarshal(bytes.TrimSpace(data), &ev)
	payload, _ := ev["payload"].(map[string]interface{})
	if payload["reason"] != "failed" {
		t.Errorf("event payload reason: got %v, want failed", payload["reason"])
	}
}

// AC-003: reason=abandoned restores ready-for-agent.
func TestReleaseIssue_AC003_ReasonAbandoned(t *testing.T) { //nolint:cyclop
	homeDir, repoRoot := claimIssueFixture(t, testProject, testToken)

	issueView := releaseIssueViewJSON([]string{"in-progress"}, []string{testDevLogin})
	var capturedEditArgs []string

	code, stdout, stderr, eventLogPath := releaseIssueRun(t, homeDir, repoRoot, nil,
		func(_ map[string]string, name string, args []string) (string, error) {
			switch {
			case name == "git" && args[0] == "rev-parse":
				return repoRoot + "\n", nil
			case name == "gh" && args[0] == "api" && args[1] == "user":
				return userAPIResponse(testDevLogin), nil
			case name == "gh" && args[0] == "issue" && args[1] == "view":
				return issueView, nil
			case name == "gh" && args[0] == "issue" && args[1] == "edit":
				capturedEditArgs = args
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
		"--reason", "abandoned",
	)

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "released issue #42 as abandoned") {
		t.Errorf("stdout: got %q", stdout)
	}

	// should add ready-for-agent.
	found := false
	for i, a := range capturedEditArgs {
		if a == "--add-label" && i+1 < len(capturedEditArgs) && capturedEditArgs[i+1] == "ready-for-agent" {
			found = true
		}
	}
	if !found {
		t.Error("reason=abandoned must add --add-label ready-for-agent; args: " + strings.Join(capturedEditArgs, " "))
	}

	data, _ := os.ReadFile(eventLogPath)
	var ev map[string]interface{}
	_ = json.Unmarshal(bytes.TrimSpace(data), &ev)
	payload, _ := ev["payload"].(map[string]interface{})
	if payload["reason"] != "abandoned" {
		t.Errorf("event payload reason: got %v, want abandoned", payload["reason"])
	}
}

// AC-004: Idempotent — already released: exit 0, no event, no edit call.
func TestReleaseIssue_AC004_Idempotent(t *testing.T) { //nolint:cyclop
	homeDir, repoRoot := claimIssueFixture(t, testProject, testToken)

	issueView := releaseIssueViewJSON(nil, nil) // no in-progress, no assignees

	editCalled := false
	code, stdout, stderr, eventLogPath := releaseIssueRun(t, homeDir, repoRoot, nil,
		func(_ map[string]string, name string, args []string) (string, error) {
			switch {
			case name == "git" && args[0] == "rev-parse":
				return repoRoot + "\n", nil
			case name == "gh" && args[0] == "api" && args[1] == "user":
				return userAPIResponse(testDevLogin), nil
			case name == "gh" && args[0] == "issue" && args[1] == "view":
				return issueView, nil
			case name == "gh" && args[0] == "issue" && args[1] == "edit":
				editCalled = true
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
		"--reason", "done",
	)

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "already released issue #42") {
		t.Errorf("stdout should contain 'already released issue #42', got: %q", stdout)
	}
	if editCalled {
		t.Error("edit must not be called on idempotent path")
	}
	if _, err := os.Stat(eventLogPath); !os.IsNotExist(err) {
		t.Error("event log must not be written on idempotent path")
	}
	if stderr != "" {
		t.Errorf("stderr should be empty, got: %q", stderr)
	}
}

// AC-005: Foreign-owned issue — exit 3, no edit, no event.
func TestReleaseIssue_AC005_ForeignClaim(t *testing.T) { //nolint:cyclop
	homeDir, repoRoot := claimIssueFixture(t, testProject, testToken)

	issueView := releaseIssueViewJSON([]string{"in-progress"}, []string{"other-bot"})

	editCalled := false
	code, stdout, stderr, eventLogPath := releaseIssueRun(t, homeDir, repoRoot, nil,
		func(_ map[string]string, name string, args []string) (string, error) {
			switch {
			case name == "git" && args[0] == "rev-parse":
				return repoRoot + "\n", nil
			case name == "gh" && args[0] == "api" && args[1] == "user":
				return userAPIResponse(testDevLogin), nil
			case name == "gh" && args[0] == "issue" && args[1] == "view":
				return issueView, nil
			case name == "gh" && args[0] == "issue" && args[1] == "edit":
				editCalled = true
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
		"--reason", "done",
	)

	if code != 3 {
		t.Fatalf("exit code: got %d, want 3; stdout: %s stderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "issue #42 is claimed by") {
		t.Errorf("stderr should contain 'issue #42 is claimed by', got: %q", stderr)
	}
	if editCalled {
		t.Error("edit must not be called on foreign-claim path")
	}
	if _, err := os.Stat(eventLogPath); !os.IsNotExist(err) {
		t.Error("event log must not be written on foreign-claim path")
	}
	if stdout != "" {
		t.Errorf("stdout should be empty, got: %q", stdout)
	}
}

// AC-006a: Missing env var — exit 1, no gh call.
func TestReleaseIssue_AC006_MissingEnvVar(t *testing.T) {
	_, repoRoot := claimIssueFixture(t, testProject, testToken)

	ghCalled := false
	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
				return repoRoot + "\n", nil
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvFunc: func(_ map[string]string, name string, args ...string) (string, error) {
			if name == "gh" {
				ghCalled = true
			}
			return "", fmt.Errorf("should not be called")
		},
	}

	envOverrides := map[string]string{
		"GOLEMIC_RUN_ID":    "", // explicitly missing
		"GOLEMIC_EVENT_LOG": "some/path",
		"GOLEMIC_TURN_ID":   "0",
	}
	getenv := func(key string) string { return envOverrides[key] }

	args := []string{"golemic", "release-issue", "--number", "42", "--reason", "done"}
	var stdout, stderr bytes.Buffer
	code := runReleaseIssue(args, &stdout, &stderr, getenv, exec)

	if code != 1 {
		t.Fatalf("exit code: got %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "missing required environment variable: GOLEMIC_RUN_ID") {
		t.Errorf("stderr should contain missing var message, got: %q", stderr.String())
	}
	if ghCalled {
		t.Error("gh must not be called when env var is missing")
	}
	if stdout.String() != "" {
		t.Errorf("stdout should be empty, got: %q", stdout.String())
	}
}

// AC-006b: Invalid --reason — exit 1, no gh call.
func TestReleaseIssue_AC006_InvalidReason(t *testing.T) {
	_, repoRoot := claimIssueFixture(t, testProject, testToken)

	ghCalled := false
	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
				return repoRoot + "\n", nil
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvFunc: func(_ map[string]string, name string, _ ...string) (string, error) {
			if name == "gh" {
				ghCalled = true
			}
			return "", fmt.Errorf("should not be called")
		},
	}

	envOverrides := map[string]string{
		"GOLEMIC_RUN_ID":    "run-test-001",
		"GOLEMIC_EVENT_LOG": "some/path",
		"GOLEMIC_TURN_ID":   "0",
	}
	getenv := func(key string) string { return envOverrides[key] }

	args := []string{"golemic", "release-issue", "--number", "42", "--reason", "bogus"}
	var stdout, stderr bytes.Buffer
	code := runReleaseIssue(args, &stdout, &stderr, getenv, exec)

	if code != 1 {
		t.Fatalf("exit code: got %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "invalid --reason: must be one of done|failed|abandoned") {
		t.Errorf("stderr should contain usage error message, got: %q", stderr.String())
	}
	if ghCalled {
		t.Error("gh must not be called on usage error")
	}
	if stdout.String() != "" {
		t.Errorf("stdout should be empty, got: %q", stdout.String())
	}
}
