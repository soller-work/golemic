package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"golemic/internal/agent"
	"golemic/internal/config"
	"golemic/internal/loop"
	"golemic/internal/preflight"
)

// ---------------------------------------------------------------------------
// Helpers — conflict executor builders
// ---------------------------------------------------------------------------

// stalePingPongExecutorWithConflictRetry returns an executor where the branch is
// stale (first merge-base call fails) and rebase always fails with a conflict.
// The agent attempts are supplied via agentFn, not the executor. The executor
// reports verifyRebaseComplete success after the first resolve attempt (controlled
// by resolvedOnAttempt: 0 = never, N = after Nth git rev-parse REBASE_HEAD check).
func stalePingPongWithConflictAndVerify(commentCalls *[]string, rebaseHeadErrorAfterAttempt int) *fakeExecutor { //nolint:cyclop,gocognit // simulating stateful git interactions requires tracking call counts
	base := pingPongExecutor(false, commentCalls)

	mergeBaseCallCount := 0
	rebaseHeadCallCount := 0
	statusAfterConflictCount := 0
	rebaseCalled := false

	innerRun := base.runFunc
	base.runFunc = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) >= 1 && args[0] == "merge-base" {
			mergeBaseCallCount++
			if mergeBaseCallCount == 1 {
				return "", &preflight.ErrExit{ExitCode: 1} // stale
			}
			return "", nil
		}
		if name == "git" && len(args) >= 1 && args[0] == "rebase" {
			if len(args) >= 2 && args[1] == "--abort" {
				return "", nil
			}
			if !rebaseCalled {
				rebaseCalled = true
				return "", &preflight.ErrExit{ExitCode: 1} // conflict on first rebase
			}
			return "", nil
		}
		if name == "git" && len(args) >= 1 && args[0] == "status" {
			// After conflict is triggered, first status call detects unmerged path.
			if rebaseCalled {
				statusAfterConflictCount++
				if statusAfterConflictCount == 1 {
					return "UU internal/foo/bar.go\n", nil // conflict detected
				}
				// Subsequent calls: clean tree
				return "", nil
			}
		}
		if name == "git" && len(args) >= 3 && args[1] == "--verify" && args[2] == "REBASE_HEAD" {
			rebaseHeadCallCount++
			if rebaseHeadCallCount <= rebaseHeadErrorAfterAttempt {
				return "", nil // REBASE_HEAD exists → rebase still in progress
			}
			return "", fmt.Errorf("not found") // rebase done
		}
		if name == "git" && len(args) >= 2 && args[0] == "diff" && args[1] == "--name-only" {
			return "internal/foo/bar.go\n", nil
		}
		return innerRun(name, args...)
	}
	return base
}

// ---------------------------------------------------------------------------
// Site 1 (SYNC_CI): Bounded retry — first attempt fails, second resolves
// ---------------------------------------------------------------------------

