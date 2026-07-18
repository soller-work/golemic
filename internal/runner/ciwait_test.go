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
	"golemic/internal/eventlog"
	"golemic/internal/preflight"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// ghChecksJSON builds a JSON array of gh check items for use in fake executors.
func ghChecksJSON(items []ghCheckItem) string {
	b, _ := json.Marshal(items)
	return string(b)
}

// ghArgsMatch checks if gh args match the given subcommand pair.
func ghArgsMatch(args []string, sub1, sub2 string) bool {
	return len(args) >= 2 && args[0] == sub1 && args[1] == sub2
}

// dispatchCIWaitGh handles gh calls for ciWaitExecutor.
func dispatchCIWaitGh(args []string, checksJSON string, checksErr error, commentCalls *[]string) (string, error) {
	switch {
	case ghArgsMatch(args, "pr", "checks"):
		return checksJSON, checksErr
	case ghArgsMatch(args, "pr", "comment"):
		if commentCalls != nil {
			*commentCalls = append(*commentCalls, extractBodyArg(args))
		}
		return "", nil
	case ghArgsMatch(args, "run", "view"):
		return "fake log output", nil
	default:
		return "", fmt.Errorf("not mocked: gh %v", args)
	}
}

// ciWaitExecutor builds a fakeExecutor whose gh pr checks call returns the given JSON.
func ciWaitExecutor(checksJSON string, checksErr error, commentCalls *[]string) *fakeExecutor {
	return &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" {
				return handleGitCmd(args)
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name != "gh" {
				return "", fmt.Errorf("not mocked: %s %v", name, args)
			}
			return dispatchCIWaitGh(args, checksJSON, checksErr, commentCalls)
		},
	}
}

// buildCIGateRunner creates a runner for runCIGate unit tests.
func buildCIGateRunner(t *testing.T, exec *fakeExecutor) (*Runner, string, *bytes.Buffer) {
	t.Helper()
	homeDir, repoRoot, project := setupRunnerTest(t)
	creds := loadTestCreds(t, homeDir, project)

	// Write guidelines so RenderDevCIRetry can read them
	guidelinesDir := filepath.Join(repoRoot, ".golemic", "guidelines")
	if err := os.MkdirAll(guidelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guidelinesDir, "dev.md"), []byte("# Guidelines"), 0644); err != nil {
		t.Fatal(err)
	}

	r := New(exec, homeDir, repoRoot, 42)
	r.repoRoot = repoRoot
	r.project = project
	r.runID = "issue-42-ci-gate"
	r.branchName = "golemic/issue-42"
	r.creds = creds
	r.cfg = &config.Config{
		VerifyCommand:    "go test",
		CITimeoutMinutes: 15,
		TimeoutMinutes:   30,
		Models:           config.Models{Dev: "test/dev"},
	}
	r.issue = &issueData{Number: 42, Title: "T", Body: "B"}
	r.SetCIPollInterval(1 * time.Millisecond)
	r.SetCITimeout(5 * time.Millisecond) // fast timeout for tests

	var stderr bytes.Buffer
	r.SetStderr(&stderr)

	logPath := filepath.Join(homeDir, ".golemic", project, "runs", "issue-42-ci-gate", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		t.Fatal(err)
	}
	// seed log with run_started
	w, _ := eventlog.NewWriter(logPath)
	payload, _ := json.Marshal(map[string]interface{}{"issue": 42, "runId": "issue-42-ci-gate"})
	_ = w.Write(eventlog.Event{Type: eventlog.EventRunStarted, Ts: time.Now().Format(time.RFC3339), RunID: "issue-42-ci-gate", Payload: payload})
	w.Close() //nolint:errcheck

	return r, logPath, &stderr
}

// seqResponse returns responses in sequence; repeats last when exhausted.
func seqResponse(resps []string, idx *int) string {
	if *idx < len(resps) {
		r := resps[*idx]
		(*idx)++
		return r
	}
	if len(resps) > 0 {
		return resps[len(resps)-1]
	}
	return ""
}

