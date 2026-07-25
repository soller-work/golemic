package runner

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"golemic/internal/agent"
	"golemic/internal/preflight"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// stalePingPongExecutor wraps pingPongExecutor and makes the first
// git merge-base --is-ancestor call return exit code 1 (stale branch).
// Subsequent merge-base calls succeed so verifyRebaseComplete passes.
func stalePingPongExecutor(commentCalls *[]string) *fakeExecutor {
	base := pingPongExecutor(false, commentCalls)
	mergeBaseCallCount := 0

	innerRun := base.runFunc
	base.runFunc = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) >= 1 && args[0] == "merge-base" {
			mergeBaseCallCount++
			if mergeBaseCallCount == 1 {
				// First call: branch is behind origin/main.
				return "", &preflight.ErrExit{ExitCode: 1}
			}
			return "", nil // subsequent calls: up to date
		}
		// rebase is not in handleGitCmd; handle it here for sync-gate tests.
		if name == "git" && len(args) >= 1 && args[0] == "rebase" {
			return "", nil
		}
		if innerRun != nil {
			return innerRun(name, args...)
		}
		return "", fmt.Errorf("not mocked: %s %v", name, args)
	}

	innerRunWithEnv := base.runWithEnvFunc
	base.runWithEnvFunc = func(env map[string]string, name string, args ...string) (string, error) {
		if innerRunWithEnv != nil {
			return innerRunWithEnv(env, name, args...)
		}
		return "", fmt.Errorf("not mocked: %s %v", name, args)
	}
	return base
}

// stalePingPongExecutorWithPushFail returns a stale executor where
// git push --force-with-lease fails (but the initial dev push succeeds).
func stalePingPongExecutorWithPushFail(commentCalls *[]string) *fakeExecutor {
	base := stalePingPongExecutor(commentCalls)
	innerRunWithEnv := base.runWithEnvFunc
	base.runWithEnvFunc = func(env map[string]string, name string, args ...string) (string, error) {
		if name == "git" && containsArg(args, "--force-with-lease") {
			return "", fmt.Errorf("push rejected by remote")
		}
		return innerRunWithEnv(env, name, args...)
	}
	return base
}

// stalePingPongExecutorWithCIFail returns a stale executor where CI fails
// after push (check-runs for the pushed SHA are red).
func stalePingPongExecutorWithCIFail(commentCalls *[]string) *fakeExecutor {
	base := stalePingPongExecutor(commentCalls)
	innerRunWithEnv := base.runWithEnvFunc
	base.runWithEnvFunc = func(env map[string]string, name string, args ...string) (string, error) {
		if name == "gh" && len(args) >= 2 && args[0] == "api" && strings.Contains(args[1], "/check-runs") {
			return ghCheckRunsJSON([]ghCheckRunItem{
				{Name: "verify", Status: "completed", Conclusion: "failure"},
			}), nil
		}
		return innerRunWithEnv(env, name, args...)
	}
	return base
}

// stalePingPongExecutorWithConflict returns a stale executor where git rebase
// fails with conflict markers in git status.
func stalePingPongExecutorWithConflict(commentCalls *[]string) *fakeExecutor { //nolint:cyclop // simulating rebase conflict requires tracking multiple git subcommands
	base := stalePingPongExecutor(commentCalls)

	rebaseCalled := false
	innerRun := base.runFunc
	base.runFunc = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) >= 1 && args[0] == "rebase" && !rebaseCalled {
			rebaseCalled = true
			// Simulate rebase failure with conflict.
			return "", &preflight.ErrExit{ExitCode: 1}
		}
		if name == "git" && len(args) >= 1 && args[0] == "status" {
			// After rebase conflict: report unmerged paths.
			if rebaseCalled {
				return "UU internal/foo/bar.go\n", nil
			}
		}
		if name == "git" && len(args) >= 2 && args[0] == "diff" && args[1] == "--name-only" {
			return "internal/foo/bar.go\n", nil
		}
		return innerRun(name, args...)
	}
	return base
}

// firstReviewerWorktreeAddIndex returns the index of the first git worktree add
// for the reviewer worktree in the call log, or -1 if not found.
func firstReviewerWorktreeAddIndex(calls []callRecord, repoRoot string) int {
	for i, c := range calls {
		if isReviewerWorktreeAdd(c, repoRoot) {
			return i
		}
	}
	return -1
}

