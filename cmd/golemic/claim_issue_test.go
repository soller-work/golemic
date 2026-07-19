package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golemic/internal/eventlog"
)

// claimFixture creates temp homeDir and repoRoot with config and credentials.
func claimFixture(t *testing.T, project, devToken string) (homeDir, repoRoot string) {
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
	credsPath := filepath.Join(credsDir, "credentials.json")
	if err := os.WriteFile(credsPath, []byte(credsJSON), 0600); err != nil {
		t.Fatal(err)
	}

	origHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", homeDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("HOME", origHome) })

	for _, k := range []string{"GOLEMIC_DEV_TOKEN", "GOLEMIC_REVIEWER_TOKEN"} {
		orig := os.Getenv(k)
		_ = os.Unsetenv(k)
		t.Cleanup(func() { _ = os.Setenv(k, orig) })
	}

	return homeDir, repoRoot
}

// claimEnv returns a getenv function with the given run context.
func claimEnv(runID, eventLog, turnID string) func(string) string {
	m := map[string]string{
		"GOLEMIC_RUN_ID":    runID,
		"GOLEMIC_EVENT_LOG": eventLog,
		"GOLEMIC_TURN_ID":   turnID,
	}
	return func(k string) string { return m[k] }
}

// claimExec returns a fakeExecutor that handles infrastructure calls (git rev-parse)
// and routes gh calls to the provided handler.
func claimExec(repoRoot string, ghHandler func(env map[string]string, args ...string) (string, error)) fakeExecutor {
	return fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
				return repoRoot + "\n", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" {
				return ghHandler(env, args...)
			}
			if name == "git" {
				return repoRoot + "\n", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}
}

// AC-001: happy path — takeable issue is claimed exclusively.
func TestRunClaimIssue_HappyPath(t *testing.T) { //nolint:cyclop
	const (
		devToken = "ghp_dev_xxx"
		devLogin = "dev-bot"
		project  = "testproject"
		issueNum = 42
	)

	_, repoRoot := claimFixture(t, project, devToken)
	logFile := filepath.Join(t.TempDir(), "events.jsonl")

	callIdx := 0
	exec := claimExec(repoRoot, func(env map[string]string, args ...string) (string, error) {
		if env["GH_TOKEN"] != devToken {
			return "", fmt.Errorf("GH_TOKEN not injected correctly")
		}
		callIdx++
		switch callIdx {
		case 1: // gh api user
			return `{"login":"dev-bot"}`, nil
		case 2: // gh issue view (pre-read)
			return `{"labels":[{"name":"ready-for-agent"}],"assignees":[]}`, nil
		case 3: // gh issue edit
			return "", nil
		case 4: // gh issue view (post-verify)
			return `{"labels":[{"name":"in-progress"}],"assignees":[{"login":"dev-bot"}]}`, nil
		}
		return "", fmt.Errorf("unexpected gh call %d: %v", callIdx, args)
	})

	var stdout, stderr bytes.Buffer
	code := runClaimIssue(
		[]string{"golemic", "claim-issue", "--number", "42"},
		&stdout, &stderr,
		claimEnv("run-001", logFile, "0"),
		exec,
	)

	if code != 0 {
		t.Fatalf("exit code = %d; want 0; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "claimed issue #42 as dev-bot") {
		t.Errorf("stdout = %q; want 'claimed issue #42 as dev-bot'", stdout.String())
	}
	if strings.Contains(stdout.String(), devToken) || strings.Contains(stderr.String(), devToken) {
		t.Errorf("dev token leaked to output")
	}

	// Verify event was written.
	reader := eventlog.Reader{}
	events, err := reader.Read(logFile)
	if err != nil {
		t.Fatalf("failed to read event log: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event; got %d", len(events))
	}
	ev := events[0]
	if ev.Type != eventlog.EventIssueClaimed {
		t.Errorf("event type = %q; want %q", ev.Type, eventlog.EventIssueClaimed)
	}
	if ev.RunID != "run-001" {
		t.Errorf("event runID = %q; want 'run-001'", ev.RunID)
	}
	if ev.TurnID != 0 {
		t.Errorf("event turnID = %d; want 0", ev.TurnID)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("invalid payload JSON: %v", err)
	}
	if int(payload["issue_number"].(float64)) != issueNum {
		t.Errorf("payload issue_number = %v; want %d", payload["issue_number"], issueNum)
	}
	if payload["verify_result"] != "ok" {
		t.Errorf("payload verify_result = %v; want 'ok'", payload["verify_result"])
	}
}

// AC-002: idempotent re-claim — no edit, no event, exit 0.
func TestRunClaimIssue_Idempotent(t *testing.T) {
	const (
		devToken = "ghp_dev_xxx"
		project  = "testproject"
	)

	_, repoRoot := claimFixture(t, project, devToken)
	logFile := filepath.Join(t.TempDir(), "events.jsonl")

	callIdx := 0
	exec := claimExec(repoRoot, func(env map[string]string, args ...string) (string, error) {
		callIdx++
		switch callIdx {
		case 1: // gh api user
			return `{"login":"dev-bot"}`, nil
		case 2: // gh issue view (pre-read) — already claimed
			return `{"labels":[{"name":"in-progress"}],"assignees":[{"login":"dev-bot"}]}`, nil
		}
		return "", fmt.Errorf("unexpected gh call %d: %v", callIdx, args)
	})

	var stdout, stderr bytes.Buffer
	code := runClaimIssue(
		[]string{"golemic", "claim-issue", "--number", "42"},
		&stdout, &stderr,
		claimEnv("run-001", logFile, "0"),
		exec,
	)

	if code != 0 {
		t.Fatalf("exit code = %d; want 0; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "already claimed issue #42") {
		t.Errorf("stdout = %q; want 'already claimed issue #42'", stdout.String())
	}

	// No event should have been written.
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Errorf("event log should not exist for idempotent noop")
	}
	if callIdx != 2 {
		t.Errorf("expected 2 gh calls (user + pre-read); got %d", callIdx)
	}
}

// AC-003: race lost — rollback and exit 3.
func TestRunClaimIssue_RaceLost(t *testing.T) {
	const (
		devToken = "ghp_dev_xxx"
		project  = "testproject"
	)

	_, repoRoot := claimFixture(t, project, devToken)
	logFile := filepath.Join(t.TempDir(), "events.jsonl")

	callIdx := 0
	exec := claimExec(repoRoot, func(env map[string]string, args ...string) (string, error) {
		callIdx++
		switch callIdx {
		case 1: // gh api user
			return `{"login":"dev-bot"}`, nil
		case 2: // gh issue view (pre-read)
			return `{"labels":[{"name":"ready-for-agent"}],"assignees":[]}`, nil
		case 3: // gh issue edit
			return "", nil
		case 4: // gh issue view (post-verify) — another bot won
			return `{"labels":[{"name":"in-progress"}],"assignees":[{"login":"other-bot"}]}`, nil
		case 5: // rollback edit
			return "", nil
		}
		return "", fmt.Errorf("unexpected gh call %d: %v", callIdx, args)
	})

	var stdout, stderr bytes.Buffer
	code := runClaimIssue(
		[]string{"golemic", "claim-issue", "--number", "42"},
		&stdout, &stderr,
		claimEnv("run-001", logFile, "0"),
		exec,
	)

	if code != exitClaimRaceLost {
		t.Fatalf("exit code = %d; want %d; stderr: %s", code, exitClaimRaceLost, stderr.String())
	}
	if !strings.Contains(stderr.String(), "claim conflict on issue #42") {
		t.Errorf("stderr = %q; want 'claim conflict on issue #42'", stderr.String())
	}

	// No event written.
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Errorf("event log should not exist after race loss")
	}
}