// dispatchCIGateGh handles gh calls for ciGateExecutor.
func dispatchCIGateGh(args []string, checksResponses []string, checksIdx *int, commentCalls *[]string) (string, error) {
	switch {
	case ghArgsMatch(args, "pr", "checks"):
		return seqResponse(checksResponses, checksIdx), nil
	case ghArgsMatch(args, "pr", "comment"):
		if commentCalls != nil {
			*commentCalls = append(*commentCalls, extractBodyArg(args))
		}
		return "", nil
	case ghArgsMatch(args, "run", "view"):
		return "log output", nil
	default:
		return "", fmt.Errorf("not mocked: gh %v", args)
	}
}

// dispatchCIGateGit handles git calls for ciGateExecutor.
func dispatchCIGateGit(args []string, lsRemoteResponses []string, lsRemoteIdx *int) (string, error) {
	sub := ""
	if len(args) >= 3 && args[0] == "-C" {
		sub = args[2]
	} else if len(args) >= 1 {
		sub = args[0]
	}
	if sub == "ls-remote" {
		if resp := seqResponse(lsRemoteResponses, lsRemoteIdx); resp != "" {
			return resp, nil
		}
		return "abc123\trefs/heads/golemic/issue-42\n", nil
	}
	return handleGitCmd(args)
}

// ciGateExecutor creates an executor that answers gh pr checks with the given
// JSON responses in sequence. After exhausting the list it returns the last one.
// Also handles ls-remote for push detection and gh pr comment.
func ciGateExecutor(checksResponses []string, lsRemoteResponses []string, commentCalls *[]string) *fakeExecutor {
	checksIdx := 0
	lsRemoteIdx := 0
	return &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" {
				return dispatchCIGateGit(args, lsRemoteResponses, &lsRemoteIdx)
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name != "gh" {
				return "", fmt.Errorf("not mocked: %s %v", name, args)
			}
			return dispatchCIGateGh(args, checksResponses, &checksIdx, commentCalls)
		},
	}
}

// readCIWaitEvents returns all ci_wait_finished events from the log.
func readCIWaitEvents(t *testing.T, logPath string) []eventlog.Event {
	t.Helper()
	reader := eventlog.Reader{}
	events, err := reader.Read(logPath)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var result []eventlog.Event
	for _, ev := range events {
		if ev.Type == eventlog.EventCIWaitFinished {
			result = append(result, ev)
		}
	}
	return result
}

// ciWaitResult extracts the result field from a ci_wait_finished event payload.
func ciWaitResult(t *testing.T, ev eventlog.Event) string {
	t.Helper()
	var p struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatalf("unmarshal ci_wait_finished payload: %v", err)
	}
	return p.Result
}

// ---------------------------------------------------------------------------
// queryCIChecks unit tests
// ---------------------------------------------------------------------------

// AC-002: no checks configured → no_checks
func TestQueryCIChecks_NoChecks_AC002(t *testing.T) {
	r, logPath, _ := buildCIGateRunner(t, ciWaitExecutor("[]", nil, nil))
	_ = logPath

	result, failed, err := r.queryCIChecks(99)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "no_checks" {
		t.Errorf("result: got %q, want %q", result, "no_checks")
	}
	if len(failed) != 0 {
		t.Errorf("failed items: got %d, want 0", len(failed))
	}
}

// AC-001: all-pass → green
func TestQueryCIChecks_AllPassed_AC001(t *testing.T) {
	checks := ghChecksJSON([]ghCheckItem{
		{Name: "build", Bucket: "pass"},
		{Name: "lint", Bucket: "skipping"},
	})
	r, _, _ := buildCIGateRunner(t, ciWaitExecutor(checks, nil, nil))

	result, _, err := r.queryCIChecks(99)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "green" {
		t.Errorf("result: got %q, want %q", result, "green")
	}
}

// AC-003, AC-005: any fail → red with failed items
func TestQueryCIChecks_HasFailed_AC003(t *testing.T) {
	checks := ghChecksJSON([]ghCheckItem{
		{Name: "build", Bucket: "pass"},
		{Name: "test", Bucket: "fail", Link: "https://github.com/o/r/actions/runs/12345/jobs/1"},
	})
	r, _, _ := buildCIGateRunner(t, ciWaitExecutor(checks, nil, nil))

	result, failed, err := r.queryCIChecks(99)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "red" {
		t.Errorf("result: got %q, want %q", result, "red")
	}
	if len(failed) != 1 || failed[0].Name != "test" {
		t.Errorf("failed items: got %v, want [{test ...}]", failed)
	}
}