// TestConflictRetry_SyncCI_FirstFailSecondResolves verifies that when the first
// conflict-resolution attempt leaves the rebase incomplete but a subsequent attempt
// within the cap resolves it, the worktree is not aborted between attempts and the
// run proceeds to the reviewer (regression guard for issue-270).
func TestConflictRetry_SyncCI_FirstFailSecondResolves(t *testing.T) { //nolint:cyclop,funlen
	var commentCalls []string
	// rebaseHeadErrorAfterAttempt=1: first REBASE_HEAD check returns success (rebase in progress),
	// second check returns "not found" (rebase done).
	exec := stalePingPongWithConflictAndVerify(&commentCalls, 1)

	abortCallCount := 0
	innerRun := exec.runFunc
	exec.runFunc = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) >= 2 && args[0] == "rebase" && args[1] == "--abort" {
			abortCallCount++
		}
		return innerRun(name, args...)
	}

	r, logPath, stderr := setupPingPongRunner(t, exec)
	r.cfg.MaxConflictResolutionAttempts = 2

	agentCallCount := 0
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		agentCallCount++
		switch cfg.Role {
		case "dev":
			if agentCallCount == 1 {
				// Initial dev turn.
				sendGMProjectCheck(cfg.Env) //nolint:errcheck
				sendGMDevDone(cfg.Env)      //nolint:errcheck
				return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
			}
			// Conflict resolution turns: always exit 0 (resolved or not determined by verifyRebaseComplete).
			return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
		case "reviewer":
			writeReviewEvent(t, cfg.EventLogPath, "approved", "LGTM after retry", cfg.Round, ciTestHeadSHA)
			return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
		default:
			t.Errorf("unexpected role: %s", cfg.Role)
			return 1, agent.TranscriptPaths{}, fmt.Errorf("unexpected role")
		}
	})

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeSuccess, stderr.String())
	}

	// No abort between attempts — abort must not have been called before cap was exhausted.
	// With cap=2 and success on 2nd attempt, abort should not be called at all.
	if abortCallCount > 0 {
		t.Errorf("git rebase --abort called %d times; want 0 (worktree must be preserved between retry attempts)", abortCallCount)
	}

	// Reviewer worktree should have been created (run proceeded past SYNC_CI).
	if firstReviewerWorktreeAddIndex(exec.calls, r.repoRoot) == -1 {
		t.Error("reviewer worktree not created: run must proceed to reviewer after conflict retry resolves")
	}
}

// ---------------------------------------------------------------------------
// Site 1 (SYNC_CI): Cap=1 reproduces today's single-attempt behavior
// ---------------------------------------------------------------------------

// TestConflictRetry_SyncCI_Cap1_SingleFailTerminates verifies that when the cap is 1
// and the single attempt fails, the run terminates dev_failed immediately.
func TestConflictRetry_SyncCI_Cap1_SingleFailTerminates(t *testing.T) {
	var commentCalls []string
	// rebaseHeadErrorAfterAttempt=100: REBASE_HEAD always exists → all attempts unresolved.
	exec := stalePingPongWithConflictAndVerify(&commentCalls, 100)

	r, logPath, _ := setupPingPongRunner(t, exec)
	r.cfg.MaxConflictResolutionAttempts = 1

	agentCallCount := 0
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		agentCallCount++
		if cfg.Role == "dev" {
			if agentCallCount == 1 {
				sendGMProjectCheck(cfg.Env) //nolint:errcheck
				sendGMDevDone(cfg.Env)      //nolint:errcheck
				return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
			}
			return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
		}
		t.Errorf("unexpected role: %s (only dev should run)", cfg.Role)
		return 1, agent.TranscriptPaths{}, fmt.Errorf("unexpected role")
	})

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeDevFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeDevFailed)
	}

	// With cap=1, exactly one conflict-resolution agent call (plus the initial dev call).
	if agentCallCount != 2 {
		t.Errorf("agent called %d times, want 2 (initial dev + one conflict attempt)", agentCallCount)
	}

	// No reviewer worktree.
	if firstReviewerWorktreeAddIndex(exec.calls, r.repoRoot) != -1 {
		t.Error("reviewer worktree must not be created when conflict resolution fails")
	}
}

// ---------------------------------------------------------------------------
// Site 1 (SYNC_CI): Cap exhausted — reason names rebase conflict, not CI failure
// ---------------------------------------------------------------------------

