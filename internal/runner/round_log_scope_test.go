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
)

// TestRoundLogScope_ConfigCarriesRoundAndAttempt verifies that buildDevAgentConfig
// and buildReviewerRoleConfig embed Round and Attempt in the returned RoleConfig.
func TestRoundLogScope_ConfigCarriesRoundAndAttempt(t *testing.T) {
	r, logPath, _ := setupExitCodeRunner(t, "dev")
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")
	worktreeDir := t.TempDir()

	for _, tc := range []struct{ round, attempt int }{{1, 0}, {1, 2}, {2, 0}} {
		cfg := r.buildDevAgentConfig("sys.md", "model", worktreeDir, logPath, "prompt", "binary", time.Minute, runsDir, tc.round, tc.attempt, nil)
		if cfg.Round != tc.round {
			t.Errorf("dev round=%d attempt=%d: cfg.Round got %d", tc.round, tc.attempt, cfg.Round)
		}
		if cfg.Attempt != tc.attempt {
			t.Errorf("dev round=%d attempt=%d: cfg.Attempt got %d", tc.round, tc.attempt, cfg.Attempt)
		}
	}

	for _, tc := range []struct{ round, attempt int }{{1, 0}, {1, 1}} {
		cfg := r.buildReviewerRoleConfig("sys.md", "prompt", worktreeDir, "binary", "model", logPath, runsDir, time.Minute, tc.round, tc.attempt, nil)
		if cfg.Round != tc.round {
			t.Errorf("reviewer round=%d attempt=%d: cfg.Round got %d", tc.round, tc.attempt, cfg.Round)
		}
		if cfg.Attempt != tc.attempt {
			t.Errorf("reviewer round=%d attempt=%d: cfg.Attempt got %d", tc.round, tc.attempt, cfg.Attempt)
		}
	}
}

// TestRoundLogScope_TwoAttemptsProduceDistinctFiles verifies that attempt 0 and
// attempt 1 (gate-retry) each write to a distinct activity file that does not
// overwrite the other (acceptance scenario 2 from the spec).
//
// The fake agent exits 0 without satisfying the broker gate so the runner
// iterates to attempt 1 via the gate-retry loop. Both invocations write a
// sentinel to their respective per-attempt files; we assert both files survive
// with distinct content after the run ends.
func TestRoundLogScope_TwoAttemptsProduceDistinctFiles(t *testing.T) {
	r, logPath, _ := setupExitCodeRunner(t, "dev")
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")
	runID := r.runID

	var callCount int
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		callCount++
		activityPath := filepath.Join(cfg.RunsDir, cfg.RunID,
			fmt.Sprintf("%s-r%d-a%d.activity.jsonl", cfg.Role, cfg.Round, cfg.Attempt))
		stderrPath := filepath.Join(cfg.RunsDir, cfg.RunID,
			fmt.Sprintf("%s-r%d-a%d.stderr.log", cfg.Role, cfg.Round, cfg.Attempt))

		if err := os.MkdirAll(filepath.Dir(activityPath), 0755); err != nil {
			t.Fatalf("mkdirall: %v", err)
		}
		// Write a sentinel unique to this invocation so we can distinguish files.
		sentinel := fmt.Sprintf("call=%d round=%d attempt=%d", callCount, cfg.Round, cfg.Attempt)
		if err := os.WriteFile(activityPath, []byte(sentinel), 0644); err != nil {
			t.Fatalf("write activity: %v", err)
		}

		// Exit 0 without satisfying the broker gate → runner will classify this as
		// EventDevGateRejected and retry with attempt+1.
		return 0, agent.TranscriptPaths{Stdout: activityPath, Stderr: stderrPath}, nil
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	// All attempts will produce EventDevGateRejected so runDevTurn returns outcomeDevFailed,
	// but no git operations are needed.
	r.runDevTurn(&RunContext{GolemicDir: golemicDir, EventLogPath: logPath, Timeout: 5 * time.Minute, Round: 1}, DevModeInitial)

	if callCount < 2 {
		t.Fatalf("expected at least 2 agent invocations (attempt 0 and 1), got %d", callCount)
	}

	// Both per-attempt files must exist with distinct content.
	file0 := filepath.Join(runsDir, runID, "dev-r1-a0.activity.jsonl")
	file1 := filepath.Join(runsDir, runID, "dev-r1-a1.activity.jsonl")

	content0, err := os.ReadFile(file0)
	if err != nil {
		t.Errorf("attempt-0 file %q missing: %v", file0, err)
	}
	content1, err := os.ReadFile(file1)
	if err != nil {
		t.Errorf("attempt-1 file %q missing: %v", file1, err)
	}
	if string(content0) == string(content1) {
		t.Errorf("attempt-0 and attempt-1 files have identical content %q; they must be distinct", content0)
	}
}

