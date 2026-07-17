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
	"golemic/internal/prompt"
)

// ---------------------------------------------------------------------------
// Helpers shared across exit-code tests
// ---------------------------------------------------------------------------

func setupExitCodeRunner(t *testing.T, role string) (r *Runner, eventLogPath string, stderr *bytes.Buffer) {
	t.Helper()
	homeDir, repoRoot, project := setupRunnerTest(t)

	guidelinesDir := filepath.Join(repoRoot, ".golemic", "guidelines")
	if err := os.MkdirAll(guidelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"dev.md", "reviewer.md"} {
		if err := os.WriteFile(filepath.Join(guidelinesDir, f), []byte("# guidelines"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	loader := credentials.NewLoader(homeDir)
	creds, err := loader.Load(project)
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}

	runID := "issue-42-20240101T000000Z"
	runsDir := filepath.Join(homeDir, ".golemic", project, "runs")
	logPath := filepath.Join(runsDir, runID, "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		t.Fatal(err)
	}

	w, err := eventlog.NewWriter(logPath)
	if err != nil {
		t.Fatal(err)
	}
	startPayload, _ := json.Marshal(map[string]interface{}{"issue": 42, "runId": runID})
	_ = w.Write(eventlog.Event{
		Type:    eventlog.EventRunStarted,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   runID,
		Payload: startPayload,
	})
	if role == "reviewer" {
		prPayload, _ := json.Marshal(map[string]string{"prNumber": "99"})
		_ = w.Write(eventlog.Event{
			Type:    eventlog.EventPROpened,
			Ts:      time.Now().Format(time.RFC3339),
			RunID:   runID,
			Payload: prPayload,
		})
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	runner := New(nil, homeDir, repoRoot, 42)
	runner.repoRoot = repoRoot
	runner.project = project
	runner.homeDir = homeDir
	runner.runID = runID
	runner.creds = creds
	runner.issue = &issueData{Number: 42, Title: "t", Body: "b"}
	runner.cfg = &config.Config{
		VerifyCommand: "go test",
		Models:        config.Models{Dev: "test-model", Reviewer: "test-model"},
	}
	runner.branchName = "golemic/issue-42"

	var buf bytes.Buffer
	runner.SetStderr(&buf)
	return runner, logPath, &buf
}

func fakeTranscriptPaths(dir, role string) agent.TranscriptPaths {
	return agent.TranscriptPaths{
		Stdout: filepath.Join(dir, role+".stdout.log"),
		Stderr: filepath.Join(dir, role+".stderr.log"),
	}
}

func readAgentCompletedEvents(t *testing.T, logPath string) []eventlog.Event {
	t.Helper()
	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	var out []eventlog.Event
	for _, ev := range events {
		if ev.Type == eventlog.EventAgentCompleted {
			out = append(out, ev)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// AC-002: Dev agent non-zero exit returns dev_failed and emits diagnostic
// ---------------------------------------------------------------------------

func TestRunDevAgent_NonZeroExit_ReturnsDevFailed_AC002(t *testing.T) {
	r, logPath, stderr := setupExitCodeRunner(t, "dev")
	fakeStderrPath := "/tmp/runs/issue-42-20240101T000000Z/dev.stderr.log"
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		return 2, agent.TranscriptPaths{Stderr: fakeStderrPath}, nil
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	outcome := r.runDevAgent(golemicDir, logPath, 5*time.Minute)

	if outcome != outcomeDevFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeDevFailed)
	}
	msg := stderr.String()
	if !strings.Contains(msg, "dev_failed") {
		t.Errorf("stderr should contain 'dev_failed', got: %q", msg)
	}
	if !strings.Contains(msg, "2") {
		t.Errorf("stderr should contain exit code '2', got: %q", msg)
	}
	if !strings.Contains(msg, fakeStderrPath) {
		t.Errorf("stderr should contain transcript path %q, got: %q", fakeStderrPath, msg)
	}
}

// AC-002: runAgentFn is only invoked for dev when dev exits non-zero
func TestRunDevAgent_NonZeroExit_ReviewerNotCalled_AC002(t *testing.T) {
	r, logPath, _ := setupExitCodeRunner(t, "dev")
	var calledRoles []string
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		calledRoles = append(calledRoles, cfg.Role)
		return 42, fakeTranscriptPaths("/tmp", cfg.Role), nil
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	outcome := r.runDevAgent(golemicDir, logPath, 5*time.Minute)

	if outcome != outcomeDevFailed {
		t.Fatalf("expected dev_failed, got %q", outcome)
	}
	for _, role := range calledRoles {
		if role == "reviewer" {
			t.Error("reviewer agent must not be called when dev exits non-zero")
		}
	}
}

// ---------------------------------------------------------------------------
// AC-001: Reviewer agent non-zero exit returns review_failed and emits diagnostic
// ---------------------------------------------------------------------------

func TestRunReviewerAgent_NonZeroExit_ReturnsReviewFailed_AC001(t *testing.T) {
	r, logPath, stderr := setupExitCodeRunner(t, "reviewer")
	fakeStderrPath := "/tmp/runs/issue-42-20240101T000000Z/reviewer.stderr.log"
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		return 1, agent.TranscriptPaths{Stderr: fakeStderrPath}, nil
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	outcome := r.runReviewerAgent(golemicDir, logPath, 5*time.Minute)

	if outcome != outcomeReviewFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeReviewFailed)
	}
	msg := stderr.String()
	if !strings.Contains(msg, "review_failed") {
		t.Errorf("stderr should contain 'review_failed', got: %q", msg)
	}
	if !strings.Contains(msg, "1") {
		t.Errorf("stderr should contain exit code '1', got: %q", msg)
	}
	if !strings.Contains(msg, fakeStderrPath) {
		t.Errorf("stderr should contain transcript path %q, got: %q", fakeStderrPath, msg)
	}
}

// ---------------------------------------------------------------------------
// AC-003: Agent exit code is recorded in the event log
// ---------------------------------------------------------------------------

func TestRunDevAgent_ExitCodeRecordedInEventLog_AC003(t *testing.T) {
	for _, tc := range []struct {
		name     string
		exitCode int
	}{
		{"zero exit", 0},
		{"non-zero exit", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, logPath, _ := setupExitCodeRunner(t, "dev")
			r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
				return tc.exitCode, fakeTranscriptPaths("/tmp", "dev"), nil
			})

			golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
			r.runDevAgent(golemicDir, logPath, 5*time.Minute)

			completedEvents := readAgentCompletedEvents(t, logPath)
			if len(completedEvents) != 1 {
				t.Fatalf("expected 1 agent_completed event, got %d", len(completedEvents))
			}
			var payload struct {
				Role     string `json:"role"`
				ExitCode int    `json:"exitCode"`
			}
			if err := json.Unmarshal(completedEvents[0].Payload, &payload); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			if payload.Role != "dev" {
				t.Errorf("role: got %q, want %q", payload.Role, "dev")
			}
			if payload.ExitCode != tc.exitCode {
				t.Errorf("exitCode: got %d, want %d", payload.ExitCode, tc.exitCode)
			}
		})
	}
}