// TestConflictRetry_SyncCI_CapExhausted_ConflictReason verifies that when the
// rebase-conflict cap is exhausted, the run terminates dev_failed and the failure
// reason identifies a rebase conflict rather than a CI failure.
func TestConflictRetry_SyncCI_CapExhausted_ConflictReason(t *testing.T) { //nolint:cyclop
	var commentCalls []string
	// rebaseHeadErrorAfterAttempt=100: REBASE_HEAD always exists → all attempts fail.
	exec := stalePingPongWithConflictAndVerify(&commentCalls, 100)

	abortCallCount := 0
	innerRun := exec.runFunc
	exec.runFunc = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) >= 2 && args[0] == "rebase" && args[1] == "--abort" {
			abortCallCount++
		}
		return innerRun(name, args...)
	}

	r, logPath, stderr := setupPingPongRunner(t, exec)
	r.cfg.MaxConflictResolutionAttempts = 2

	agentCallCount := 0
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		agentCallCount++
		if cfg.Role == "dev" {
			if agentCallCount == 1 {
				sendGMProjectCheck(cfg.Env) //nolint:errcheck
				sendGMDevDone(cfg.Env)      //nolint:errcheck
				return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
			}
			return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
		}
		t.Errorf("unexpected role: %s", cfg.Role)
		return 1, agent.TranscriptPaths{}, fmt.Errorf("unexpected role")
	})

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeDevFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeDevFailed)
	}

	// Agent called initial dev + 2 conflict resolution attempts = 3 total.
	if agentCallCount != 3 {
		t.Errorf("agent called %d times, want 3 (initial dev + 2 conflict attempts)", agentCallCount)
	}

	// git rebase --abort must be called exactly once, after cap exhaustion.
	if abortCallCount != 1 {
		t.Errorf("git rebase --abort called %d times, want 1 (only after cap exhausted)", abortCallCount)
	}

	// Failure reason must identify a rebase conflict, not CI.
	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "rebase conflict") {
		t.Errorf("failure reason must mention 'rebase conflict', stderr: %s", stderrStr)
	}
	if strings.Contains(stderrStr, "CI failed") || strings.Contains(stderrStr, "ci failed") {
		t.Errorf("failure reason must not mention CI failure for a rebase conflict, stderr: %s", stderrStr)
	}

	// No reviewer worktree.
	if firstReviewerWorktreeAddIndex(exec.calls, r.repoRoot) != -1 {
		t.Error("reviewer worktree must not be created when conflict cap is exhausted")
	}
}

// ---------------------------------------------------------------------------
// Site 2 (MERGE_PR): Conflict resolved → routes to reviewer before merging
// ---------------------------------------------------------------------------

// mergeTimeConflictExecutor builds a fakeExecutor that simulates a merge-time
// rebase conflict. Before the reviewer the branch is up-to-date; at merge time
// the branch is behind, rebase fails with a conflict, and the agent resolves it.
func mergeTimeConflictExecutor(t *testing.T, commentCalls *[]string, mergeBaseStaleOnCall int) *fakeExecutor { //nolint:cyclop,funlen,gocognit
	t.Helper()
	base := pingPongExecutor(false, commentCalls)

	mergeBaseCallCount := 0
	statusCalls := 0
	rebaseAttempted := false

	innerRun := base.runFunc
	base.runFunc = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) >= 1 && args[0] == "merge-base" {
			mergeBaseCallCount++
			if mergeBaseCallCount == mergeBaseStaleOnCall {
				return "", &preflight.ErrExit{ExitCode: 1} // stale at merge time
			}
			return "", nil // up-to-date otherwise
		}
		if name == "git" && len(args) >= 1 && args[0] == "rebase" {
			if len(args) >= 2 && args[1] == "--abort" {
				return "", nil
			}
			if !rebaseAttempted {
				rebaseAttempted = true
				return "", &preflight.ErrExit{ExitCode: 1} // conflict
			}
			return "", nil
		}
		if name == "git" && len(args) >= 1 && args[0] == "status" {
			if rebaseAttempted {
				statusCalls++
				if statusCalls == 1 {
					return "UU internal/foo/bar.go\n", nil // unmerged after rebase
				}
			}
			return "", nil
		}
		if name == "git" && len(args) >= 3 && args[1] == "--verify" && args[2] == "REBASE_HEAD" {
			return "", fmt.Errorf("not found") // rebase done after agent
		}
		if name == "git" && len(args) >= 2 && args[0] == "diff" && args[1] == "--name-only" {
			return "internal/foo/bar.go\n", nil
		}
		return innerRun(name, args...)
	}
	return base
}

