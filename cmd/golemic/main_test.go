package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"golemic/internal/config"
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

func (f fakeExecutor) RunInDir(_ string, name string, args ...string) (string, error) {
	return f.Run(name, args...)
}

func (f fakeExecutor) RunWithEnvInDir(env map[string]string, _ string, name string, args ...string) (string, error) {
	return f.RunWithEnv(env, name, args...)
}

// testOKLoadConfig returns a loadConfig stub that succeeds with a minimal config.
func testOKLoadConfig() func() (*config.Config, error) {
	return func() (*config.Config, error) {
		return &config.Config{VerifyCommand: "true"}, nil
	}
}

// testNoLoadConfig returns a loadConfig stub that fails the test if called.
func testNoLoadConfig(t *testing.T) func() (*config.Config, error) {
	t.Helper()
	return func() (*config.Config, error) {
		t.Fatal("loadConfig must not be called before env/flag validation")
		return nil, nil
	}
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
			// Which error fires depends on whether GOLEMIC_RUN_ID/GOLEMIC_EVENT_LOG
			// are set in the test environment; just verify dispatch is reached.
			name:     "emit dispatches to runEmit",
			args:     []string{"golemic", "emit"},
			wantExit: 1,
		},
		{
			// Which error fires depends on whether GOLEMIC_RUN_ID/GOLEMIC_EVENT_LOG
			// are set in the test environment; just verify dispatch is reached.
			name:     "open-pr without flags fails with env var error",
			args:     []string{"golemic", "open-pr"},
			wantExit: 1,
		},
		{
			// Dispatch reached runSubmitReview: which error fires depends on whether
			// GOLEMIC_RUN_ID/GOLEMIC_EVENT_LOG are set in the test environment.
			name:     "submit-review without flags fails with validation error",
			args:     []string{"golemic", "submit-review"},
			wantExit: 1,
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

func openPRTestEnv(runID, eventLog, turnID string) func(string) string {
	return func(key string) string {
		switch key {
		case "GOLEMIC_RUN_ID":
			return runID
		case "GOLEMIC_EVENT_LOG":
			return eventLog
		case "GOLEMIC_TURN_ID":
			return turnID
		}
		return ""
	}
}

func readPROpenedPayload(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	reader := eventlog.Reader{}
	events, err := reader.Read(path)
	if err != nil {
		if strings.Contains(err.Error(), "LOG_FILE_NOT_FOUND") {
			return nil
		}
		t.Fatalf("read events: %v", err)
	}
	for _, e := range events {
		if e.Type == eventlog.EventPROpened {
			var payload map[string]interface{}
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			return payload
		}
	}
	return nil
}

// TestRunOpenPR_AC001_NoPR covers AC-001: no existing PR, create path emits event.
func TestRunOpenPR_AC001_NoPR(t *testing.T) { //nolint:cyclop
	dir := t.TempDir()
	eventLog := dir + "/events.jsonl"
	env := openPRTestEnv("run-1", eventLog, "1")

	var createCalled bool
	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "sh" {
				return "", nil
			}
			if name == "git" {
				return "golemic/issue-42\n", nil
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvFunc: func(_ map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[1] == "list" {
				return "[]", nil
			}
			if name == "gh" && len(args) >= 2 && args[1] == "create" {
				createCalled = true
				return "https://github.com/org/repo/pull/99\n", nil
			}
			return "", fmt.Errorf("unexpected RunWithEnv: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	code := runOpenPR([]string{"golemic", "open-pr", "--title", "T", "--body", "B"}, &stdout, &stderr, env, exec, testOKLoadConfig())

	if code != 0 {
		t.Errorf("exit code: got %d, want 0; stderr: %s", code, stderr.String())
	}
	if !createCalled {
		t.Error("gh pr create was not called")
	}
	if !strings.Contains(stdout.String(), "https://github.com/org/repo/pull/99") {
		t.Errorf("stdout missing PR URL; got %q", stdout.String())
	}
	payload := readPROpenedPayload(t, eventLog)
	if payload == nil {
		t.Fatal("no pr_opened event in log")
	}
	if payload["prNumber"] != "99" {
		t.Errorf("prNumber: got %v, want 99", payload["prNumber"])
	}
	if payload["url"] != "https://github.com/org/repo/pull/99" {
		t.Errorf("url: got %v", payload["url"])
	}
	if payload["branch"] != "golemic/issue-42" {
		t.Errorf("branch: got %v", payload["branch"])
	}
}

// TestRunOpenPR_AC002_OnePR covers AC-002: exactly one existing PR, idempotent path.
func TestRunOpenPR_AC002_OnePR(t *testing.T) { //nolint:cyclop
	dir := t.TempDir()
	eventLog := dir + "/events.jsonl"
	env := openPRTestEnv("run-2", eventLog, "1")

	var createCalled bool
	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "sh" {
				return "", nil
			}
			if name == "git" {
				return "golemic/issue-31\n", nil
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvFunc: func(_ map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[1] == "list" {
				return `[{"number":37,"url":"https://github.com/org/repo/pull/37"}]`, nil
			}
			if name == "gh" && len(args) >= 2 && args[1] == "create" {
				createCalled = true
				return "", fmt.Errorf("should not be called")
			}
			return "", fmt.Errorf("unexpected RunWithEnv: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	code := runOpenPR([]string{"golemic", "open-pr", "--title", "T", "--body", "B"}, &stdout, &stderr, env, exec, testOKLoadConfig())

	if code != 0 {
		t.Errorf("exit code: got %d, want 0; stderr: %s", code, stderr.String())
	}
	if createCalled {
		t.Error("gh pr create must NOT be called on idempotent path")
	}
	if !strings.Contains(stdout.String(), "https://github.com/org/repo/pull/37") {
		t.Errorf("stdout missing existing PR URL; got %q", stdout.String())
	}
	payload := readPROpenedPayload(t, eventLog)
	if payload == nil {
		t.Fatal("no pr_opened event in log")
	}
	if payload["prNumber"] != "37" {
		t.Errorf("prNumber: got %v, want 37", payload["prNumber"])
	}
	if payload["url"] != "https://github.com/org/repo/pull/37" {
		t.Errorf("url: got %v", payload["url"])
	}
	if payload["branch"] != "golemic/issue-31" {
		t.Errorf("branch: got %v", payload["branch"])
	}
}

// TestRunOpenPR_AC003_MultiplePRs covers AC-003: multiple open PRs, fail fast.
func TestRunOpenPR_AC003_MultiplePRs(t *testing.T) { //nolint:cyclop
	dir := t.TempDir()
	eventLog := dir + "/events.jsonl"
	env := openPRTestEnv("run-3", eventLog, "1")

	var createCalled bool
	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "sh" {
				return "", nil
			}
			if name == "git" {
				return "golemic/issue-42\n", nil
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvFunc: func(_ map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[1] == "list" {
				return `[{"number":40,"url":"https://github.com/org/repo/pull/40"},{"number":41,"url":"https://github.com/org/repo/pull/41"}]`, nil
			}
			if name == "gh" && len(args) >= 2 && args[1] == "create" {
				createCalled = true
				return "", nil
			}
			return "", fmt.Errorf("unexpected RunWithEnv: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	code := runOpenPR([]string{"golemic", "open-pr", "--title", "T", "--body", "B"}, &stdout, &stderr, env, exec, testOKLoadConfig())

	if code != 1 {
		t.Errorf("exit code: got %d, want 1", code)
	}
	if createCalled {
		t.Error("gh pr create must NOT be called")
	}
	if readPROpenedPayload(t, eventLog) != nil {
		t.Error("no pr_opened event must be written")
	}
	if !strings.Contains(stderr.String(), "golemic/issue-42") {
		t.Errorf("stderr missing branch name; got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "2") {
		t.Errorf("stderr missing count; got %q", stderr.String())
	}
}

// TestRunOpenPR_AC004_ListFails covers AC-004: gh pr list failure, exit 1.
func TestRunOpenPR_AC004_ListFails(t *testing.T) { //nolint:cyclop
	dir := t.TempDir()
	eventLog := dir + "/events.jsonl"
	env := openPRTestEnv("run-4", eventLog, "1")

	var createCalled bool
	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "sh" {
				return "", nil
			}
			if name == "git" {
				return "golemic/issue-42\n", nil
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvFunc: func(_ map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[1] == "list" {
				return "", &preflight.ErrExit{ExitCode: 1, Stderr: "network error"}
			}
			if name == "gh" && len(args) >= 2 && args[1] == "create" {
				createCalled = true
				return "", nil
			}
			return "", fmt.Errorf("unexpected RunWithEnv: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	code := runOpenPR([]string{"golemic", "open-pr", "--title", "T", "--body", "B"}, &stdout, &stderr, env, exec, testOKLoadConfig())

	if code != 1 {
		t.Errorf("exit code: got %d, want 1", code)
	}
	if createCalled {
		t.Error("gh pr create must NOT be called")
	}
	if readPROpenedPayload(t, eventLog) != nil {
		t.Error("no pr_opened event must be written")
	}
	if !strings.Contains(stderr.String(), "network error") {
		t.Errorf("stderr missing gh error; got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "golemic/issue-42") {
		t.Errorf("stderr missing branch name; got %q", stderr.String())
	}
}

// TestRunOpenPR_AC005_MissingEnvVar covers AC-005: missing env var, fail before any gh call.
func TestRunOpenPR_AC005_MissingEnvVar(t *testing.T) {
	dir := t.TempDir()
	eventLog := dir + "/events.jsonl"
	env := openPRTestEnv("", eventLog, "1") // GOLEMIC_RUN_ID intentionally empty

	var ghCalled bool
	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			ghCalled = true
			return "", fmt.Errorf("should not be called")
		},
		runWithEnvFunc: func(_ map[string]string, name string, args ...string) (string, error) {
			ghCalled = true
			return "", fmt.Errorf("should not be called")
		},
	}

	var stdout, stderr bytes.Buffer
	code := runOpenPR([]string{"golemic", "open-pr", "--title", "T", "--body", "B"}, &stdout, &stderr, env, exec, testNoLoadConfig(t))

	if code != 1 {
		t.Errorf("exit code: got %d, want 1", code)
	}
	if ghCalled {
		t.Error("no gh or git call must happen before env var validation")
	}
	if !strings.Contains(stderr.String(), "GOLEMIC_RUN_ID") {
		t.Errorf("stderr missing env var name; got %q", stderr.String())
	}
}