// TestRoundLogScope_AgentCompletedEventHasPaths verifies that the agent_completed
// event payload carries activityPath and stderrPath matching the invocation's
// round/attempt (acceptance scenario 3 from the spec).
func TestRoundLogScope_AgentCompletedEventHasPaths(t *testing.T) {
	r, logPath, _ := setupExitCodeRunner(t, "dev")

	var capturedActivity, capturedStderr string
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		// Only capture paths on the first invocation (round=1, attempt=0).
		if cfg.Attempt == 0 {
			capturedActivity = filepath.Join(cfg.RunsDir, cfg.RunID,
				fmt.Sprintf("%s-r%d-a%d.activity.jsonl", cfg.Role, cfg.Round, cfg.Attempt))
			capturedStderr = filepath.Join(cfg.RunsDir, cfg.RunID,
				fmt.Sprintf("%s-r%d-a%d.stderr.log", cfg.Role, cfg.Round, cfg.Attempt))
		}
		activityPath := filepath.Join(cfg.RunsDir, cfg.RunID,
			fmt.Sprintf("%s-r%d-a%d.activity.jsonl", cfg.Role, cfg.Round, cfg.Attempt))
		stderrPath := filepath.Join(cfg.RunsDir, cfg.RunID,
			fmt.Sprintf("%s-r%d-a%d.stderr.log", cfg.Role, cfg.Round, cfg.Attempt))
		// Exit 0 without broker gate → EventDevGateRejected; writeAgentCompleted still fires.
		return 0, agent.TranscriptPaths{Stdout: activityPath, Stderr: stderrPath}, nil
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	r.runDevTurn(&RunContext{GolemicDir: golemicDir, EventLogPath: logPath, Timeout: 5 * time.Minute, Round: 1}, DevModeInitial)

	events := readAgentCompletedEvents(t, logPath)
	if len(events) == 0 {
		t.Fatal("expected at least one agent_completed event")
	}

	var payload struct {
		Role         string `json:"role"`
		ActivityPath string `json:"activityPath"`
		StderrPath   string `json:"stderrPath"`
	}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.ActivityPath == "" {
		t.Error("agent_completed payload must have activityPath")
	}
	if payload.StderrPath == "" {
		t.Error("agent_completed payload must have stderrPath")
	}
	if payload.ActivityPath != capturedActivity {
		t.Errorf("activityPath mismatch: got %q, want %q", payload.ActivityPath, capturedActivity)
	}
	if payload.StderrPath != capturedStderr {
		t.Errorf("stderrPath mismatch: got %q, want %q", payload.StderrPath, capturedStderr)
	}
	// Must follow role-rN-aM.activity.jsonl scheme.
	if !strings.HasSuffix(payload.ActivityPath, "dev-r1-a0.activity.jsonl") {
		t.Errorf("activityPath %q must end with dev-r1-a0.activity.jsonl", payload.ActivityPath)
	}
}

// TestRoundLogScope_HeaderNamesRunDirectory verifies the run-start header shows
// the run directory and does NOT show fixed per-round log filenames
// (acceptance scenario 4 from the spec).
func TestRoundLogScope_HeaderNamesRunDirectory(t *testing.T) {
	homeDir := t.TempDir()
	r := buildHeaderRunner(t, homeDir)

	var buf bytes.Buffer
	r.writeRunHeader(&buf)
	out := buf.String()

	runsDir := filepath.Join(homeDir, ".golemic", "hdr-project", "runs", r.runID)
	if !strings.Contains(out, runsDir) {
		t.Errorf("header must contain run directory %q; got:\n%s", runsDir, out)
	}
	for _, banned := range []string{"dev.activity.jsonl", "dev.stderr.log", "reviewer.activity.jsonl", "reviewer.stderr.log"} {
		if strings.Contains(out, banned) {
			t.Errorf("header must not contain fixed log filename %q; got:\n%s", banned, out)
		}
	}
}