func TestRunReviewerAgent_ExitCodeRecordedInEventLog_AC003(t *testing.T) {
	r, logPath, _ := setupExitCodeRunner(t, "reviewer")
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		return 5, fakeTranscriptPaths("/tmp", "reviewer"), nil
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	r.runReviewerAgent(golemicDir, logPath, 5*time.Minute)

	completedEvents := readAgentCompletedEvents(t, logPath)
	if len(completedEvents) != 1 {
		t.Fatalf("expected 1 agent_completed event, got %d", len(completedEvents))
	}
	var payload struct {
		Role     string `json:"role"`
		ExitCode int    `json:"exitCode"`
	}
	if err := json.Unmarshal(completedEvents[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Role != "reviewer" {
		t.Errorf("role: got %q, want %q", payload.Role, "reviewer")
	}
	if payload.ExitCode != 5 {
		t.Errorf("exitCode: got %d, want 5", payload.ExitCode)
	}
}

// ---------------------------------------------------------------------------
// AC-004: Zero-exit reviewer with no review_submitted event uses existing message
// ---------------------------------------------------------------------------

func TestRunReviewerAgent_ZeroExit_NoReviewSubmitted_ExistingMessage_AC004(t *testing.T) {
	r, logPath, stderr := setupExitCodeRunner(t, "reviewer")
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		return 0, fakeTranscriptPaths("/tmp", "reviewer"), nil
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	outcome := r.runReviewerAgent(golemicDir, logPath, 5*time.Minute)

	if outcome != outcomeSuccess {
		t.Fatalf("runReviewerAgent should succeed on zero exit, got %q", outcome)
	}
	if strings.Contains(stderr.String(), "exited with code") {
		t.Errorf("zero-exit must not produce exit-code diagnostic, got: %q", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// AC-005: Diagnostic never contains agent stderr content
// ---------------------------------------------------------------------------

func TestRunDevAgent_NonZeroExit_DiagnosticNoStderrContent_AC005(t *testing.T) {
	r, logPath, stderr := setupExitCodeRunner(t, "dev")

	secret := "ghp_secret_token_abc123"
	fakeStderrContent := fmt.Sprintf("error: %s rate limited", secret)
	fakeStderrPath := t.TempDir() + "/dev.stderr.log"

	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		_ = os.WriteFile(fakeStderrPath, []byte(fakeStderrContent), 0644) //nolint:errcheck
		return 1, agent.TranscriptPaths{Stderr: fakeStderrPath}, nil
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	r.runDevAgent(golemicDir, logPath, 5*time.Minute)

	msg := stderr.String()
	if strings.Contains(msg, secret) {
		t.Errorf("diagnostic must not contain agent stderr content (secret leaked), got: %q", msg)
	}
	if strings.Contains(msg, fakeStderrContent) {
		t.Errorf("diagnostic must not contain agent stderr content, got: %q", msg)
	}
	if !strings.Contains(msg, fakeStderrPath) {
		t.Errorf("diagnostic should contain transcript path %q, got: %q", fakeStderrPath, msg)
	}
}

// ---------------------------------------------------------------------------
// Zero-exit reviewer returns success
// ---------------------------------------------------------------------------

func TestRunReviewerAgent_ZeroExit_ReturnsSuccess(t *testing.T) {
	r, logPath, stderr := setupExitCodeRunner(t, "reviewer")
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		return 0, fakeTranscriptPaths("/tmp", "reviewer"), nil
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	outcome := r.runReviewerAgent(golemicDir, logPath, 5*time.Minute)

	if outcome != outcomeSuccess {
		t.Errorf("expected outcomeSuccess on zero exit, got %q; stderr: %s", outcome, stderr.String())
	}
	if strings.Contains(stderr.String(), "exited with code") {
		t.Errorf("zero-exit must not produce exit-code diagnostic, got: %q", stderr.String())
	}
}

// import guard
var _ = prompt.RenderReviewer