// TestConflictRetry_MergeTime_ConflictResolved_RoutesToReviewer verifies that when
// a post-approval merge-time rebase conflict is resolved within the cap, the machine
// routes to RUN_REVIEWER for a fresh verdict before merging, rather than merging
// on the unreviewed resolution.
func TestConflictRetry_MergeTime_ConflictResolved_RoutesToReviewer(t *testing.T) { //nolint:cyclop,funlen
	var commentCalls []string
	// mergeBaseStaleOnCall=2: first call (SYNC_CI) is up-to-date; second call (MERGE_PR) is stale.
	exec := mergeTimeConflictExecutor(t, &commentCalls, 2)

	reviewerCallCount := 0
	r, logPath, stderr := setupPingPongRunner(t, exec)
	r.cfg.MaxConflictResolutionAttempts = 2

	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		switch cfg.Role {
		case "dev":
			sendGMProjectCheck(cfg.Env) //nolint:errcheck
			sendGMDevDone(cfg.Env)      //nolint:errcheck
			return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
		case "reviewer":
			reviewerCallCount++
			switch reviewerCallCount {
			case 1:
				// Pre-approval reviewer: approve.
				writeReviewEvent(t, cfg.EventLogPath, "approved", "LGTM", cfg.Round, ciTestHeadSHA)
			case 2:
				// Merge-time re-review after conflict resolved: approve again.
				writeReviewEvent(t, cfg.EventLogPath, "approved", "Still LGTM", cfg.Round, ciTestHeadSHA)
			default:
				t.Errorf("reviewer called %d times, want at most 2", reviewerCallCount)
			}
			return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
		default:
			t.Errorf("unexpected role: %s", cfg.Role)
			return 1, agent.TranscriptPaths{}, fmt.Errorf("unexpected role")
		}
	})

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeSuccess, stderr.String())
	}

	// Reviewer must have been called twice: once pre-approval, once for merge re-review.
	if reviewerCallCount != 2 {
		t.Errorf("reviewer called %d times, want 2 (pre-approval + merge re-review)", reviewerCallCount)
	}
}

// TestConflictRetry_MergeTime_ReReviewChangesRequested_EscalatesWhenBudgetExhausted verifies
// that when a merge-time re-review returns changes_requested and the separate budget is
// exhausted, the run escalates rather than looping or merging.
func TestConflictRetry_MergeTime_ReReviewChangesRequested_EscalatesWhenBudgetExhausted(t *testing.T) { //nolint:cyclop,funlen
	var commentCalls []string
	exec := mergeTimeConflictExecutor(t, &commentCalls, 2)

	// Track multiple merge-base calls carefully: after the merge-time re-review sends
	// the dev back, the SYNC_CI will do another freshness check. We need to handle this.
	mergeBaseCallCount := 0
	innerRun := exec.runFunc
	exec.runFunc = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) >= 1 && args[0] == "merge-base" {
			mergeBaseCallCount++
			// Call 1: SYNC_CI before pre-approval reviewer → up-to-date
			// Call 2: MERGE_PR → stale (triggers conflict)
			// Call 3+: after dev re-addresses findings → up-to-date
			if mergeBaseCallCount == 2 {
				return "", &preflight.ErrExit{ExitCode: 1}
			}
			return "", nil
		}
		return innerRun(name, args...)
	}

	reviewerCallCount := 0
	r, logPath, stderr := setupPingPongRunner(t, exec)
	r.cfg.MaxConflictResolutionAttempts = 2
	// MaxMergeReReviewRounds stays at maxMergeReReviewRounds (2 by default).

	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		switch cfg.Role {
		case "dev":
			sendGMProjectCheck(cfg.Env) //nolint:errcheck
			sendGMDevDone(cfg.Env)      //nolint:errcheck
			return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
		case "reviewer":
			reviewerCallCount++
			switch reviewerCallCount {
			case 1:
				// Pre-approval reviewer: approve.
				writeReviewEvent(t, cfg.EventLogPath, "approved", "LGTM", cfg.Round, ciTestHeadSHA)
			default:
				// Merge-time re-review: always request changes to exhaust the budget.
				writeReviewEvent(t, cfg.EventLogPath, "changes_requested", "Fix the merge conflict resolution", cfg.Round, ciTestHeadSHA)
			}
			return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
		default:
			t.Errorf("unexpected role: %s", cfg.Role)
			return 1, agent.TranscriptPaths{}, fmt.Errorf("unexpected role")
		}
	})

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeEscalated {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeEscalated, stderr.String())
	}
}

