package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golemic/internal/agent"
	"golemic/internal/config"
	"golemic/internal/credentials"
	"golemic/internal/eventlog"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func loadCreds(t *testing.T, homeDir, project string) *credentials.Credentials {
	t.Helper()
	credDir := filepath.Join(homeDir, ".golemic", project)
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	credJSON := `{"dev_token":"ghp_dev_ci_test","reviewer_token":"ghp_rev_ci_test"}`
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(credJSON), 0600); err != nil {
		t.Fatal(err)
	}
	loader := credentials.NewLoader(homeDir)
	creds, err := loader.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	return creds
}

func ciTestRunner(t *testing.T, exec *fakeExecutor) (*Runner, string) {
	t.Helper()
	homeDir := t.TempDir()
	repoRoot := t.TempDir()
	project := "ci-test"
	creds := loadCreds(t, homeDir, project)

	r := New(exec, homeDir, repoRoot, 13)
	r.repoRoot = repoRoot
	r.project = project
	r.creds = creds
	r.runID = "issue-13-20240101T000000Z"
	r.branchName = "golemic/issue-13"
	r.cfg = &config.Config{
		VerifyCommand:    "go test",
		CITimeoutMinutes: 1,
		Models:           config.Models{Dev: "test-model"},
	}
	r.issue = &issueData{Number: 13, Title: "Test", Body: "Body"}

	var stderr bytes.Buffer
	r.SetStderr(&stderr)

	return r, homeDir
}

// makeEventLog creates a minimal event log with a run_started event.
func makeEventLog(t *testing.T, homeDir, project, runID string) string {
	t.Helper()
	path := filepath.Join(homeDir, ".golemic", project, "runs", runID, "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---------------------------------------------------------------------------
// Unit: evaluateChecks dispatch table (DT-001)
// ---------------------------------------------------------------------------

func TestEvaluateChecks_Green(t *testing.T) {
	checks := []ghCheck{
		{Name: "lint", State: "pass"},
		{Name: "test", State: "success"},
		{Name: "build", State: "pass"},
	}
	result, failed := evaluateChecks(checks)
	if result != "green" {
		t.Errorf("result: got %q, want %q", result, "green")
	}
	if len(failed) != 0 {
		t.Errorf("failed: got %v, want empty", failed)
	}
}

func TestEvaluateChecks_Red(t *testing.T) {
	checks := []ghCheck{
		{Name: "lint", State: "pass"},
		{Name: "test", State: "fail"},
		{Name: "build", State: "error"},
	}
	result, failed := evaluateChecks(checks)
	if result != "red" {
		t.Errorf("result: got %q, want %q", result, "red")
	}
	if len(failed) != 2 {
		t.Errorf("failed count: got %d, want 2: %v", len(failed), failed)
	}
}

func TestEvaluateChecks_Pending(t *testing.T) {
	checks := []ghCheck{
		{Name: "lint", State: "pass"},
		{Name: "test", State: "pending"},
	}
	result, failed := evaluateChecks(checks)
	if result != "pending" {
		t.Errorf("result: got %q, want %q", result, "pending")
	}
	if len(failed) != 0 {
		t.Errorf("expected no failed, got: %v", failed)
	}
}

func TestEvaluateChecks_RedBeforePending(t *testing.T) {
	// Red takes priority over pending.
	checks := []ghCheck{
		{Name: "test", State: "fail"},
		{Name: "build", State: "pending"},
	}
	result, failed := evaluateChecks(checks)
	if result != "red" {
		t.Errorf("result: got %q, want %q", result, "red")
	}
	if len(failed) == 0 {
		t.Error("expected at least one failed check")
	}
}

func TestEvaluateChecks_Skipping(t *testing.T) {
	// skipping is treated as pass-through (neutral).
	checks := []ghCheck{
		{Name: "lint", State: "pass"},
		{Name: "optional", State: "skipping"},
	}
	result, _ := evaluateChecks(checks)
	if result != "green" {
		t.Errorf("result: got %q, want %q (skipping counts as neutral)", result, "green")
	}
}

// ---------------------------------------------------------------------------
// Unit: queryPRChecks — fail-closed on gh error (AC-007)
// ---------------------------------------------------------------------------

func TestQueryPRChecks_FailClosed(t *testing.T) {
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "", fmt.Errorf("network error")
		},
	}
	r, _ := ciTestRunner(t, exec)

	checks, err := r.queryPRChecks(42)
	if err == nil {
		t.Error("expected error from gh failure, got nil")
	}
	if checks != nil {
		t.Errorf("expected nil checks on error, got: %v", checks)
	}
	if !strings.Contains(err.Error(), "CHECKS_QUERY_FAILED") {
		t.Errorf("error should contain CHECKS_QUERY_FAILED, got: %v", err)
	}
}

func TestQueryPRChecks_NoChecks_EmptyArray(t *testing.T) {
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "[]", nil
		},
	}
	r, _ := ciTestRunner(t, exec)

	checks, err := r.queryPRChecks(42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(checks) != 0 {
		t.Errorf("expected 0 checks, got: %v", checks)
	}
}

