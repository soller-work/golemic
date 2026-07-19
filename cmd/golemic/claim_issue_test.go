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

// claimIssueFixture creates a temp homeDir and repoRoot with minimal config and credentials.
func claimIssueFixture(t *testing.T, project, devToken string) (homeDir, repoRoot string) {
	t.Helper()
	homeDir = t.TempDir()
	repoRoot = t.TempDir()

	cfgDir := filepath.Join(repoRoot, ".golemic")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfgJSON := fmt.Sprintf(`{"project":%q,"verify_command":"go test"}`, project)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(cfgJSON), 0644); err != nil {
		t.Fatal(err)
	}

	credsDir := filepath.Join(homeDir, ".golemic", project)
	if err := os.MkdirAll(credsDir, 0700); err != nil {
		t.Fatal(err)
	}
	credsJSON := fmt.Sprintf(`{"dev_token":%q,"reviewer_token":"ghp_reviewer_test"}`, devToken)
	if err := os.WriteFile(filepath.Join(credsDir, "credentials.json"), []byte(credsJSON), 0600); err != nil {
		t.Fatal(err)
	}

	return homeDir, repoRoot
}

// claimIssueRun runs runClaimIssue with a controlled environment and executor.
// envVars are merged with a set of required defaults unless explicitly set to "".
func claimIssueRun( //nolint:cyclop,gocognit
	t *testing.T,
	homeDir, repoRoot string,
	envOverrides map[string]string,
	ghResponses func(env map[string]string, name string, args []string) (string, error),
	extraArgs ...string,
) (int, string, string, string) { // code, stdout, stderr, eventLogPath
	t.Helper()

	// Set HOME so credentials.NewLoader resolves to our temp homeDir.
	origHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", homeDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Setenv("HOME", origHome) }()

	// Unset bot-token env vars so credentials file is authoritative.
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

	getenv := func(key string) string {
		return envDefaults[key]
	}

	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			// git rev-parse for repo root resolution.
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

	args := append([]string{"golemic", "claim-issue", "--number", "42"}, extraArgs...)
	var stdout, stderr bytes.Buffer
	code := runClaimIssue(args, &stdout, &stderr, getenv, exec)
	return code, stdout.String(), stderr.String(), eventLogPath
}