// ---------------------------------------------------------------------------
// Site 2 (MERGE_PR): Pre-approval rounds exhausted does not starve merge re-review
// ---------------------------------------------------------------------------

// TestConflictRetry_MergeTime_ReReviewIndependentOfMaxRounds verifies that when
// the pre-approval MaxReviewRounds was already exhausted before approval, the
// merge-time re-review is not blocked by the exhausted Round counter.
func TestConflictRetry_MergeTime_ReReviewIndependentOfMaxRounds(t *testing.T) { //nolint:cyclop,funlen
	var commentCalls []string
	exec := mergeTimeConflictExecutor(t, &commentCalls, 2)

	reviewerCallCount := 0
	r, logPath, stderr := setupPingPongRunner(t, exec)
	r.cfg.MaxConflictResolutionAttempts = 2
	// MaxReviewRounds=1 means the pre-approval budget is consumed after 1 reviewer turn.
	r.cfg.MaxReviewRounds = 1

	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		switch cfg.Role {
		case "dev":
			sendGMProjectCheck(cfg.Env) //nolint:errcheck
			sendGMDevDone(cfg.Env)      //nolint:errcheck
			return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
		case "reviewer":
			reviewerCallCount++
			// Pre-approval reviewer: approve (Round=1, MaxRounds=1 — budget exhausted but approved).
			writeReviewEvent(t, cfg.EventLogPath, "approved", "LGTM", cfg.Round, ciTestHeadSHA)
			return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
		default:
			t.Errorf("unexpected role: %s", cfg.Role)
			return 1, agent.TranscriptPaths{}, fmt.Errorf("unexpected role")
		}
	})

	outcome := runOrchestrate(t, r, logPath)
	// The merge-time re-review should complete successfully (approved by re-reviewer).
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeSuccess, stderr.String())
	}

	// Reviewer should be called twice: once pre-approval, once for merge re-review.
	if reviewerCallCount != 2 {
		t.Errorf("reviewer called %d times, want 2", reviewerCallCount)
	}
}

// ---------------------------------------------------------------------------
// loopdef: EventConflictUnresolved and EventConflictResolved transition coverage
// ---------------------------------------------------------------------------

// TestLoopDef_ConflictUnresolved_SyncCI_TerminatesDevFailed asserts the SYNC_CI +
// EventConflictUnresolved transition routes to TERMINAL_DEV_FAILED.
func TestLoopDef_ConflictUnresolved_SyncCI_TerminatesDevFailed(t *testing.T) {
	rc := &RunContext{
		MaxRounds:              5,
		MaxMergeReReviewRounds: 2,
	}
	transitions := loopTransitions()
	for _, tr := range transitions {
		if tr.From == loop.StepSyncCI && tr.Event == loop.EventConflictUnresolved {
			if tr.To != loop.StepTerminalDevFailed {
				t.Errorf("SYNC_CI + CONFLICT_UNRESOLVED → %s, want TERMINAL_DEV_FAILED", tr.To)
			}
			if tr.Guard != nil && !tr.Guard(rc) {
				t.Error("SYNC_CI + CONFLICT_UNRESOLVED transition guard must always match")
			}
			return
		}
	}
	t.Error("SYNC_CI + CONFLICT_UNRESOLVED transition not found in loopTransitions()")
}

