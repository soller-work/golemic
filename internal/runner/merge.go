package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golemic/internal/agent"
	"golemic/internal/eventlog"
	"golemic/internal/preflight"
	"golemic/internal/prompt"
	"golemic/internal/worktree"
)

// evaluateAutoMergeGate applies DT-001 given the already-known verdict=approved.
// Returns (proceed bool, skipReason string).
// Proceeds iff confidence != "low"; only skip reason is "confidence low".
func (r *Runner) evaluateAutoMergeGate(eventLogPath string) (bool, string) {
	confidence, err := r.latestConfidence(eventLogPath)
	if err != nil || confidence == "low" {
		return false, "confidence low"
	}
	return true, ""
}

// writeAutomergeSkipped appends an automerge_skipped event.
func (r *Runner) writeAutomergeSkipped(writer worktree.EventWriter, reason string) {
	payload, _ := json.Marshal(map[string]string{"reason": reason})
	_ = writer.Write(eventlog.Event{
		Type:    eventlog.EventAutomergeSkipped,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   r.runID,
		Payload: payload,
	})
}

// writeAutomergeFailed appends an automerge_failed event.
func (r *Runner) writeAutomergeFailed(writer worktree.EventWriter, reason string) {
	payload, _ := json.Marshal(map[string]string{"reason": reason})
	_ = writer.Write(eventlog.Event{
		Type:    eventlog.EventAutomergeFailed,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   r.runID,
		Payload: payload,
	})
}

// postMergeFailureComment posts a PR comment explaining the auto-merge failure.
// Errors are logged to stderr but do not change the outcome (SE-001).
func (r *Runner) postMergeFailureComment(prNumber int, reason string) {
	body := fmt.Sprintf(
		"golemic: auto-merge failed for issue #%d (PR #%d): %s. Human intervention required.",
		r.issueNum, prNumber, reason,
	)
	_, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.ReviewerToken()},
		r.repoRoot,
		"gh", "pr", "comment", fmt.Sprintf("%d", prNumber), "--body", body,
	)
	if err != nil {
		fmt.Fprintf(r.stderr, "Warning: failed to post merge failure comment: %v\n", err)
	}
}

// devWorktreePath returns the absolute path to the dev worktree for this run.
func (r *Runner) devWorktreePath() string {
	golemicDir := filepath.Join(r.homeDir, ".golemic", r.cfg.Project)
	return filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d", r.issueNum))
}

// isBranchUpToDate returns true when origin/main is an ancestor of HEAD in the dev worktree.
// This means the branch already contains all commits from origin/main.
func (r *Runner) isBranchUpToDate(devWT string) (bool, error) {
	_, err := r.executor.RunInDir(
		devWT,
		"git", "merge-base", "--is-ancestor", "origin/main", "HEAD",
	)
	if err != nil {
		var ee *preflight.ErrExit
		if errors.As(err, &ee) && ee.ExitCode == 1 {
			return false, nil // exit 1 = not an ancestor, expected
		}
		return false, err // exit 2+, bad revision, corrupt repo, etc.
	}
	return true, nil
}

// errMergeConflict is returned by rebaseBranch when the rebase fails with merge
// conflicts (U-status entries in git status --porcelain). The worktree is left
// in the conflicted state for the agent to resolve.
var errMergeConflict = errors.New("merge conflict")

// hasUnmergedPaths returns true if git status --porcelain reports any unmerged path
// (any line with U in the X or Y position, or AA/DD).
func (r *Runner) hasUnmergedPaths(devWT string) (bool, error) {
	out, err := r.executor.RunInDir(devWT, "git", "status", "--porcelain")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 2 {
			continue
		}
		x, y := line[0], line[1]
		if x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D') {
			return true, nil
		}
	}
	return false, nil
}