// forcePushIndex returns the index of the first git push --force-with-lease call, or -1.
func forcePushIndex(calls []callRecord) int {
	for i, c := range calls {
		if c.name == "git" && len(c.args) >= 1 && c.args[0] == "push" &&
			containsArg(c.args, "--force-with-lease") {
			return i
		}
	}
	return -1
}

func containsArg(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}

// rebaseIndex returns the index of the first git rebase origin/main call, or -1.
func rebaseIndex(calls []callRecord) int {
	for i, c := range calls {
		if c.name == "git" && len(c.args) >= 2 && c.args[0] == "rebase" && c.args[1] == "origin/main" {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Issue #205 regression tests: pre-review sync gate
// ---------------------------------------------------------------------------

// TestPreReviewSync_InitialPath_StaleBranch_SyncsBeforeReview verifies that when the
// PR branch is behind origin/main after the initial dev turn, the runner rebases, pushes,
// and waits for CI before creating the reviewer worktree.
func TestPreReviewSync_InitialPath_StaleBranch_SyncsBeforeReview(t *testing.T) { //nolint:cyclop // multiple ordering assertions require sequential index comparisons
	var commentCalls []string
	exec := stalePingPongExecutor(&commentCalls)

	r, logPath, _ := setupPingPongRunner(t, exec)
	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "approved", body: "LGTM", exitCode: 0},
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}

	// Assert that the sync (rebase + push) happened before reviewer worktree creation.
	rebaseIdx := rebaseIndex(exec.calls)
	pushIdx := forcePushIndex(exec.calls)
	reviewerAddIdx := firstReviewerWorktreeAddIndex(exec.calls, r.repoRoot)

	if rebaseIdx == -1 {
		t.Error("expected git rebase origin/main to be called for stale branch sync")
	}
	if pushIdx == -1 {
		t.Error("expected git push --force-with-lease to be called after sync")
	}
	if reviewerAddIdx == -1 {
		t.Error("expected reviewer worktree to be created after sync")
	}
	if rebaseIdx != -1 && pushIdx != -1 && rebaseIdx > pushIdx {
		t.Errorf("expected rebase before push: rebase=%d push=%d", rebaseIdx, pushIdx)
	}
	if pushIdx != -1 && reviewerAddIdx != -1 && pushIdx > reviewerAddIdx {
		t.Errorf("expected push before reviewer worktree creation: push=%d reviewerAdd=%d", pushIdx, reviewerAddIdx)
	}
}

// TestPreReviewSync_InitialPath_StaleBranch_ConflictFailsClosed verifies that when
// the pre-review rebase encounters a conflict and the dev agent cannot resolve it,
// the run fails closed without creating a reviewer worktree.
func TestPreReviewSync_InitialPath_StaleBranch_ConflictFailsClosed(t *testing.T) {
	var commentCalls []string
	exec := stalePingPongExecutorWithConflict(&commentCalls)

	r, logPath, stderr := setupPingPongRunner(t, exec)
	// dev agent exits 0 for initial dev, then exits 1 for conflict resolution.
	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "dev", exitCode: 1}, // conflict resolution agent fails
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeDevFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeDevFailed)
	}

	// No reviewer worktree should have been created.
	if firstReviewerWorktreeAddIndex(exec.calls, r.repoRoot) != -1 {
		t.Error("reviewer worktree must not be created when conflict resolution fails")
	}
	if !strings.Contains(stderr.String(), "pre-review sync") {
		t.Errorf("stderr should mention pre-review sync, got: %s", stderr.String())
	}
}

// TestPreReviewSync_InitialPath_StaleBranch_PushFailsClosed verifies that when the
// branch push fails after sync, the run fails closed without starting the reviewer.
func TestPreReviewSync_InitialPath_StaleBranch_PushFailsClosed(t *testing.T) {
	var commentCalls []string
	exec := stalePingPongExecutorWithPushFail(&commentCalls)

	r, logPath, stderr := setupPingPongRunner(t, exec)
	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeDevFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeDevFailed)
	}
	if firstReviewerWorktreeAddIndex(exec.calls, r.repoRoot) != -1 {
		t.Error("reviewer worktree must not be created when push fails")
	}
	if !strings.Contains(stderr.String(), "pre-review sync: push failed") {
		t.Errorf("stderr should mention pre-review sync push failure, got: %s", stderr.String())
	}
}