func TestQueryPRChecks_ParsesChecks(t *testing.T) {
	checkJSON := `[{"name":"lint","state":"pass"},{"name":"test","state":"fail"}]`
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return checkJSON, nil
		},
	}
	r, _ := ciTestRunner(t, exec)

	checks, err := r.queryPRChecks(42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(checks) != 2 {
		t.Fatalf("expected 2 checks, got %d: %v", len(checks), checks)
	}
}

// ---------------------------------------------------------------------------
// Unit: waitForChecks — polls every 10s, respects timeout (BR-002)
// ---------------------------------------------------------------------------

func TestWaitForChecks_NoChecks(t *testing.T) {
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "[]", nil
		},
	}
	r, _ := ciTestRunner(t, exec)

	result := r.waitForChecks(42, 15)
	if result.result != "no_checks" {
		t.Errorf("result: got %q, want %q", result.result, "no_checks")
	}
}

func TestWaitForChecks_Green(t *testing.T) {
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return `[{"name":"lint","state":"pass"}]`, nil
		},
	}
	r, _ := ciTestRunner(t, exec)

	result := r.waitForChecks(42, 15)
	if result.result != "green" {
		t.Errorf("result: got %q, want %q", result.result, "green")
	}
}

func TestWaitForChecks_ImmediateRed(t *testing.T) {
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return `[{"name":"test","state":"fail"}]`, nil
		},
	}
	r, _ := ciTestRunner(t, exec)

	result := r.waitForChecks(42, 15)
	if result.result != "red" {
		t.Errorf("result: got %q, want %q", result.result, "red")
	}
	if len(result.failedChecks) == 0 {
		t.Error("expected non-empty failedChecks")
	}
}

func TestWaitForChecks_QueryFailed(t *testing.T) {
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "", fmt.Errorf("auth error")
		},
	}
	r, _ := ciTestRunner(t, exec)

	result := r.waitForChecks(42, 15)
	if result.result != "" {
		t.Errorf("query failure should return empty result, got %q", result.result)
	}
}

// ---------------------------------------------------------------------------
// Unit: runCIGate — DT-001 dispatch
// ---------------------------------------------------------------------------

func TestRunCIGate_GreenProceedsToReviewer(t *testing.T) {
	// AC-001: green checks → empty outcome (proceed)
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" {
				return `[{"name":"ci","state":"pass"}]`, nil
			}
			return "", nil
		},
	}
	r, homeDir := ciTestRunner(t, exec)
	eventLogPath := makeEventLog(t, homeDir, "ci-test", r.runID)

	w, _ := eventlog.NewWriter(eventLogPath)
	defer w.Close() //nolint:errcheck

	outcome := r.runCIGate(w, eventLogPath, 42, filepath.Join(homeDir, ".golemic", "ci-test"), time.Minute)
	if outcome != "" {
		t.Errorf("green checks: expected empty outcome (proceed), got %q", outcome)
	}

	// Verify ci_wait_finished event was written.
	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var found bool
	for _, e := range events {
		if e.Type == eventlog.EventCIWaitFinished {
			found = true
			var payload struct {
				Result string `json:"result"`
				Round  int    `json:"round"`
			}
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("unmarshal ci_wait_finished payload: %v", err)
			}
			if payload.Result != "green" {
				t.Errorf("ci_wait_finished result: got %q, want %q", payload.Result, "green")
			}
		}
	}
	if !found {
		t.Error("ci_wait_finished event not written")
	}
}

func TestRunCIGate_NoChecksProceedsImmediately(t *testing.T) {
	// AC-002: no checks → immediate pass-through
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "[]", nil
		},
	}
	r, homeDir := ciTestRunner(t, exec)
	eventLogPath := makeEventLog(t, homeDir, "ci-test", r.runID)

	w, _ := eventlog.NewWriter(eventLogPath)
	defer w.Close() //nolint:errcheck

	outcome := r.runCIGate(w, eventLogPath, 42, filepath.Join(homeDir, ".golemic", "ci-test"), time.Minute)
	if outcome != "" {
		t.Errorf("no checks: expected empty outcome, got %q", outcome)
	}

	reader := eventlog.Reader{}
	events, _ := reader.Read(eventLogPath)
	var found bool
	for _, e := range events {
		if e.Type == eventlog.EventCIWaitFinished {
			found = true
			var payload struct{ Result string }
			_ = json.Unmarshal(e.Payload, &payload)
			if payload.Result != "no_checks" {
				t.Errorf("ci_wait_finished result: got %q, want %q", payload.Result, "no_checks")
			}
		}
	}
	if !found {
		t.Error("ci_wait_finished event not written")
	}
}