// rebaseBranch fetches origin and rebases the dev worktree onto origin/main.
// On merge conflict it returns errMergeConflict without aborting, leaving the
// worktree in the conflicted state for the agent. On other failures it aborts
// and returns the underlying error.
func (r *Runner) rebaseBranch(devWT string) error {
	if _, err := r.executor.RunInDir(devWT, "git", "fetch", "origin"); err != nil {
		return fmt.Errorf("fetch origin: %w", err)
	}
	if _, err := r.executor.RunInDir(devWT, "git", "rebase", "origin/main"); err != nil {
		// Check whether the failure is a merge conflict.
		isConflict, statusErr := r.hasUnmergedPaths(devWT)
		if statusErr == nil && isConflict {
			// Leave the worktree in the conflicted state for resolveRebaseConflictWithAgent.
			return errMergeConflict
		}
		// Generic rebase failure — abort and return.
		_, _ = r.executor.RunInDir(devWT, "git", "rebase", "--abort")
		return fmt.Errorf("rebase failed: %w", err)
	}
	return nil
}

// writeAutomergeConflictRetry appends an automerge_conflict_retry event. SE-001: write
// failure is warned to stderr; the merge phase continues regardless.
func (r *Runner) writeAutomergeConflictRetry(writer worktree.EventWriter, conflictedFiles []string, result string, turnID int) {
	payload, err := eventlog.MarshalAutomergeConflictRetryPayload(conflictedFiles, result, turnID)
	if err != nil {
		fmt.Fprintf(r.stderr, "Warning: automerge_conflict_retry marshal failed: %v\n", err)
		return
	}
	if err := writer.Write(eventlog.Event{
		Type:    eventlog.EventAutomergeConflictRetry,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   r.runID,
		TurnID:  turnID,
		Payload: payload,
	}); err != nil {
		fmt.Fprintf(r.stderr, "Warning: automerge_conflict_retry event write failed: %v\n", err)
	}
}

// agentTimeout returns the effective agent invocation timeout.
func (r *Runner) agentTimeout() time.Duration {
	if r.cfg.TimeoutSeconds > 0 {
		return time.Duration(r.cfg.TimeoutSeconds) * time.Second
	}
	return time.Duration(r.cfg.TimeoutMinutes) * time.Minute
}

// resolveRebaseConflictWithAgent invokes the dev agent once to resolve merge
// conflicts left after git rebase origin/main failed. IF-001.
//
// Returns nil when the rebase was fully resolved and the worktree is clean;
// the caller continues into verifyAndPush. Returns non-nil on any failure;
// the caller should invoke failMerge with the returned error message.
func (r *Runner) resolveRebaseConflictWithAgent(writer worktree.EventWriter, devWT string, prNumber int, eventLogPath string) error {
	conflictedFiles, err := r.collectConflictedFilesForRebase(devWT)
	if err != nil {
		return err
	}

	guidelinesPath := filepath.Join(r.repoRoot, ".golemic", "guidelines", "dev.md")
	userPrompt, err := prompt.RenderDevRebaseConflictResolve(
		prNumber,
		r.branchName,
		"origin/main",
		conflictedFiles,
		r.cfg.VerifyCommand,
		guidelinesPath,
	)
	if err != nil {
		_, _ = r.executor.RunInDir(devWT, "git", "rebase", "--abort")
		return fmt.Errorf("failed to render conflict resolve prompt: %w", err)
	}

	r.turnCounter++
	golemicBinaryPath, _ := os.Executable()
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")

	systemPromptFile, model, cleanupPrompt, resolveErr := r.resolveAgentFile("dev")
	if resolveErr != nil {
		_, _ = r.executor.RunInDir(devWT, "git", "rebase", "--abort")
		return fmt.Errorf("conflict retry: %w", resolveErr)
	}
	defer cleanupPrompt()

	runFn := r.runAgentFn
	if runFn == nil {
		runFn = agent.RunRole
	}

	cfg := r.buildRebaseConflictAgentConfig(systemPromptFile, model, devWT, eventLogPath, userPrompt, golemicBinaryPath, runsDir)
	exitCode, _, agentErr := runFn(context.Background(), cfg)

	result, failReason := r.determineConflictResolutionResult(devWT, agentErr, exitCode)

	r.writeAutomergeConflictRetry(writer, conflictedFiles, result, r.turnCounter)

	if result == "resolved" {
		return nil
	}
	_, _ = r.executor.RunInDir(devWT, "git", "rebase", "--abort")
	return fmt.Errorf("%s", failReason)
}