// AC-004: non-takeable issue — exit 4.
func TestRunClaimIssue_NotTakeable(t *testing.T) {
	const (
		devToken = "ghp_dev_xxx"
		project  = "testproject"
	)

	_, repoRoot := claimFixture(t, project, devToken)
	logFile := filepath.Join(t.TempDir(), "events.jsonl")

	callIdx := 0
	exec := claimExec(repoRoot, func(env map[string]string, args ...string) (string, error) {
		callIdx++
		switch callIdx {
		case 1: // gh api user
			return `{"login":"dev-bot"}`, nil
		case 2: // gh issue view — no ready-for-agent label, not owned
			return `{"labels":[],"assignees":[]}`, nil
		}
		return "", fmt.Errorf("unexpected gh call %d: %v", callIdx, args)
	})

	var stdout, stderr bytes.Buffer
	code := runClaimIssue(
		[]string{"golemic", "claim-issue", "--number", "42"},
		&stdout, &stderr,
		claimEnv("run-001", logFile, "0"),
		exec,
	)

	if code != exitClaimNotTakeable {
		t.Fatalf("exit code = %d; want %d; stderr: %s", code, exitClaimNotTakeable, stderr.String())
	}
	if !strings.Contains(stderr.String(), "issue #42 is not takeable") {
		t.Errorf("stderr = %q; want 'issue #42 is not takeable'", stderr.String())
	}
	if callIdx != 2 {
		t.Errorf("expected 2 gh calls (no edit); got %d", callIdx)
	}
}

// AC-005: missing env var — exit 1, no gh call.
func TestRunClaimIssue_MissingEnvVar(t *testing.T) {
	const (
		devToken = "ghp_dev_xxx"
		project  = "testproject"
	)

	_, repoRoot := claimFixture(t, project, devToken)

	ghCalled := false
	exec := claimExec(repoRoot, func(env map[string]string, args ...string) (string, error) {
		ghCalled = true
		return "", nil
	})

	var stdout, stderr bytes.Buffer
	code := runClaimIssue(
		[]string{"golemic", "claim-issue", "--number", "42"},
		&stdout, &stderr,
		claimEnv("", "", ""), // GOLEMIC_RUN_ID missing
		exec,
	)

	if code != 1 {
		t.Fatalf("exit code = %d; want 1", code)
	}
	if !strings.Contains(stderr.String(), "GOLEMIC_RUN_ID") {
		t.Errorf("stderr = %q; want mention of GOLEMIC_RUN_ID", stderr.String())
	}
	if ghCalled {
		t.Errorf("gh should not be called when env var is missing")
	}
}

func TestRunClaimIssue_MissingNumber(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runClaimIssue(
		[]string{"golemic", "claim-issue"},
		&stdout, &stderr,
		claimEnv("run-001", "/tmp/events.jsonl", "0"),
		fakeExecutor{},
	)
	if code != 1 {
		t.Fatalf("exit code = %d; want 1", code)
	}
}
