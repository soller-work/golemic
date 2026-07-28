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

// ---------------------------------------------------------------------------
// Integration: reviewer produces no fresh verdict → review_failed (StateError)
// ---------------------------------------------------------------------------

// TestOrchestrate_ReviewerNoFreshSubmit_Round2_StaleVerdictNotConsumed_SM013 verifies the
// critical stale-verdict case: round-2 reviewer completes without a fresh verdict while a
// round-1 changes_requested event exists.  The machine must emit a StateError and return
// review_failed WITHOUT consuming the round-1 verdict (i.e. no extra dev-retry round).
func TestOrchestrate_ReviewerNoFreshSubmit_Round2_StaleVerdictNotConsumed_SM013(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, logPath, stderr := setupPingPongRunner(t, exec)

	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "changes_requested", body: "Fix something", exitCode: 0}, // round-1
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "", exitCode: 0}, // round-2: no fresh verdict
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeReviewFailed {
		t.Errorf("outcome: got %q, want review_failed (stale verdict must not be consumed); stderr: %s", outcome, stderr.String())
	}
	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "gm_review_submit") {
		t.Errorf("expected gm_review_submit predicate in stderr, got: %s", stderrStr)
	}
	// If the stale round-1 changes_requested verdict were consumed, the runner would
	// attempt another dev round (which would panic on the missing agent config).
	// Getting review_failed here proves it was not consumed.
	if outcome == outcomeEscalated {
		t.Error("stale round-1 changes_requested must not trigger escalation")
	}
}

// ---------------------------------------------------------------------------
// Integration: dev gate-rejected classification (green/red) emitted to stderr
// ---------------------------------------------------------------------------

// TestRunDevAgent_GateRejected_RedTree_EmitsStateErrorRed_SM015 verifies that a gate
// rejection where the tree is red (no prior gm_project_check) produces a StateError
// with GATE_REJECTED_RED in stderr, proving the classifier fires.
func TestRunDevAgent_GateRejected_RedTree_EmitsStateErrorRed_SM015(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, _ := setupGMRunner(t)
	r.executor = exec

	var stderrBuf bytes.Buffer
	r.stderr = &stderrBuf

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	logPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")

	injectFakeGMBrokerWithConfig(t,
		func(_ gmbroker.ProjectCheckConfig, _ string) (*gmbroker.ProjectCheckResult, error) {
			return &gmbroker.ProjectCheckResult{OK: true, WorkingTreeFingerprint: "fp-ok"}, nil
		},
		func(string) (string, error) { return "fp-ok", nil },
	)

	// Agent calls gm_dev_done without a prior gm_project_check → gate rejects (red).
	r.SetRunAgentFn(gateTestAgent(t, nil, false, true))

	outcome := r.runMachineFrom(loop.StepRunDev, &RunContext{GolemicDir: golemicDir, EventLogPath: logPath, Timeout: 30 * time.Second, Round: 1, DevMode: DevModeInitial})
	if outcome != outcomeDevFailed {
		t.Fatalf("expected dev_failed, got %q", outcome)
	}
	stderrStr := stderrBuf.String()
	if !strings.Contains(stderrStr, string(loop.EventDevGateRejected)) {
		t.Errorf("expected %q in stderr, got: %s", loop.EventDevGateRejected, stderrStr)
	}
	if !strings.Contains(stderrStr, "gm_dev_done") {
		t.Errorf("expected gm_dev_done predicate in stderr, got: %s", stderrStr)
	}
}

// TestRunDevAgent_MissingDevDone_EmitsStateError_BoundedRetry_SM016 verifies that a dev
// invocation that never calls gm_dev_done emits a StateError naming gm_dev_done, routes
// to the bounded gate-retry (3 invocations total), and maps to dev_failed on exhaustion.
func TestRunDevAgent_MissingDevDone_EmitsStateError_BoundedRetry_SM016(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, _ := setupGMRunner(t)
	r.executor = exec

	var stderrBuf bytes.Buffer
	r.stderr = &stderrBuf

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	logPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")

	injectFakeGMBrokerWithConfig(t,
		func(_ gmbroker.ProjectCheckConfig, _ string) (*gmbroker.ProjectCheckResult, error) {
			return &gmbroker.ProjectCheckResult{OK: true, WorkingTreeFingerprint: "fp-ok"}, nil
		},
		func(string) (string, error) { return "fp-ok", nil },
	)

	var callCount int
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		callCount++
		// Never call gm_dev_done.
		return 0, agent.TranscriptPaths{}, nil
	})

	outcome := r.runMachineFrom(loop.StepRunDev, &RunContext{GolemicDir: golemicDir, EventLogPath: logPath, Timeout: 30 * time.Second, Round: 1, DevMode: DevModeInitial})
	if outcome != outcomeDevFailed {
		t.Fatalf("expected dev_failed, got %q", outcome)
	}
	if callCount != 3 {
		t.Fatalf("expected 3 invocations (bounded retry), got %d", callCount)
	}
	stderrStr := stderrBuf.String()
	if !strings.Contains(stderrStr, "gm_dev_done") {
		t.Errorf("expected gm_dev_done predicate in StateError, got: %s", stderrStr)
	}
}