// still pending → "pending"
func TestQueryCIChecks_StillPending(t *testing.T) {
	checks := ghChecksJSON([]ghCheckItem{
		{Name: "build", Bucket: "pass"},
		{Name: "test", Bucket: "pending"},
	})
	r, _, _ := buildCIGateRunner(t, ciWaitExecutor(checks, nil, nil))

	result, _, err := r.queryCIChecks(99)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "pending" {
		t.Errorf("result: got %q, want %q", result, "pending")
	}
}

// AC-002 (real-error shape): gh exits non-zero with "no checks reported" → no_checks, not CHECKS_QUERY_FAILED.
// Reproduces the actual gh CLI behavior for a PR on a branch with no CI checks.
func TestQueryCIChecks_NoChecksRealGhError_AC002(t *testing.T) {
	stderr := "no checks reported on the 'golemic/issue-42' branch"
	ghErr := &preflight.ErrExit{ExitCode: 1, Stderr: stderr}
	r, _, _ := buildCIGateRunner(t, ciWaitExecutor("", ghErr, nil))

	result, failed, err := r.queryCIChecks(99)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "no_checks" {
		t.Errorf("result: got %q, want %q (gh 'no checks reported' must map to no_checks, not CHECKS_QUERY_FAILED)", result, "no_checks")
	}
	if len(failed) != 0 {
		t.Errorf("failed items: got %d, want 0", len(failed))
	}
}

// AC-007: gh error → CHECKS_QUERY_FAILED
func TestQueryCIChecks_GhError_AC007(t *testing.T) {
	r, _, _ := buildCIGateRunner(t, ciWaitExecutor("", fmt.Errorf("network error"), nil))

	_, _, err := r.queryCIChecks(99)
	if err == nil {
		t.Fatal("expected CHECKS_QUERY_FAILED error, got nil")
	}
	if !strings.Contains(err.Error(), "CHECKS_QUERY_FAILED") {
		t.Errorf("error should contain CHECKS_QUERY_FAILED, got: %v", err)
	}
}