// TestLoopDef_ConflictResolved_MergePR_RoutesToReviewer asserts the MERGE_PR +
// EventConflictResolved transition routes to RUN_REVIEWER.
func TestLoopDef_ConflictResolved_MergePR_RoutesToReviewer(t *testing.T) {
	rc := &RunContext{
		MaxRounds:              5,
		MaxMergeReReviewRounds: 2,
	}
	transitions := loopTransitions()
	for _, tr := range transitions {
		if tr.From == loop.StepMergePR && tr.Event == loop.EventConflictResolved {
			if tr.To != loop.StepRunReviewer {
				t.Errorf("MERGE_PR + CONFLICT_RESOLVED → %s, want RUN_REVIEWER", tr.To)
			}
			if tr.Guard != nil && !tr.Guard(rc) {
				t.Error("MERGE_PR + CONFLICT_RESOLVED transition guard must always match")
			}
			return
		}
	}
	t.Error("MERGE_PR + CONFLICT_RESOLVED transition not found in loopTransitions()")
}

// TestLoopDef_ConflictUnresolved_MergePR_TerminatesDevFailed asserts the MERGE_PR +
// EventConflictUnresolved transition routes to TERMINAL_DEV_FAILED.
func TestLoopDef_ConflictUnresolved_MergePR_TerminatesDevFailed(t *testing.T) {
	rc := &RunContext{
		MaxRounds:              5,
		MaxMergeReReviewRounds: 2,
	}
	transitions := loopTransitions()
	for _, tr := range transitions {
		if tr.From == loop.StepMergePR && tr.Event == loop.EventConflictUnresolved {
			if tr.To != loop.StepTerminalDevFailed {
				t.Errorf("MERGE_PR + CONFLICT_UNRESOLVED → %s, want TERMINAL_DEV_FAILED", tr.To)
			}
			if tr.Guard != nil && !tr.Guard(rc) {
				t.Error("MERGE_PR + CONFLICT_UNRESOLVED transition guard must always match")
			}
			return
		}
	}
	t.Error("MERGE_PR + CONFLICT_UNRESOLVED transition not found in loopTransitions()")
}

// TestLoopDef_MergeReReview_ChangesRequested_UsesSeperateBudget asserts that when
// InMergeReReview=true, EventChangesRequested routing uses MergeReReviewRound vs
// MaxMergeReReviewRounds rather than Round vs MaxRounds.
func TestLoopDef_MergeReReview_ChangesRequested_UsesSeperateBudget(t *testing.T) {
	transitions := loopTransitions()

	// Budget not exhausted: should route to RUN_DEV.
	rcUnderBudget := &RunContext{
		InMergeReReview:        true,
		MergeReReviewRound:     1,
		MaxMergeReReviewRounds: 2,
		Round:                  999, // exhausted pre-approval budget — must be ignored
		MaxRounds:              1,
	}
	// Budget exhausted: should route to TERMINAL_ESCALATED.
	rcOverBudget := &RunContext{
		InMergeReReview:        true,
		MergeReReviewRound:     2,
		MaxMergeReReviewRounds: 2,
		Round:                  0,
		MaxRounds:              5,
	}

	var underBudgetTarget, overBudgetTarget loop.StepKey
	for _, tr := range transitions {
		if tr.From != loop.StepRunReviewer || tr.Event != loop.EventChangesRequested {
			continue
		}
		if tr.Guard != nil && tr.Guard(rcUnderBudget) {
			underBudgetTarget = tr.To
		}
		if tr.Guard != nil && tr.Guard(rcOverBudget) {
			overBudgetTarget = tr.To
		}
	}

	if underBudgetTarget != loop.StepRunDev {
		t.Errorf("InMergeReReview + Round<budget → %s, want RUN_DEV", underBudgetTarget)
	}
	if overBudgetTarget != loop.StepTerminalEscalated {
		t.Errorf("InMergeReReview + Round>=budget → %s, want TERMINAL_ESCALATED", overBudgetTarget)
	}
}

// ---------------------------------------------------------------------------
// Config: MaxConflictResolutionAttempts default and validation
// ---------------------------------------------------------------------------

