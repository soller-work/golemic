package runner

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golemic/internal/agent"
	"golemic/internal/gmbroker"
	"golemic/internal/loop"
)

// TestStepRunDev_GateRejection_IncrementsAttempt_AC2a verifies that a gate rejection
// increments ctx.DevAttempt and returns EventDevGateRejected.
func TestStepRunDev_GateRejection_IncrementsAttempt_AC2a(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, _ := setupGMRunner(t)
	r.executor = exec
	injectFakeGMBrokerWithConfig(t,
		func(_ gmbroker.ProjectCheckConfig, _ string) (*gmbroker.ProjectCheckResult, error) {
			return &gmbroker.ProjectCheckResult{OK: true, WorkingTreeFingerprint: "fp-ok"}, nil
		},
		func(string) (string, error) { return "fp-ok", nil },
	)
	// Agent calls gm_dev_done without prior check → gate rejects.
	r.SetRunAgentFn(gateTestAgent(t, nil, false, true))

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	logPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")
	ctx := &RunContext{GolemicDir: golemicDir, EventLogPath: logPath, Timeout: 30 * time.Second, Round: 1}

	ev := r.stepRunDev(ctx)
	if ev != loop.EventDevGateRejected {
		t.Fatalf("expected EventDevGateRejected, got %q", ev)
	}
	if ctx.DevAttempt != 1 {
		t.Fatalf("expected DevAttempt=1 after gate rejection, got %d", ctx.DevAttempt)
	}
}

// TestStepRunDev_GateAccepted_ResetsAttempt_AC2b verifies that gate acceptance resets
// ctx.DevAttempt to 0 and returns EventDevDone.
func TestStepRunDev_GateAccepted_ResetsAttempt_AC2b(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, _ := setupGMRunner(t)
	r.executor = exec
	injectFakeGMBrokerWithConfig(t,
		func(_ gmbroker.ProjectCheckConfig, _ string) (*gmbroker.ProjectCheckResult, error) {
			return &gmbroker.ProjectCheckResult{OK: true, WorkingTreeFingerprint: "fp-ok"}, nil
		},
		func(string) (string, error) { return "fp-ok", nil },
	)
	r.SetRunAgentFn(gateTestAgent(t, nil, true, true))

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	logPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")
	ctx := &RunContext{GolemicDir: golemicDir, EventLogPath: logPath, Timeout: 30 * time.Second, Round: 1, DevAttempt: 2}

	ev := r.stepRunDev(ctx)
	if ev != loop.EventDevDone {
		t.Fatalf("expected EventDevDone, got %q", ev)
	}
	if ctx.DevAttempt != 0 {
		t.Fatalf("expected DevAttempt reset to 0 on success, got %d", ctx.DevAttempt)
	}
}

// TestRunDevTurn_ThreeGateRejections_DevFailed_AC3 verifies that 3 consecutive gate
// rejections produce outcomeDevFailed with exactly 3 agent invocations, and that
// stderr contains "gm_dev_done".
func TestRunDevTurn_ThreeGateRejections_DevFailed_AC3(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, _ := setupGMRunner(t)
	r.executor = exec
	var stderrBuf bytes.Buffer
	r.stderr = &stderrBuf
	injectFakeGMBrokerWithConfig(t,
		func(_ gmbroker.ProjectCheckConfig, _ string) (*gmbroker.ProjectCheckResult, error) {
			return &gmbroker.ProjectCheckResult{OK: true, WorkingTreeFingerprint: "fp-ok"}, nil
		},
		func(string) (string, error) { return "fp-ok", nil },
	)

	var callCount int
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		if cfg.Role != "dev" {
			t.Fatalf("unexpected role %q", cfg.Role)
		}
		callCount++
		// Never call gm_dev_done → gate always rejects.
		return 0, agent.TranscriptPaths{}, nil
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	logPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")
	ctx := &RunContext{GolemicDir: golemicDir, EventLogPath: logPath, Timeout: 30 * time.Second, Round: 1}

	outcome := r.runDevTurn(ctx, DevModeInitial)
	if outcome != outcomeDevFailed {
		t.Fatalf("expected dev_failed after 3 gate rejections, got %q", outcome)
	}
	if callCount != 3 {
		t.Fatalf("expected exactly 3 agent invocations, got %d", callCount)
	}
	stderr := stderrBuf.String()
	if !strings.Contains(stderr, "gm_dev_done") {
		t.Errorf("expected gm_dev_done in stderr, got: %s", stderr)
	}
	if !strings.Contains(stderr, "dev did not complete gm_dev_done after 3 invocations") {
		t.Errorf("expected exhaustion message in stderr, got: %s", stderr)
	}
}

// TestStepRunDev_SingleInvocation_NeverLoops_AC3b verifies BR-3: stepRunDev performs
// exactly one agent invocation per call.
func TestStepRunDev_SingleInvocation_NeverLoops_AC3b(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, _ := setupGMRunner(t)
	r.executor = exec
	injectFakeGMBrokerWithConfig(t,
		func(_ gmbroker.ProjectCheckConfig, _ string) (*gmbroker.ProjectCheckResult, error) {
			return &gmbroker.ProjectCheckResult{OK: true, WorkingTreeFingerprint: "fp-ok"}, nil
		},
		func(string) (string, error) { return "fp-ok", nil },
	)

	var callCount int
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		callCount++
		// No gm_dev_done call → gate rejects.
		return 0, agent.TranscriptPaths{}, nil
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	logPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")
	ctx := &RunContext{GolemicDir: golemicDir, EventLogPath: logPath, Timeout: 30 * time.Second, Round: 1}

	r.stepRunDev(ctx)

	if callCount != 1 {
		t.Fatalf("stepRunDev must perform exactly 1 agent invocation, got %d", callCount)
	}
}