// TestQueryCIChecks_FailClosed_AC007 proves the no-checks detector is fail-closed:
// only ErrExit{ExitCode:1} with the exact gh message maps to no_checks;
// any other error shape must return CHECKS_QUERY_FAILED.
func TestQueryCIChecks_FailClosed_AC007(t *testing.T) {
	noChecksMsg := "no checks reported on the 'x' branch"
	cases := []struct {
		name  string
		ghErr error
	}{
		{
			name:  "ErrExit_401_bad_credentials",
			ghErr: &preflight.ErrExit{ExitCode: 1, Stderr: "HTTP 401: Bad credentials"},
		},
		{
			// Right message, wrong exit code — must NOT be treated as no_checks.
			name:  "ErrExit_exitcode2_no_checks_message",
			ghErr: &preflight.ErrExit{ExitCode: 2, Stderr: noChecksMsg},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			r, _, _ := buildCIGateRunner(t, ciWaitExecutor("", tc.ghErr, nil))
			result, _, err := r.queryCIChecks(99)
			if err == nil {
				t.Fatalf("want CHECKS_QUERY_FAILED error, got nil (result=%q)", result)
			}
			if !strings.Contains(err.Error(), "CHECKS_QUERY_FAILED") {
				t.Errorf("error should contain CHECKS_QUERY_FAILED, got: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// pollCIChecks
// ---------------------------------------------------------------------------

// AC-001: immediate green → no wait
func TestPollCIChecks_ImmediateGreen_AC001(t *testing.T) {
	checks := ghChecksJSON([]ghCheckItem{{Name: "build", Bucket: "pass"}})
	r, _, _ := buildCIGateRunner(t, ciWaitExecutor(checks, nil, nil))

	result, _, err := r.pollCIChecks(99, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "green" {
		t.Errorf("result: got %q, want %q", result, "green")
	}
}

// AC-005: pending checks time out
func TestPollCIChecks_TimeoutWhilePending_AC005(t *testing.T) {
	checks := ghChecksJSON([]ghCheckItem{{Name: "build", Bucket: "pending"}})
	r, _, _ := buildCIGateRunner(t, ciWaitExecutor(checks, nil, nil))
	r.SetCIPollInterval(1 * time.Millisecond)

	result, _, err := r.pollCIChecks(99, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "timeout" {
		t.Errorf("result: got %q, want %q", result, "timeout")
	}
}

// AC-002: no checks → immediate pass
func TestPollCIChecks_NoChecksPassThrough_AC002(t *testing.T) {
	r, _, _ := buildCIGateRunner(t, ciWaitExecutor("[]", nil, nil))

	result, _, err := r.pollCIChecks(99, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "no_checks" {
		t.Errorf("result: got %q, want %q", result, "no_checks")
	}
}

// ---------------------------------------------------------------------------
// writeCIWaitFinished
// ---------------------------------------------------------------------------

func TestWriteCIWaitFinished_WritesCorrectEvent(t *testing.T) {
	r, logPath, _ := buildCIGateRunner(t, &fakeExecutor{})
	r.writeCIWaitFinished(logPath, "green", 0)

	ciEvents := readCIWaitEvents(t, logPath)
	if len(ciEvents) != 1 {
		t.Fatalf("expected 1 ci_wait_finished event, got %d", len(ciEvents))
	}
	if got := ciWaitResult(t, ciEvents[0]); got != "green" {
		t.Errorf("result: got %q, want %q", got, "green")
	}
}

// ---------------------------------------------------------------------------
// runCIGate integration-level tests
// ---------------------------------------------------------------------------

// AC-001: green checks release the reviewer
func TestRunCIGate_GreenPassThrough_AC001(t *testing.T) {
	greenChecks := ghChecksJSON([]ghCheckItem{{Name: "build", Bucket: "pass"}})
	exec := ciGateExecutor([]string{greenChecks}, nil, nil)
	r, logPath, _ := buildCIGateRunner(t, exec)

	outcome := r.runCIGate(99, logPath, 5*time.Second)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}

	ciEvents := readCIWaitEvents(t, logPath)
	if len(ciEvents) == 0 {
		t.Fatal("ci_wait_finished event not written")
	}
	if got := ciWaitResult(t, ciEvents[0]); got != "green" {
		t.Errorf("ci_wait_finished result: got %q, want %q", got, "green")
	}
}

// AC-002: no checks → immediate pass-through
func TestRunCIGate_NoChecksPassThrough_AC002(t *testing.T) {
	exec := ciGateExecutor([]string{"[]"}, nil, nil)
	r, logPath, _ := buildCIGateRunner(t, exec)

	outcome := r.runCIGate(99, logPath, 5*time.Second)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}

	ciEvents := readCIWaitEvents(t, logPath)
	if len(ciEvents) == 0 {
		t.Fatal("ci_wait_finished event not written")
	}
	if got := ciWaitResult(t, ciEvents[0]); got != "no_checks" {
		t.Errorf("ci_wait_finished result: got %q, want %q", got, "no_checks")
	}
}

// AC-003: red build triggers dev retry that heals the PR
func TestRunCIGate_RedThenGreen_AC003(t *testing.T) {
	redChecks := ghChecksJSON([]ghCheckItem{{Name: "test", Bucket: "fail"}})
	greenChecks := ghChecksJSON([]ghCheckItem{{Name: "test", Bucket: "pass"}})

	lsRemoteResps := []string{
		"sha1abc\trefs/heads/golemic/issue-42\n",
		"sha2def\trefs/heads/golemic/issue-42\n",
	}
	var commentCalls []string
	exec := ciGateExecutor([]string{redChecks, greenChecks}, lsRemoteResps, &commentCalls)
	r, logPath, _ := buildCIGateRunner(t, exec)

	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		return 0, agent.TranscriptPaths{}, nil
	})

	outcome := r.runCIGate(99, logPath, 5*time.Second)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
	if len(commentCalls) != 0 {
		t.Errorf("no escalation comment expected, got %d: %v", len(commentCalls), commentCalls)
	}

	ciEvents := readCIWaitEvents(t, logPath)
	if len(ciEvents) != 2 {
		t.Fatalf("expected 2 ci_wait_finished events, got %d", len(ciEvents))
	}
	if got := ciWaitResult(t, ciEvents[0]); got != "red" {
		t.Errorf("first ci_wait_finished result: got %q, want %q", got, "red")
	}
	if got := ciWaitResult(t, ciEvents[1]); got != "green" {
		t.Errorf("second ci_wait_finished result: got %q, want %q", got, "green")
	}
}

// AC-004: retries exhausted → escalate with PR comment mentioning 3 attempts
func TestRunCIGate_ExhaustedRetries_AC004(t *testing.T) {
	redChecks := ghChecksJSON([]ghCheckItem{{Name: "test", Bucket: "fail"}})
	lsRemoteResps := []string{
		"sha1\trefs/heads/golemic/issue-42\n",
		"sha2\trefs/heads/golemic/issue-42\n",
		"sha3\trefs/heads/golemic/issue-42\n",
		"sha4\trefs/heads/golemic/issue-42\n",
	}
	var commentCalls []string
	exec := ciGateExecutor([]string{redChecks, redChecks, redChecks}, lsRemoteResps, &commentCalls)
	r, logPath, _ := buildCIGateRunner(t, exec)

	devCallCount := 0
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		devCallCount++
		return 0, agent.TranscriptPaths{}, nil
	})

	outcome := r.runCIGate(99, logPath, 5*time.Second)
	if outcome != outcomeDevFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeDevFailed)
	}
	if devCallCount != 2 {
		t.Errorf("expected 2 dev retry calls, got %d", devCallCount)
	}
	if len(commentCalls) == 0 {
		t.Error("expected escalation comment, got none")
	}
	// Comment should mention "3" (1 initial + 2 retries)
	if !strings.Contains(commentCalls[len(commentCalls)-1], "3") {
		t.Errorf("escalation comment should mention 3 attempts, got: %s", commentCalls[len(commentCalls)-1])
	}
}