func TestConfig_MaxConflictResolutionAttempts_Default(t *testing.T) {
	cfg := config.DefaultConfig("p")
	if cfg.MaxConflictResolutionAttempts != 2 {
		t.Errorf("MaxConflictResolutionAttempts default: got %d, want 2", cfg.MaxConflictResolutionAttempts)
	}
}

// ---------------------------------------------------------------------------
// resolveRebaseConflictWithAgent: bounded retry preserves worktree between attempts
// ---------------------------------------------------------------------------

// TestConflictRetry_BoundedLoop_PreservesWorktreeBetweenAttempts verifies that when
// the first conflict-resolution attempt fails and the cap allows another, git rebase
// --abort is NOT called between attempts — only after the cap is exhausted.
func TestConflictRetry_BoundedLoop_PreservesWorktreeBetweenAttempts(t *testing.T) {
	r, logPath, eventsPtr := makeConflictRetryRunner(t, nil)
	r.cfg.MaxConflictResolutionAttempts = 2

	abortCalls := 0
	agentCalls := 0

	// Track abort calls.
	exec := buildConflictRebaseExec(t, conflictRebaseExecConfig{
		statusOnConflict:   "UU foo.go\n",
		conflictedFiles:    "foo.go\n",
		rebaseHeadError:    nil, // REBASE_HEAD always exists → all attempts unresolved
		statusAfterAgent:   "",
		ancestorAfterAgent: true,
	})
	innerRun := exec.runFunc
	exec.runFunc = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) >= 2 && args[0] == "rebase" && args[1] == "--abort" {
			abortCalls++
		}
		return innerRun(name, args...)
	}
	r.executor = exec

	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		agentCalls++
		return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
	})

	writer := &recordingWriter{events: eventsPtr}
	devWT := r.devWorktreePath()
	err := r.resolveRebaseConflictWithAgent(writer, devWT, 65, logPath)

	if err == nil {
		t.Error("expected error when cap exhausted, got nil")
	}
	if agentCalls != 2 {
		t.Errorf("agent called %d times, want 2 (cap=2)", agentCalls)
	}
	// Abort must be called exactly once — after the cap is exhausted, not between attempts.
	if abortCalls != 1 {
		t.Errorf("git rebase --abort called %d times, want 1 (only after cap exhausted)", abortCalls)
	}
	// The error must be errConflictCapExhausted.
	if !strings.Contains(err.Error(), "cap exhausted") {
		t.Errorf("error message must mention cap exhausted, got: %v", err)
	}
}

// TestConflictRetry_AutomergeConflictRetry_CarriesAttemptIndex verifies that the
// automerge_conflict_retry event payload carries the attempt index.
func TestConflictRetry_AutomergeConflictRetry_CarriesAttemptIndex(t *testing.T) {
	r, logPath, eventsPtr := makeConflictRetryRunner(t, nil)
	r.cfg.MaxConflictResolutionAttempts = 2

	exec := buildConflictRebaseExec(t, conflictRebaseExecConfig{
		statusOnConflict:   "UU foo.go\n",
		conflictedFiles:    "foo.go\n",
		rebaseHeadError:    nil, // always unresolved
		statusAfterAgent:   "",
		ancestorAfterAgent: true,
	})
	r.executor = exec

	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
	})

	writer := &recordingWriter{events: eventsPtr}
	devWT := r.devWorktreePath()
	_ = r.resolveRebaseConflictWithAgent(writer, devWT, 65, logPath)

	var attempts []int
	for _, ev := range *eventsPtr {
		if ev.Type != "automerge_conflict_retry" {
			continue
		}
		var p struct {
			Attempt int `json:"attempt"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err == nil {
			attempts = append(attempts, p.Attempt)
		}
	}
	if len(attempts) != 2 {
		t.Errorf("expected 2 automerge_conflict_retry events, got %d", len(attempts))
	}
	if len(attempts) >= 1 && attempts[0] != 1 {
		t.Errorf("first event attempt index: got %d, want 1", attempts[0])
	}
	if len(attempts) >= 2 && attempts[1] != 2 {
		t.Errorf("second event attempt index: got %d, want 2", attempts[1])
	}
}