// verifyRebaseComplete checks all four post-agent conditions required for a clean rebase:
// agent exit 0, rebase not in progress, tree clean, and origin/main is an ancestor of HEAD.
// Returns (true, "") on success, or (false, reason) on failure.
func (r *Runner) verifyRebaseComplete(devWT string) (bool, string) {
	// Rebase-in-progress check: REBASE_HEAD ref must not exist.
	_, err := r.executor.RunInDir(devWT, "git", "rev-parse", "--verify", "REBASE_HEAD")
	if err == nil {
		return false, "rebase conflict: dev retry did not resolve: rebase not completed"
	}

	// Tree-clean check.
	out, err := r.executor.RunInDir(devWT, "git", "status", "--porcelain")
	if err != nil {
		return false, fmt.Sprintf("rebase conflict: dev retry did not resolve: git status check failed: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		return false, "rebase conflict: dev retry did not resolve: tree dirty after retry"
	}

	// Ancestor check: origin/main must be an ancestor of HEAD.
	_, err = r.executor.RunInDir(devWT, "git", "merge-base", "--is-ancestor", "origin/main", "HEAD")
	if err != nil {
		var ee *preflight.ErrExit
		if errors.As(err, &ee) && ee.ExitCode == 1 {
			return false, "rebase conflict: dev retry did not resolve: origin/main not ancestor after retry"
		}
		return false, fmt.Sprintf("rebase conflict: dev retry did not resolve: ancestor check failed: %v", err)
	}
	return true, ""
}

// parseConflictedFiles splits newline-separated git diff --name-only output into a []string,
// omitting empty lines.
func parseConflictedFiles(out string) []string {
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files
}

// forcePushBranch pushes the current branch with --force-with-lease using the dev token.
func (r *Runner) forcePushBranch(devWT string) error {
	_, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		devWT,
		"git", "push", "--force-with-lease",
	)
	if err != nil {
		return fmt.Errorf("force-with-lease push rejected: %w", err)
	}
	return nil
}

const maxOutOfDateRetries = 3

// isHeadBranchOutOfDate reports whether err is the GitHub "Head branch is out of date" rejection.
func isHeadBranchOutOfDate(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Head branch is out of date")
}

// writeAutomergeOutOfDateRetry appends an automerge_out_of_date_retry event.
func (r *Runner) writeAutomergeOutOfDateRetry(writer worktree.EventWriter, attempt int) {
	payload, _ := json.Marshal(map[string]interface{}{"attempt": attempt})
	if err := writer.Write(eventlog.Event{
		Type:    eventlog.EventAutomergeOutOfDateRetry,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   r.runID,
		Payload: payload,
	}); err != nil {
		fmt.Fprintf(r.stderr, "Warning: automerge_out_of_date_retry event write failed: %v\n", err)
	}
}