// issueViewJSON returns a minimal gh issue view JSON response.
func claimIssueViewJSON(labels []string, assignees []string) string {
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

const (
	testProject  = "testproject"
	testToken    = "ghp_dev_xxx"
	testDevLogin = "golemic-dev"
)

// userAPIResponse returns a minimal gh api user JSON response.
func userAPIResponse(login string) string {
	return fmt.Sprintf(`{"login":%q,"id":1}`, login)
}

// AC-001: Happy path — issue is claimed, event written, exit 0.
func TestClaimIssue_AC001_HappyPath(t *testing.T) { //nolint:gocognit,cyclop
	homeDir, repoRoot := claimIssueFixture(t, testProject, testToken)

	preView := claimIssueViewJSON([]string{"ready-for-agent"}, nil)
	postView := claimIssueViewJSON([]string{"in-progress"}, []string{testDevLogin})

	calls := 0
	var capturedGHToken string

	code, stdout, stderr, eventLogPath := claimIssueRun(t, homeDir, repoRoot, nil,
		func(env map[string]string, name string, args []string) (string, error) {
			if tok := env["GH_TOKEN"]; tok != "" {
				capturedGHToken = tok
			}
			calls++
			switch {
			case name == "git" && len(args) > 0 && args[0] == "rev-parse":
				return repoRoot + "\n", nil
			case name == "gh" && args[0] == "api" && args[1] == "user":
				return userAPIResponse(testDevLogin), nil
			case name == "gh" && args[0] == "issue" && args[1] == "view":
				// first view = pre-read, third call = post-verify
				if calls == 2 {
					return preView, nil
				}
				return postView, nil
			case name == "gh" && args[0] == "issue" && args[1] == "edit":
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		})

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "claimed issue #42") {
		t.Errorf("stdout should contain 'claimed issue #42', got: %q", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr should be empty, got: %q", stderr)
	}

	// Verify GH_TOKEN is the dev-bot token (not leaked in stdout/stderr).
	if capturedGHToken != testToken {
		t.Errorf("GH_TOKEN: got %q, want %q", capturedGHToken, testToken)
	}
	if strings.Contains(stdout, testToken) {
		t.Errorf("dev_token must not appear in stdout")
	}
	if strings.Contains(stderr, testToken) {
		t.Errorf("dev_token must not appear in stderr")
	}

	// Verify issue_claimed event was written.
	data, err := os.ReadFile(eventLogPath)
	if err != nil {
		t.Fatalf("event log not written: %v", err)
	}
	var ev map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(data), &ev); err != nil {
		t.Fatalf("event log JSON invalid: %v", err)
	}
	if ev["type"] != "issue_claimed" {
		t.Errorf("event type: got %v, want issue_claimed", ev["type"])
	}
	payload, _ := ev["payload"].(map[string]interface{})
	if payload["issue_number"] != float64(42) {
		t.Errorf("event payload issue_number: got %v, want 42", payload["issue_number"])
	}
	if payload["verify_result"] != "ok" {
		t.Errorf("event payload verify_result: got %v, want ok", payload["verify_result"])
	}
}

// AC-002: Idempotent — issue already owned, no edit, no event, exit 0.
func TestClaimIssue_AC002_Idempotent(t *testing.T) { //nolint:cyclop,gocognit
	homeDir, repoRoot := claimIssueFixture(t, testProject, testToken)

	preView := claimIssueViewJSON([]string{"in-progress"}, []string{testDevLogin})

	editCalled := false
	code, stdout, stderr, eventLogPath := claimIssueRun(t, homeDir, repoRoot, nil,
		func(_ map[string]string, name string, args []string) (string, error) {
			switch {
			case name == "git" && args[0] == "rev-parse":
				return repoRoot + "\n", nil
			case name == "gh" && args[0] == "api" && args[1] == "user":
				return userAPIResponse(testDevLogin), nil
			case name == "gh" && args[0] == "issue" && args[1] == "view":
				return preView, nil
			case name == "gh" && args[0] == "issue" && args[1] == "edit":
				editCalled = true
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		})

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "already claimed issue #42") {
		t.Errorf("stdout should contain 'already claimed issue #42', got: %q", stdout)
	}
	if editCalled {
		t.Error("edit should not be called on idempotent path")
	}
	if _, err := os.Stat(eventLogPath); !os.IsNotExist(err) {
		t.Error("event log should not be written on idempotent path")
	}
}

// AC-003: Race lost — post-verify shows foreign assignee, rollback issued, exit 3.
func TestClaimIssue_AC003_RaceLost(t *testing.T) { //nolint:gocognit,cyclop
	homeDir, repoRoot := claimIssueFixture(t, testProject, testToken)

	preView := claimIssueViewJSON([]string{"ready-for-agent"}, nil)
	postView := claimIssueViewJSON([]string{"in-progress"}, []string{"other-bot"})

	calls := 0
	rollbackCalled := false

	code, stdout, stderr, eventLogPath := claimIssueRun(t, homeDir, repoRoot, nil,
		func(_ map[string]string, name string, args []string) (string, error) {
			switch {
			case name == "git" && args[0] == "rev-parse":
				return repoRoot + "\n", nil
			case name == "gh" && args[0] == "api" && args[1] == "user":
				return userAPIResponse(testDevLogin), nil
			case name == "gh" && args[0] == "issue" && args[1] == "view":
				calls++
				if calls == 1 {
					return preView, nil
				}
				return postView, nil
			case name == "gh" && args[0] == "issue" && args[1] == "edit":
				calls++
				// detect rollback by checking for --add-label ready-for-agent
				for _, a := range args {
					if a == "ready-for-agent" {
						rollbackCalled = true
					}
				}
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		})

	if code != 3 {
		t.Fatalf("exit code: got %d, want 3; stdout: %s stderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "claim conflict on issue #42") {
		t.Errorf("stderr should contain 'claim conflict on issue #42', got: %q", stderr)
	}
	if !rollbackCalled {
		t.Error("rollback edit should have been called")
	}
	if _, err := os.Stat(eventLogPath); !os.IsNotExist(err) {
		t.Error("event log should not be written on race lost path")
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on race lost, got: %q", stdout)
	}
}

// AC-004: Not takeable — no ready-for-agent label, exit 4.
func TestClaimIssue_AC004_NotTakeable(t *testing.T) { //nolint:cyclop,gocognit
	homeDir, repoRoot := claimIssueFixture(t, testProject, testToken)

	preView := claimIssueViewJSON([]string{"enhancement"}, nil)
	editCalled := false

	code, stdout, stderr, eventLogPath := claimIssueRun(t, homeDir, repoRoot, nil,
		func(_ map[string]string, name string, args []string) (string, error) {
			switch {
			case name == "git" && args[0] == "rev-parse":
				return repoRoot + "\n", nil
			case name == "gh" && args[0] == "api" && args[1] == "user":
				return userAPIResponse(testDevLogin), nil
			case name == "gh" && args[0] == "issue" && args[1] == "view":
				return preView, nil
			case name == "gh" && args[0] == "issue" && args[1] == "edit":
				editCalled = true
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		})

	if code != 4 {
		t.Fatalf("exit code: got %d, want 4; stdout: %s stderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "issue #42 is not takeable") {
		t.Errorf("stderr should contain 'issue #42 is not takeable', got: %q", stderr)
	}
	if editCalled {
		t.Error("edit should not be called on not-takeable path")
	}
	if _, err := os.Stat(eventLogPath); !os.IsNotExist(err) {
		t.Error("event log should not be written on not-takeable path")
	}
	if stdout != "" {
		t.Errorf("stdout should be empty, got: %q", stdout)
	}
}

// AC-005: Missing env var — exit 1, no gh call.
func TestClaimIssue_AC005_MissingEnvVar(t *testing.T) {
	_, repoRoot := claimIssueFixture(t, testProject, testToken)

	ghCalled := false
	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
				return repoRoot + "\n", nil
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
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

	args := []string{"golemic", "claim-issue", "--number", "42"}
	var stdout, stderr bytes.Buffer
	code := runClaimIssue(args, &stdout, &stderr, getenv, exec)

	if code != 1 {
		t.Fatalf("exit code: got %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "missing required environment variable: GOLEMIC_RUN_ID") {
		t.Errorf("stderr should contain missing var message, got: %q", stderr.String())
	}
	if ghCalled {
		t.Error("gh should not be called when env var is missing")
	}
	if stdout.String() != "" {
		t.Errorf("stdout should be empty, got: %q", stdout.String())
	}
}