// TestPreReviewSync_InitialPath_StaleBranch_CIFailsClosed verifies that when CI is
// red for the pushed SHA, the run fails closed without starting the reviewer.
func TestPreReviewSync_InitialPath_StaleBranch_CIFailsClosed(t *testing.T) {
	var commentCalls []string
	exec := stalePingPongExecutorWithCIFail(&commentCalls)

	r, logPath, stderr := setupPingPongRunner(t, exec)
	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeDevFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeDevFailed)
	}
	if firstReviewerWorktreeAddIndex(exec.calls, r.repoRoot) != -1 {
		t.Error("reviewer worktree must not be created when CI fails after sync")
	}
	if !strings.Contains(stderr.String(), "pre-review sync") {
		t.Errorf("stderr should mention pre-review sync, got: %s", stderr.String())
	}
}

// TestPreReviewSync_ChangesRequestedRetry_StaleBranch verifies that after a
// changes_requested dev retry, the runner syncs with origin/main before starting
// the next reviewer round.
func TestPreReviewSync_ChangesRequestedRetry_StaleBranch(t *testing.T) { //nolint:cyclop,gocognit // closure tracking + ordering assertions; splitting obscures the test intent
	var commentCalls []string
	// Branch is up-to-date for the initial gate, then becomes stale after dev retry.
	exec := pingPongExecutor(false, &commentCalls)

	// Track merge-base calls: first call succeeds (initial pre-review gate),
	// second call returns stale (post changes-requested retry gate).
	mergeBaseCallCount := 0
	innerRun := exec.runFunc
	exec.runFunc = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) >= 1 && args[0] == "merge-base" {
			mergeBaseCallCount++
			if mergeBaseCallCount == 2 {
				// Second freshness check (after dev retry): branch is stale.
				return "", &preflight.ErrExit{ExitCode: 1}
			}
			return "", nil
		}
		// rebase is not in handleGitCmd; succeed for sync-gate rebase calls.
		if name == "git" && len(args) >= 1 && args[0] == "rebase" {
			return "", nil
		}
		return innerRun(name, args...)
	}

	r, logPath, _ := setupPingPongRunner(t, exec)
	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "changes_requested", body: "Fix the error handling", exitCode: 0},
		{role: "dev", exitCode: 0}, // dev retry
		{role: "reviewer", verdict: "approved", body: "LGTM", exitCode: 0},
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}

	// After the dev retry, a rebase + push must precede the second reviewer worktree.
	rebaseIdx := rebaseIndex(exec.calls)
	pushIdx := forcePushIndex(exec.calls)
	secondReviewerIdx := secondReviewerWorktreeAddIndex(exec.calls, r.repoRoot)

	if rebaseIdx == -1 {
		t.Error("expected git rebase origin/main for the post-retry sync")
	}
	if pushIdx == -1 {
		t.Error("expected git push --force-with-lease for the post-retry sync")
	}
	if secondReviewerIdx == -1 {
		t.Error("expected a second reviewer worktree to be created after sync")
	}
	if rebaseIdx != -1 && secondReviewerIdx != -1 && rebaseIdx > secondReviewerIdx {
		t.Errorf("expected rebase before second reviewer worktree: rebase=%d reviewer=%d", rebaseIdx, secondReviewerIdx)
	}
	if pushIdx != -1 && secondReviewerIdx != -1 && pushIdx > secondReviewerIdx {
		t.Errorf("expected push before second reviewer worktree: push=%d reviewer=%d", pushIdx, secondReviewerIdx)
	}
}

// TestPreReviewSync_ChangesRequestedRetry_SyncFailsClosed verifies that when the
// post-retry pre-review sync gate fails (e.g. CI red), the run stops and does not
// start another reviewer round.
func TestPreReviewSync_ChangesRequestedRetry_SyncFailsClosed(t *testing.T) { //nolint:cyclop,gocognit // closure tracking CI + merge-base state; splitting obscures the test intent
	var commentCalls []string
	exec := pingPongExecutor(false, &commentCalls)

	// Second merge-base check (post dev-retry): stale.
	// CI for the pushed SHA: red.
	mergeBaseCallCount := 0
	ciCheckCallCount := 0
	innerRun := exec.runFunc
	exec.runFunc = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) >= 1 && args[0] == "merge-base" {
			mergeBaseCallCount++
			if mergeBaseCallCount == 2 {
				return "", &preflight.ErrExit{ExitCode: 1}
			}
			return "", nil
		}
		// rebase is not in handleGitCmd; succeed for sync-gate rebase calls.
		if name == "git" && len(args) >= 1 && args[0] == "rebase" {
			return "", nil
		}
		return innerRun(name, args...)
	}
	innerRunWithEnv := exec.runWithEnvFunc
	exec.runWithEnvFunc = func(env map[string]string, name string, args ...string) (string, error) {
		if name == "gh" && len(args) >= 2 && args[0] == "api" && strings.Contains(args[1], "/check-runs") {
			ciCheckCallCount++
			if ciCheckCallCount >= 2 {
				// Second CI check (post-retry sync): red.
				return ghCheckRunsJSON([]ghCheckRunItem{
					{Name: "verify", Status: "completed", Conclusion: "failure"},
				}), nil
			}
		}
		return innerRunWithEnv(env, name, args...)
	}

	r, logPath, stderr := setupPingPongRunner(t, exec)
	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "changes_requested", body: "Fix it", exitCode: 0},
		{role: "dev", exitCode: 0}, // retry
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeDevFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeDevFailed)
	}
	if secondReviewerWorktreeAddIndex(exec.calls, r.repoRoot) != -1 {
		t.Error("second reviewer worktree must not be created when post-retry sync fails")
	}
	if !strings.Contains(stderr.String(), "pre-review sync") {
		t.Errorf("stderr should mention pre-review sync, got: %s", stderr.String())
	}
}

