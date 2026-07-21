package runner

import (
	"context"
	"strings"
	"testing"

	"golemic/internal/agent"
)

// sessionIDFor mirrors the formula in agent.RunRole (sanitizeSessionID(RunID + "-" + Role))
// so runner-level tests can assert session-ID stability without importing the unexported helper.
func sessionIDFor(runID, role string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, runID+"-"+role)
}

// assertTurnIDsIncreasing asserts each consecutive pair is strictly increasing.
func assertTurnIDsIncreasing(t *testing.T, cfgs []agent.RoleConfig) {
	t.Helper()
	for i := 1; i < len(cfgs); i++ {
		if cfgs[i].TurnID <= cfgs[i-1].TurnID {
			t.Errorf("TurnID not increasing at index %d: %d → %d",
				i, cfgs[i-1].TurnID, cfgs[i].TurnID)
		}
	}
}

// assertSessionIDsEqual asserts all cfgs produce the same session ID as cfgs[0].
func assertSessionIDsEqual(t *testing.T, cfgs []agent.RoleConfig) {
	t.Helper()
	want := sessionIDFor(cfgs[0].RunID, cfgs[0].Role)
	for i, cfg := range cfgs[1:] {
		got := sessionIDFor(cfg.RunID, cfg.Role)
		if got != want {
			t.Errorf("session ID mismatch at index %d: got %q, want %q", i+1, got, want)
		}
	}
}

// TestRunnerSessionIDStableAcrossDevTurns verifies that all dev turns (initial and
// retry) produce the same Pi session ID while TurnIDs remain distinct.
func TestRunnerSessionIDStableAcrossDevTurns_Issue147(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, logPath, _ := setupPingPongRunner(t, exec)

	devCfgs, _ := runPingPongWithCapture(t, r, logPath, "changes_requested")

	if len(devCfgs) < 2 {
		t.Fatalf("expected >= 2 dev calls (initial + retry), got %d", len(devCfgs))
	}
	assertSessionIDsEqual(t, devCfgs)
	assertTurnIDsIncreasing(t, devCfgs)
}

// TestRunnerSessionIDStableAcrossReviewerTurns verifies that all reviewer turns
// produce the same Pi session ID while TurnIDs remain distinct.
func TestRunnerSessionIDStableAcrossReviewerTurns_Issue147(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, logPath, _ := setupPingPongRunner(t, exec)

	_, reviewerCfgs := runPingPongWithCapture(t, r, logPath, "changes_requested")

	if len(reviewerCfgs) < 2 {
		t.Fatalf("expected >= 2 reviewer calls, got %d", len(reviewerCfgs))
	}
	assertSessionIDsEqual(t, reviewerCfgs)
	assertTurnIDsIncreasing(t, reviewerCfgs)
}

// TestRunnerDevAndReviewerSessionIDsDiffer verifies that dev and reviewer maintain
// separate session IDs within the same run.
func TestRunnerDevAndReviewerSessionIDsDiffer_Issue147(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, logPath, _ := setupPingPongRunner(t, exec)

	devCfgs, reviewerCfgs := runPingPongWithCapture(t, r, logPath, "approved")

	if len(devCfgs) == 0 || len(reviewerCfgs) == 0 {
		t.Fatalf("missing dev or reviewer calls")
	}
	devSID := sessionIDFor(devCfgs[0].RunID, "dev")
	reviewerSID := sessionIDFor(reviewerCfgs[0].RunID, "reviewer")
	if devSID == reviewerSID {
		t.Errorf("dev and reviewer must have different session IDs, both got: %q", devSID)
	}
}

// runPingPongWithCapture runs a dev→reviewer(→dev-retry→reviewer) orchestration
// where the first reviewer verdict is firstVerdict (e.g. "changes_requested" or "approved").
// Returns captured dev and reviewer RoleConfigs in order.
func runPingPongWithCapture(t *testing.T, r *Runner, logPath, firstVerdict string) (devCfgs, reviewerCfgs []agent.RoleConfig) { //nolint:cyclop
	t.Helper()
	devCallCount := 0
	reviewerCallCount := 0
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		switch cfg.Role {
		case "dev":
			devCfgs = append(devCfgs, cfg)
			if devCallCount == 0 {
				writePROpenedEvent(t, cfg.EventLogPath, 99)
			}
			devCallCount++
		case "reviewer":
			reviewerCfgs = append(reviewerCfgs, cfg)
			if reviewerCallCount == 0 && firstVerdict != "approved" {
				writeReviewEvent(t, cfg.EventLogPath, firstVerdict, "needs work")
			} else {
				writeReviewEvent(t, cfg.EventLogPath, "approved", "LGTM")
			}
			reviewerCallCount++
		}
		return 0, agent.TranscriptPaths{}, nil
	})
	runOrchestrate(t, r, logPath)
	return devCfgs, reviewerCfgs
}
