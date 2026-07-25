package runner

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"golemic/internal/agent"
)

// sessionIDFor mirrors the formula in agent.RunRole so runner-level tests can
// compute expected session IDs without importing the unexported helper.
// Both dev and reviewer sessions are scoped by round (issue-212, issue-219).
func sessionIDFor(runID, role string, round int) string {
	return sanitize(fmt.Sprintf("%s-%s-r%d", runID, role, round))
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, s)
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

// TestRunnerDevSessionIDDiffersAcrossRounds verifies that dev rounds use distinct
// pi session IDs so each new round starts a fresh session (issue-219).
func TestRunnerDevSessionIDDiffersAcrossRounds_Issue219(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, logPath, _ := setupPingPongRunner(t, exec)

	devCfgs, _ := runPingPongWithCapture(t, r, logPath, "changes_requested")

	if len(devCfgs) < 2 {
		t.Fatalf("expected >= 2 dev calls (initial + retry), got %d", len(devCfgs))
	}
	// Round 1 (initial) and round 2+ (retry) must use different session IDs.
	sid1 := sessionIDFor(devCfgs[0].RunID, devCfgs[0].Role, devCfgs[0].Round)
	sid2 := sessionIDFor(devCfgs[1].RunID, devCfgs[1].Role, devCfgs[1].Round)
	if sid1 == sid2 {
		t.Errorf("dev round 1 and round 2 must have distinct session IDs, both got: %q", sid1)
	}
	assertTurnIDsIncreasing(t, devCfgs)
}

// TestRunnerReviewerSessionIDsDifferAcrossRounds verifies that reviewer rounds use
// distinct pi session IDs so each new round starts a fresh session (issue-212).
func TestRunnerReviewerSessionIDsDifferAcrossRounds_Issue212(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, logPath, _ := setupPingPongRunner(t, exec)

	_, reviewerCfgs := runPingPongWithCapture(t, r, logPath, "changes_requested")

	if len(reviewerCfgs) < 2 {
		t.Fatalf("expected >= 2 reviewer calls (round 1 + round 2), got %d", len(reviewerCfgs))
	}
	// Round 1 and round 2 must use different session IDs.
	sid1 := sessionIDFor(reviewerCfgs[0].RunID, reviewerCfgs[0].Role, reviewerCfgs[0].Round)
	sid2 := sessionIDFor(reviewerCfgs[1].RunID, reviewerCfgs[1].Role, reviewerCfgs[1].Round)
	if sid1 == sid2 {
		t.Errorf("reviewer round 1 and round 2 must have distinct session IDs, both got: %q", sid1)
	}
	// TurnIDs still increase across rounds.
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
	devSID := sessionIDFor(devCfgs[0].RunID, "dev", devCfgs[0].Round)
	reviewerSID := sessionIDFor(reviewerCfgs[0].RunID, "reviewer", reviewerCfgs[0].Round)
	if devSID == reviewerSID {
		t.Errorf("dev and reviewer must have different session IDs, both got: %q", devSID)
	}
}

// runPingPongWithCapture runs a dev→reviewer(→dev-retry→reviewer) orchestration
// where the first reviewer verdict is firstVerdict (e.g. "changes_requested" or "approved").
// Returns captured dev and reviewer RoleConfigs in order.
func runPingPongWithCapture(t *testing.T, r *Runner, logPath, firstVerdict string) (devCfgs, reviewerCfgs []agent.RoleConfig) { //nolint:cyclop
	t.Helper()
	reviewerCallCount := 0
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		switch cfg.Role {
		case "dev":
			devCfgs = append(devCfgs, cfg)
			// Satisfy the §10 gate; runner writes pr_opened on the first call.
			if !sendGMProjectCheck(cfg.Env) {
				t.Errorf("runPingPongWithCapture: sendGMProjectCheck failed")
			}
			if !sendGMDevDone(cfg.Env) {
				t.Errorf("runPingPongWithCapture: sendGMDevDone failed")
			}
		case "reviewer":
			reviewerCfgs = append(reviewerCfgs, cfg)
			if reviewerCallCount == 0 && firstVerdict != "approved" {
				writeReviewEvent(t, cfg.EventLogPath, firstVerdict, "needs work", cfg.Round, ciTestHeadSHA)
			} else {
				writeReviewEvent(t, cfg.EventLogPath, "approved", "LGTM", cfg.Round, ciTestHeadSHA)
			}
			reviewerCallCount++
		}
		return 0, agent.TranscriptPaths{}, nil
	})
	runOrchestrate(t, r, logPath)
	return devCfgs, reviewerCfgs
}