// TestPreReviewSync_ResumePath_ChangesRequested_StaleBranch verifies that the
// resume CHANGES_REQUESTED path also runs the pre-review sync gate before
// creating the reviewer worktree. The dev retry in the resume path should
// trigger the same rebase+push+CI sequence when the branch is behind origin/main.
func TestPreReviewSync_ResumePath_ChangesRequested_StaleBranch(t *testing.T) { //nolint:cyclop,funlen,gocognit // closure tracking + ordering assertions; splitting obscures the test intent
	reviews := []map[string]interface{}{
		{"id": 101, "state": "CHANGES_REQUESTED", "body": "Fix the typo in main.go", "user": map[string]interface{}{"login": "golemic-reviewer"}},
	}
	exec := baseResumeExecutor(7, "OPEN", nil, "golemic-reviewer", reviews, nil)

	// Override runFunc so that: (1) the first merge-base call is stale (triggers rebase),
	// and (2) git rebase succeeds (not in handleGitCmd by default).
	mergeBaseCallCount := 0
	innerRun := exec.runFunc
	exec.runFunc = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) >= 1 && args[0] == "merge-base" {
			mergeBaseCallCount++
			if mergeBaseCallCount == 1 {
				// Branch is stale before the reviewer starts.
				return "", &preflight.ErrExit{ExitCode: 1}
			}
			return "", nil
		}
		// rebase is not in handleGitCmd.
		if name == "git" && len(args) >= 1 && args[0] == "rebase" {
			return "", nil
		}
		return innerRun(name, args...)
	}

	r, logPath, stderr := setupResumeRunner(t, exec)
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		switch cfg.Role {
		case "dev":
			if !sendGMProjectCheck(cfg.Env) {
				t.Fatalf("sendGMProjectCheck failed")
			}
			if !sendGMDevDone(cfg.Env) {
				t.Fatalf("sendGMDevDone failed")
			}
			return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
		case "reviewer":
			writeReviewEvent(t, cfg.EventLogPath, "approved", "Looks good after sync", cfg.Round, ciTestHeadSHA)
			return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
		default:
			t.Errorf("unexpected role: %s", cfg.Role)
			return 1, agent.TranscriptPaths{}, fmt.Errorf("unexpected role")
		}
	})

	outcome := runResumeOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeSuccess, stderr.String())
	}

	// The pre-review sync gate must run (rebase) before the reviewer worktree is created.
	rebaseIdx := rebaseIndex(exec.calls)
	reviwerIdx := firstReviewerWorktreeAddIndex(exec.calls, r.repoRoot)

	if rebaseIdx == -1 {
		t.Error("expected git rebase for stale branch sync in resume path")
	}
	if reviwerIdx == -1 {
		t.Error("expected reviewer worktree to be created after sync in resume path")
	}
	if rebaseIdx != -1 && reviwerIdx != -1 && rebaseIdx > reviwerIdx {
		t.Errorf("expected rebase before reviewer worktree creation: rebase=%d reviewer=%d", rebaseIdx, reviwerIdx)
	}
	if mergeBaseCallCount == 0 {
		t.Error("expected at least one merge-base call for freshness check")
	}
	// Verify a force-push happened after the rebase (sync gate push).
	synPushFound := false
	for i, c := range exec.calls {
		if i > rebaseIdx && c.name == "git" && containsArg(c.args, "--force-with-lease") {
			synPushFound = true
			break
		}
	}
	if !synPushFound {
		t.Error("expected git push --force-with-lease after the sync gate rebase")
	}
}