// AC-005: CI timeout is treated as red → triggers retry
func TestRunCIGate_TimeoutTreatedAsRed_AC005(t *testing.T) {
	pendingChecks := ghChecksJSON([]ghCheckItem{{Name: "test", Bucket: "pending"}})
	greenChecks := ghChecksJSON([]ghCheckItem{{Name: "test", Bucket: "pass"}})

	lsRemoteResps := []string{
		"sha1\trefs/heads/golemic/issue-42\n",
		"sha2\trefs/heads/golemic/issue-42\n",
	}
	var commentCalls []string
	exec := ciGateExecutor([]string{pendingChecks, greenChecks}, lsRemoteResps, &commentCalls)
	r, logPath, _ := buildCIGateRunner(t, exec)
	// Poll interval larger than CI timeout so deadline fires before ticker
	r.SetCIPollInterval(200 * time.Millisecond)
	r.SetCITimeout(1 * time.Millisecond)

	devCalled := false
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		devCalled = true
		return 0, agent.TranscriptPaths{}, nil
	})

	outcome := r.runCIGate(99, logPath, 5*time.Second)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
	if !devCalled {
		t.Error("expected dev CI retry to be called after timeout")
	}
	if len(commentCalls) != 0 {
		t.Errorf("no escalation comment expected, got %d", len(commentCalls))
	}

	ciEvents := readCIWaitEvents(t, logPath)
	if len(ciEvents) == 0 {
		t.Fatal("ci_wait_finished event not written")
	}
	if got := ciWaitResult(t, ciEvents[0]); got != "timeout" {
		t.Errorf("first ci_wait_finished result: got %q, want %q", got, "timeout")
	}
}