// squashMerge executes gh pr merge --squash with the reviewer token.
func (r *Runner) squashMerge(prNumber int) (string, error) {
	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.ReviewerToken()},
		r.repoRoot,
		"gh", "pr", "merge", fmt.Sprintf("%d", prNumber), "--squash",
	)
	if err != nil {
		return "", fmt.Errorf("gh pr merge failed: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// deleteRemoteBranch removes the remote branch after a successful squash-merge.
// It is idempotent: if the branch is already gone, no push is issued.
// Any error is logged as a warning; the run outcome is not changed.
func (r *Runner) deleteRemoteBranch(branchName string) {
	out, err := r.executor.RunInDir(r.repoRoot, "git", "ls-remote", "--heads", "origin", branchName)
	if err != nil {
		fmt.Fprintf(r.stderr, "Warning: remote branch delete failed: %v\n", err)
		return
	}
	if strings.TrimSpace(out) == "" {
		return // already gone, nothing to do
	}
	_, err = r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		r.repoRoot,
		"git", "push", "origin", "--delete", branchName,
	)
	if err != nil {
		// Re-check: GitHub's auto-delete may have won the race, leaving the ref
		// unresolvable mid-delete. Only warn when the branch genuinely persists.
		out, lsErr := r.executor.RunInDir(r.repoRoot, "git", "ls-remote", "--heads", "origin", branchName)
		if lsErr == nil && strings.TrimSpace(out) == "" {
			return // goal achieved: branch is gone
		}
		fmt.Fprintf(r.stderr, "Warning: remote branch delete failed: %v\n", err)
	}
}

// rebaseAndResolve rebases the dev worktree onto origin/main. On conflict it
// invokes the dev agent once; returns nil only when the worktree is clean and
// the rebase is complete.
func (r *Runner) rebaseAndResolve(writer worktree.EventWriter, devWT string, prNumber int, eventLogPath string) error {
	if err := r.rebaseBranch(devWT); err != nil {
		if !errors.Is(err, errMergeConflict) {
			return err
		}
		if resolveErr := r.resolveRebaseConflictWithAgent(writer, devWT, prNumber, eventLogPath); resolveErr != nil {
			return resolveErr
		}
	}
	return nil
}

// runPreReviewSyncGate ensures the dev branch is synchronized with origin/main and
// that required CI is green before any reviewer worktree is created. It is called
// after every successful dev turn and before every reviewer worktree creation, covering
// the initial dev path, changes_requested retry, and resume paths.
//
// If the branch is already up to date, it delegates to runCIGate (preserving CI fix
// retries). If the branch is stale or conflicting, it rebases via the dev role and
// then calls preReviewPushAndCI.
func (r *Runner) runPreReviewSyncGate(
	writer worktree.EventWriter,
	prNumber int,
	eventLogPath string,
	agentTimeout time.Duration,
) string {
	devWT := r.devWorktreePath()

	if _, err := r.executor.RunInDir(devWT, "git", "fetch", "origin"); err != nil {
		fmt.Fprintf(r.stderr, "dev_failed: pre-review sync: fetch origin: %v\n", err)
		return outcomeDevFailed
	}

	upToDate, err := r.isBranchUpToDate(devWT)
	if err != nil {
		fmt.Fprintf(r.stderr, "dev_failed: pre-review sync: freshness check: %v\n", err)
		return outcomeDevFailed
	}

	if upToDate {
		// Branch already contains origin/main; delegate to the full CI gate which
		// can retry CI failures with the dev agent.
		return r.runCIGate(prNumber, eventLogPath, agentTimeout)
	}

	// Branch is behind or conflicting: rebase and resolve through the dev role.
	if err := r.rebaseAndResolve(writer, devWT, prNumber, eventLogPath); err != nil {
		msg := fmt.Sprintf("pre-review sync: %v", err)
		fmt.Fprintf(r.stderr, "dev_failed: %s\n", msg)
		r.postCIEscalationComment(prNumber, msg)
		return outcomeDevFailed
	}
	return r.preReviewPushAndCI(prNumber, devWT)
}

// preReviewPushAndCI force-pushes the dev branch and waits for green CI on the pushed
// SHA. Called by runPreReviewSyncGate after a successful rebase.
func (r *Runner) preReviewPushAndCI(prNumber int, devWT string) string {
	if err := r.forcePushBranch(devWT); err != nil {
		msg := fmt.Sprintf("pre-review sync: push failed: %v", err)
		fmt.Fprintf(r.stderr, "dev_failed: %s\n", msg)
		r.postCIEscalationComment(prNumber, msg)
		return outcomeDevFailed
	}
	pushedSHA, err := r.getLocalHeadSHA(devWT)
	if err != nil {
		fmt.Fprintf(r.stderr, "dev_failed: pre-review sync: read pushed SHA: %v\n", err)
		return outcomeDevFailed
	}
	nwo, err := r.getRepoNWO()
	if err != nil {
		fmt.Fprintf(r.stderr, "dev_failed: pre-review sync: get repo NWO: %v\n", err)
		return outcomeDevFailed
	}
	result, failedChecks, err := r.pollCheckRunsForSHA(pushedSHA, nwo, r.ciTimeout())
	if err != nil {
		msg := fmt.Sprintf("pre-review sync: CI check query failed: %v", err)
		fmt.Fprintf(r.stderr, "dev_failed: %s\n", msg)
		r.postCIEscalationComment(prNumber, msg)
		return outcomeDevFailed
	}
	if result != "green" {
		msg := fmt.Sprintf("pre-review sync: CI %s", r.ciFailReason(result, failedChecks))
		fmt.Fprintf(r.stderr, "dev_failed: %s\n", msg)
		r.postCIEscalationComment(prNumber, msg)
		return outcomeDevFailed
	}
	return outcomeSuccess
}

// runMergePhase implements the merge phase (gate → fetch → freshness → CI gate / rebase → merge).
// It is called by orchestrate() after the verdict is confirmed as "approved".
// Returns outcomeSuccess (merged or skipped) or outcomeMergeFailed.
func (r *Runner) runMergePhase(writer worktree.EventWriter, eventLogPath string) string {
	prNumber, err := r.getPRNumber(eventLogPath)
	if err != nil {
		fmt.Fprintf(r.stderr, "merge_failed: failed to get PR number: %v\n", err)
		r.writeAutomergeFailed(writer, "PR number unavailable")
		return outcomeMergeFailed
	}

	// Gate evaluation
	proceed, skipReason := r.evaluateAutoMergeGate(eventLogPath)
	if !proceed {
		r.writeAutomergeSkipped(writer, skipReason)
		return outcomeSuccess // skip is a successful run
	}

	devWT := r.devWorktreePath()

	// Fetch origin so isBranchUpToDate compares against a current ref
	if _, err := r.executor.RunInDir(devWT, "git", "fetch", "origin"); err != nil {
		return r.failMerge(writer, prNumber, fmt.Sprintf("git fetch origin failed: %v", err))
	}

	// Freshness check
	upToDate, err := r.isBranchUpToDate(devWT)
	if err != nil {
		return r.failMerge(writer, prNumber, fmt.Sprintf("freshness check failed: %v", err))
	}
	if upToDate {
		// CI gate on up-to-date branch
		return r.mergeIfCIGreen(writer, prNumber, devWT)
	}

	// Rebase (and resolve conflicts if needed) then push
	if err := r.rebaseAndResolve(writer, devWT, prNumber, eventLogPath); err != nil {
		return r.failMerge(writer, prNumber, err.Error())
	}
	return r.verifyAndPush(writer, prNumber, devWT)
}

// mergeIfCIGreen runs the CI gate on an up-to-date branch.
// mergeWithOutOfDateRetry is only called when pollCIChecks returns green.
// no_checks is treated as merge_failed rather than falling back to a local verify_command.
func (r *Runner) mergeIfCIGreen(writer worktree.EventWriter, prNumber int, devWT string) string {
	result, failedChecks, err := r.pollCIChecks(prNumber, r.ciTimeout())
	if err != nil {
		return r.failMerge(writer, prNumber, fmt.Sprintf("CI check query failed: %v", err))
	}
	switch result {
	case "green":
		return r.mergeWithOutOfDateRetry(writer, prNumber, devWT)
	case "no_checks":
		return r.failMerge(writer, prNumber, "required check not reported for PR head")
	default:
		if len(failedChecks) > 0 {
			return r.failMerge(writer, prNumber, r.ciFailReason(result, failedChecks))
		}
		return r.failMerge(writer, prNumber, fmt.Sprintf("CI checks %s on up-to-date branch", result))
	}
}

// verifyAndPush verifies the rebased branch and pushes.
func (r *Runner) verifyAndPush(writer worktree.EventWriter, prNumber int, devWT string) string {
	ciTimeout := r.ciTimeout()

	// Push first, then wait for green using the exact pushed SHA to avoid trusting
	// stale completed checks from a superseded commit.
	if err := r.forcePushBranch(devWT); err != nil {
		return r.failMerge(writer, prNumber, err.Error())
	}
	pushedSHA, err := r.getLocalHeadSHA(devWT)
	if err != nil {
		return r.failMerge(writer, prNumber, fmt.Sprintf("failed to read pushed SHA: %v", err))
	}
	nwo, err := r.getRepoNWO()
	if err != nil {
		return r.failMerge(writer, prNumber, fmt.Sprintf("failed to get repo: %v", err))
	}
	result, failedChecks, err := r.pollCheckRunsForSHA(pushedSHA, nwo, ciTimeout)
	if err != nil {
		return r.failMerge(writer, prNumber, fmt.Sprintf("CI poll failed: %v", err))
	}
	if result != "green" {
		return r.failMerge(writer, prNumber, r.ciFailReason(result, failedChecks))
	}
	return r.mergeWithOutOfDateRetry(writer, prNumber, devWT)
}

// ciFailReason builds a human-readable failure reason from a CI poll result.
func (r *Runner) ciFailReason(result string, failedChecks []ghCheckItem) string {
	var names []string
	for _, c := range failedChecks {
		names = append(names, c.Name)
	}
	if len(names) > 0 {
		return fmt.Sprintf("CI checks failed: %s", strings.Join(names, ", "))
	}
	return fmt.Sprintf("CI checks %s after rebase push", result)
}

// failMerge records an automerge_failed event, posts a PR comment, and returns outcomeMergeFailed.
func (r *Runner) failMerge(writer worktree.EventWriter, prNumber int, reason string) string {
	fmt.Fprintf(r.stderr, "merge_failed: %s\n", reason)
	r.postMergeFailureComment(prNumber, reason)
	r.writeAutomergeFailed(writer, reason)
	return outcomeMergeFailed
}

// resyncForOODRetry re-syncs devWT onto origin/main, force-pushes, and waits for CI green.
// Returns empty string on success, or an outcomeMergeFailed string on failure.
func (r *Runner) resyncForOODRetry(writer worktree.EventWriter, prNumber int, devWT string, ciTimeout time.Duration) string {
	if err := r.rebaseBranch(devWT); err != nil {
		if errors.Is(err, errMergeConflict) {
			_, _ = r.executor.RunInDir(devWT, "git", "rebase", "--abort")
			return r.failMerge(writer, prNumber, "out-of-date retry: merge conflict during re-sync")
		}
		return r.failMerge(writer, prNumber, fmt.Sprintf("out-of-date retry: rebase: %v", err))
	}
	if err := r.forcePushBranch(devWT); err != nil {
		return r.failMerge(writer, prNumber, fmt.Sprintf("out-of-date retry: %v", err))
	}
	pushedSHA, err := r.getLocalHeadSHA(devWT)
	if err != nil {
		return r.failMerge(writer, prNumber, fmt.Sprintf("out-of-date retry: read SHA: %v", err))
	}
	nwo, err := r.getRepoNWO()
	if err != nil {
		return r.failMerge(writer, prNumber, fmt.Sprintf("out-of-date retry: get repo: %v", err))
	}
	ciResult, failedChecks, err := r.pollCheckRunsForSHA(pushedSHA, nwo, ciTimeout)
	if err != nil {
		return r.failMerge(writer, prNumber, fmt.Sprintf("out-of-date retry: CI poll: %v", err))
	}
	if ciResult != "green" {
		return r.failMerge(writer, prNumber, r.ciFailReason(ciResult, failedChecks))
	}
	return ""
}

// mergeWithOutOfDateRetry squash-merges the PR, automatically re-syncing and retrying
// when GitHub rejects with "Head branch is out of date" (bounded by maxOutOfDateRetries).
// Non-OOD failures and an exhausted retry budget both call failMerge as today.
func (r *Runner) mergeWithOutOfDateRetry(writer worktree.EventWriter, prNumber int, devWT string) string {
	ciTimeout := r.ciTimeout()
	for attempt := 1; attempt <= maxOutOfDateRetries; attempt++ {
		mergedSHA, mergeErr := r.squashMerge(prNumber)
		if mergeErr == nil {
			payload, _ := json.Marshal(map[string]interface{}{
				"prNumber":  prNumber,
				"mergedSHA": mergedSHA,
			})
			_ = writer.Write(eventlog.Event{
				Type:    eventlog.EventPRMerged,
				Ts:      time.Now().Format(time.RFC3339),
				RunID:   r.runID,
				Payload: payload,
			})
			r.deleteRemoteBranch(r.branchName)
			return outcomeSuccess
		}
		if !isHeadBranchOutOfDate(mergeErr) {
			return r.failMerge(writer, prNumber, mergeErr.Error())
		}
		if attempt == maxOutOfDateRetries {
			break
		}
		// Re-sync: rebase onto the new origin/main, push, wait for green CI.
		r.writeAutomergeOutOfDateRetry(writer, attempt)
		if out := r.resyncForOODRetry(writer, prNumber, devWT, ciTimeout); out != "" {
			return out
		}
	}
	return r.failMerge(writer, prNumber, "out-of-date retry: branch is still out of date after max attempts")
}

// collectConflictedFilesForRebase retrieves the list of conflicted files from git.
func (r *Runner) collectConflictedFilesForRebase(devWT string) ([]string, error) {
	conflictOut, err := r.executor.RunInDir(devWT, "git", "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		_, _ = r.executor.RunInDir(devWT, "git", "rebase", "--abort")
		return nil, fmt.Errorf("failed to enumerate conflicted files: %w", err)
	}
	return parseConflictedFiles(conflictOut), nil
}

// buildRebaseConflictAgentConfig creates the agent.RoleConfig for rebase conflict resolution.
func (r *Runner) buildRebaseConflictAgentConfig(systemPromptFile, model, devWT, eventLogPath, userPrompt, golemicBinaryPath string, runsDir string) agent.RoleConfig {
	_ = golemicBinaryPath
	return agent.RoleConfig{
		Role:             "dev",
		SystemPromptFile: systemPromptFile,
		UserPrompt:       userPrompt,
		WorktreeDir:      devWT,
		RunID:            r.runID,
		EventLogPath:     eventLogPath,
		TurnID:           r.turnCounter,
		Model:            model,
		Timeout:          r.agentTimeout(),
		IdleTimeout:      time.Duration(r.cfg.AgentIdleTimeoutMinutes) * time.Minute,
		ToolAllowlist:    []string{"read", "bash", "write", "edit"},
		RunsDir:          runsDir,
	}
}

// determineConflictResolutionResult evaluates agent outcome and returns result status and reason.
func (r *Runner) determineConflictResolutionResult(devWT string, agentErr error, exitCode int) (string, string) {
	result := "unresolved"
	failReason := "rebase conflict: dev retry did not resolve"

	if agentErr != nil {
		if errors.Is(agentErr, agent.ErrTimeout) {
			failReason = "rebase conflict: agent timeout during conflict resolution"
		}
	} else if exitCode == 0 {
		if ok, reason := r.verifyRebaseComplete(devWT); ok {
			result = "resolved"
		} else {
			failReason = reason
		}
	}
	return result, failReason
}
