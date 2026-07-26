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
)

// ---------------------------------------------------------------------------
// Pure predicate unit tests
// ---------------------------------------------------------------------------

func TestClassifyDevGate_GreenTree_SelectsGreenTransition_SM001(t *testing.T) {
	got := classifyDevGate(true)
	if got != StateGateRejectedGreen {
		t.Errorf("classifyDevGate(true) = %q, want %q", got, StateGateRejectedGreen)
	}
}

func TestClassifyDevGate_RedTree_SelectsRedTransition_SM002(t *testing.T) {
	got := classifyDevGate(false)
	if got != StateGateRejectedRed {
		t.Errorf("classifyDevGate(false) = %q, want %q", got, StateGateRejectedRed)
	}
}

func TestClassifyDevGate_GreenAndRed_AreDistinctStates_SM003(t *testing.T) {
	green := classifyDevGate(true)
	red := classifyDevGate(false)
	if green == red {
		t.Fatal("green and red gate transitions must be distinct states")
	}
}

func TestReviewerFreshnessMet_True_SM004(t *testing.T) {
	if !reviewerFreshnessMet(true) {
		t.Error("reviewerFreshnessMet(true) should return true")
	}
}

func TestReviewerFreshnessMet_False_SM005(t *testing.T) {
	if reviewerFreshnessMet(false) {
		t.Error("reviewerFreshnessMet(false) should return false")
	}
}

// ---------------------------------------------------------------------------
// StateError construction and formatting
// ---------------------------------------------------------------------------

func TestStateError_Error_ContainsStatePredicateMessage_SM010(t *testing.T) {
	e := &StateError{
		State:     StateReviewerRequired,
		Predicate: "gm_review_submit",
		Message:   "no fresh submission",
	}
	got := e.Error()
	for _, want := range []string{string(StateReviewerRequired), "gm_review_submit", "no fresh submission"} {
		if !strings.Contains(got, want) {
			t.Errorf("StateError.Error() missing %q: %q", want, got)
		}
	}
}

func TestStateError_DevGate_ContainsPredicate_SM011(t *testing.T) {
	e := &StateError{
		State:     StateGateRejectedRed,
		Predicate: "gm_dev_done",
		Message:   "invocation ended without accepted call",
	}
	got := e.Error()
	for _, want := range []string{"gm_dev_done", string(StateGateRejectedRed)} {
		if !strings.Contains(got, want) {
			t.Errorf("StateError.Error() missing %q: %q", want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Integration: reviewer produces no fresh verdict → review_failed (StateError)
// ---------------------------------------------------------------------------

// TestOrchestrate_ReviewerNoFreshSubmit_Round1_StateError_SM012 verifies that when the
// reviewer completes (exit 0) but writes no event-log entry and calls no gm_review_submit,
// the machine emits a StateError and returns review_failed instead of silently nil.
func TestOrchestrate_ReviewerNoFreshSubmit_Round1_StateError_SM012(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, logPath, stderr := setupPingPongRunner(t, exec)

	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "", exitCode: 0}, // no verdict written
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeReviewFailed {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeReviewFailed, stderr.String())
	}
	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "gm_review_submit") {
		t.Errorf("expected StateError mentioning gm_review_submit in stderr, got: %s", stderrStr)
	}
	if !strings.Contains(stderrStr, string(StateReviewerRequired)) {
		t.Errorf("expected %q in stderr, got: %s", StateReviewerRequired, stderrStr)
	}
}

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

// TestOrchestrate_NormalRun_DevImplementToMerge_SM014 verifies the happy path: dev
// implements and calls gm_dev_done, the runner verifies, opens the PR, CI goes green,
// the reviewer approves, and the PR auto-merges, returning success.
func TestOrchestrate_NormalRun_DevImplementToMerge_SM014(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, logPath, stderr := setupPingPongRunner(t, exec)

	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "approved", body: "LGTM", exitCode: 0},
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Errorf("happy path: outcome %q, want %q; stderr: %s", outcome, outcomeSuccess, stderr.String())
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

	outcome := r.runDevAgent(golemicDir, logPath, 30*time.Second, "", 1)
	if outcome != outcomeDevFailed {
		t.Fatalf("expected dev_failed, got %q", outcome)
	}
	stderrStr := stderrBuf.String()
	if !strings.Contains(stderrStr, string(StateGateRejectedRed)) {
		t.Errorf("expected %q in stderr, got: %s", StateGateRejectedRed, stderrStr)
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

	outcome := r.runDevAgent(golemicDir, logPath, 30*time.Second, "", 1)
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