// AC-006: failed retry round escalates immediately (non-zero exit)
func TestRunCIGate_FailedRetryEscalates_AC006(t *testing.T) {
	redChecks := ghChecksJSON([]ghCheckItem{{Name: "test", Bucket: "fail"}})
	lsRemoteResps := []string{
		"sha1\trefs/heads/golemic/issue-42\n",
		"sha2\trefs/heads/golemic/issue-42\n",
	}
	var commentCalls []string
	exec := ciGateExecutor([]string{redChecks}, lsRemoteResps, &commentCalls)
	r, logPath, _ := buildCIGateRunner(t, exec)

	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		return 1, agent.TranscriptPaths{Stderr: "/tmp/err"}, nil
	})

	outcome := r.runCIGate(99, logPath, 5*time.Second)
	if outcome != outcomeDevFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeDevFailed)
	}
	if len(commentCalls) == 0 {
		t.Error("expected escalation comment on retry failure, got none")
	}
}

// AC-006: dev pushes nothing → escalate
func TestRunCIGate_NoPushEscalates_AC006b(t *testing.T) {
	redChecks := ghChecksJSON([]ghCheckItem{{Name: "test", Bucket: "fail"}})
	// Both ls-remote calls return the same SHA → no push detected
	sameSHA := "abc123\trefs/heads/golemic/issue-42\n"
	var commentCalls []string
	exec := ciGateExecutor([]string{redChecks}, []string{sameSHA, sameSHA}, &commentCalls)

	r, logPath, _ := buildCIGateRunner(t, exec)
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		return 0, agent.TranscriptPaths{}, nil // exits 0 but no push
	})

	outcome := r.runCIGate(99, logPath, 5*time.Second)
	if outcome != outcomeDevFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeDevFailed)
	}
	if len(commentCalls) == 0 {
		t.Error("expected escalation comment when dev pushes nothing, got none")
	}
}

// AC-007: check query failure is fail-closed
func TestRunCIGate_CheckQueryFailure_AC007(t *testing.T) {
	var commentCalls []string
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" {
				return handleGitCmd(args)
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "pr" && args[1] == "checks" {
				return "", fmt.Errorf("network error")
			}
			if len(args) >= 2 && args[0] == "pr" && args[1] == "comment" {
				commentCalls = append(commentCalls, extractBodyArg(args))
				return "", nil
			}
			return "", fmt.Errorf("not mocked: gh %v", args)
		},
	}
	r, logPath, _ := buildCIGateRunner(t, exec)

	outcome := r.runCIGate(99, logPath, 5*time.Second)
	if outcome != outcomeDevFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeDevFailed)
	}
	if len(commentCalls) == 0 {
		t.Error("expected escalation comment on check query failure, got none")
	}
}

// AC-008: prompt contains failed check info
func TestRunDevCIRetryAgent_PromptContainsCheckInfo(t *testing.T) {
	var capturedPrompt string
	homeDir, repoRoot, project := setupRunnerTest(t)
	creds := loadTestCreds(t, homeDir, project)

	guidelinesDir := filepath.Join(repoRoot, ".golemic", "guidelines")
	if err := os.MkdirAll(guidelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guidelinesDir, "dev.md"), []byte("# Guidelines"), 0644); err != nil {
		t.Fatal(err)
	}

	r := New(&fakeExecutor{}, homeDir, repoRoot, 42)
	r.repoRoot = repoRoot
	r.project = project
	r.runID = "issue-42-ci-prompt"
	r.branchName = "golemic/issue-42"
	r.creds = creds
	r.cfg = &config.Config{VerifyCommand: "go test", Models: config.Models{Dev: "test/dev"}}
	r.issue = &issueData{Number: 42, Title: "T", Body: "B"}
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		capturedPrompt = cfg.UserPrompt
		return 0, agent.TranscriptPaths{}, nil
	})

	golemicDir := filepath.Join(homeDir, ".golemic", project)
	logPath := filepath.Join(golemicDir, "runs", "issue-42-ci-prompt", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		t.Fatal(err)
	}

	failedCheckInfo := "### test\n```\ngo test: failed\n```\n"
	outcome := r.runDevCIRetryAgent(golemicDir, logPath, 5*time.Second, failedCheckInfo)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
	if !strings.Contains(capturedPrompt, failedCheckInfo) {
		t.Errorf("prompt does not contain failed check info; got: %s", capturedPrompt)
	}
	if !strings.Contains(capturedPrompt, "Do not open a new PR") {
		t.Errorf("prompt should instruct not to open a new PR")
	}
}