func TestRunCIGate_ExhaustedRetriesEscalates(t *testing.T) {
	// AC-004: retries exhausted → dev_failed.
	// Checks always fail; agent succeeds; push detected via alternating SHAs.
	exec := exhaustedRetriesExecutor()
	r, homeDir := ciTestRunner(t, exec)
	r.cfg.CITimeoutMinutes = 1
	installFakeGuidelines(t, r.repoRoot)
	r.SetRunAgentFn(func(_ context.Context, _ agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		return 0, agent.TranscriptPaths{}, nil
	})

	eventLogPath := makeEventLog(t, homeDir, "ci-test", r.runID)
	w, _ := eventlog.NewWriter(eventLogPath)
	defer w.Close() //nolint:errcheck

	outcome := r.runCIGate(w, eventLogPath, 42, filepath.Join(homeDir, ".golemic", "ci-test"), 5*time.Second)
	if outcome != outcomeDevFailed {
		t.Errorf("exhausted retries: expected %q, got %q", outcomeDevFailed, outcome)
	}
}

func exhaustedRetriesExecutor() *fakeExecutor {
	shaCall := 0
	exec := &fakeExecutor{}
	exec.runWithEnvFunc = func(_ map[string]string, name string, args ...string) (string, error) {
		if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "checks" {
			return `[{"name":"test","state":"fail"}]`, nil
		}
		return "", nil
	}
	exec.runFunc = func(name string, args ...string) (string, error) {
		if name != "git" || len(args) == 0 || args[0] != "ls-remote" {
			return "", nil
		}
		shaCall++
		if shaCall%2 == 0 {
			return "abc123\trefs/heads/golemic/issue-13\n", nil
		}
		return "def456\trefs/heads/golemic/issue-13\n", nil
	}
	return exec
}

func installFakeGuidelines(t *testing.T, repoRoot string) {
	t.Helper()
	dir := filepath.Join(repoRoot, ".golemic", "guidelines")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dev.md"), []byte("# Guidelines"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestRunCIGate_CheckQueryFailed_FailClosed(t *testing.T) {
	// AC-007: gh pr checks fails → dev_failed
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) > 0 && args[0] == "pr" && len(args) > 1 && args[1] == "checks" {
				return "", fmt.Errorf("network error")
			}
			// gh pr comment → success
			return "", nil
		},
	}
	r, homeDir := ciTestRunner(t, exec)
	eventLogPath := makeEventLog(t, homeDir, "ci-test", r.runID)

	w, _ := eventlog.NewWriter(eventLogPath)
	defer w.Close() //nolint:errcheck

	outcome := r.runCIGate(w, eventLogPath, 42, filepath.Join(homeDir, ".golemic", "ci-test"), time.Minute)
	if outcome != outcomeDevFailed {
		t.Errorf("query failure: expected %q, got %q", outcomeDevFailed, outcome)
	}
}

// ---------------------------------------------------------------------------
// Unit: truncate helper
// ---------------------------------------------------------------------------

func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		maxBytes int
		wantLen  int
	}{
		{"empty", "", 100, 0},
		{"fits", "hello", 100, 5},
		{"exact", "hello", 5, 5},
		{"zero max", "hello", 0, 0},
		{"truncates", "line1\nline2\nline3", 10, 5}, // trims to last newline
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.s, tt.maxBytes)
			if len(got) > tt.maxBytes {
				t.Errorf("truncate(%q, %d): got len %d > %d", tt.s, tt.maxBytes, len(got), tt.maxBytes)
			}
			if tt.wantLen >= 0 && len(got) != tt.wantLen {
				t.Errorf("truncate(%q, %d) = %q (len %d), want len %d", tt.s, tt.maxBytes, got, len(got), tt.wantLen)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Unit: ci_wait_finished event is written for timeout (AC-005)
// ---------------------------------------------------------------------------

func TestRunCIGate_TimeoutWritesEvent(t *testing.T) {
	// AC-005: timeout is treated like red.
	// Use a very short timeout (0 minutes) to trigger immediately.
	pendingJSON := `[{"name":"test","state":"pending"}]`
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) > 1 && args[1] == "checks" {
				return pendingJSON, nil
			}
			return "", nil
		},
	}
	r, homeDir := ciTestRunner(t, exec)
	r.cfg.CITimeoutMinutes = 0 // expired immediately

	eventLogPath := makeEventLog(t, homeDir, "ci-test", r.runID)
	w, _ := eventlog.NewWriter(eventLogPath)
	defer w.Close() //nolint:errcheck

	outcome := r.runCIGate(w, eventLogPath, 42, filepath.Join(homeDir, ".golemic", "ci-test"), time.Second)

	// Should result in dev_failed after exhausting retries (since timeout = red).
	// With CITimeoutMinutes=0 it times out immediately → timeout → retries → dev_failed.
	if outcome != outcomeDevFailed {
		t.Errorf("timeout: expected %q, got %q", outcomeDevFailed, outcome)
	}

	reader := eventlog.Reader{}
	events, _ := reader.Read(eventLogPath)
	var found bool
	for _, e := range events {
		if e.Type == eventlog.EventCIWaitFinished {
			found = true
			var payload struct{ Result string }
			_ = json.Unmarshal(e.Payload, &payload)
			if payload.Result != "timeout" {
				t.Errorf("ci_wait_finished result: got %q, want %q", payload.Result, "timeout")
			}
			break
		}
	}
	if !found {
		t.Error("ci_wait_finished event not written for timeout")
	}
}
