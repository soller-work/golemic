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

// riskLabelFromIssue selects the effective risk label by taking the most restrictive
// of all risk:* labels on the issue. Returns "" when no risk label is present.
// BR-002: missing treated as risk:high (handled by caller); most restrictive wins.
func riskLabelFromIssue(labels []issueLabel) string {
	hasHigh := false
	hasMedium := false
	hasLow := false
	for _, l := range labels {
		switch l.Name {
		case "risk:high":
			hasHigh = true
		case "risk:medium":
			hasMedium = true
		case "risk:low":
			hasLow = true
		}
	}
	if hasHigh {
		return "risk:high"
	}
	if hasMedium {
		return "risk:medium"
	}
	if hasLow {
		return "risk:low"
	}
	return ""
}

// evaluateAutoMergeGate applies DT-001 given the already-known verdict=approved.
// Returns (proceed bool, skipReason string).
// The reason is one of: "confidence low", "risk:high", "no risk label".
func (r *Runner) evaluateAutoMergeGate(eventLogPath string) (bool, string) {
	confidence, err := r.latestMergeConfidence(eventLogPath)
	if err != nil || confidence != "high" {
		return false, "confidence low"
	}

	riskLabel := riskLabelFromIssue(r.issue.Labels)
	switch riskLabel {
	case "risk:low", "risk:medium":
		return true, ""
	case "risk:high":
		return false, "risk:high"
	default:
		return false, "no risk label"
	}
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

// setConfidenceLowLabel adds the confidence:low label to the PR via the reviewer token.
// Errors are logged to stderr but do not change the outcome.
func (r *Runner) setConfidenceLowLabel(prNumber int) {
	_, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.ReviewerToken()},
		r.repoRoot,
		"gh", "pr", "edit", fmt.Sprintf("%d", prNumber), "--add-label", "confidence:low",
	)
	if err != nil {
		fmt.Fprintf(r.stderr, "Warning: failed to set confidence:low label: %v\n", err)
	}
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
// This means the branch already contains all commits from origin/main (BR-003).
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
// (any line with U in the X or Y position, or AA/DD). PS-001.
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
		// Check whether the failure is a merge conflict (BR-001, PS-001).
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
	binaryDir := filepath.Dir(golemicBinaryPath)
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")

	runFn := r.runAgentFn
	if runFn == nil {
		runFn = agent.RunRole
	}

	cfg := r.buildRebaseConflictAgentConfig(binaryDir, devWT, eventLogPath, userPrompt, golemicBinaryPath, runsDir)
	exitCode, _, agentErr := runFn(context.Background(), cfg)

	result, failReason := r.determineConflictResolutionResult(devWT, agentErr, exitCode)

	r.writeAutomergeConflictRetry(writer, conflictedFiles, result, r.turnCounter)

	if result == "resolved" {
		return nil
	}
	_, _ = r.executor.RunInDir(devWT, "git", "rebase", "--abort")
	return fmt.Errorf("%s", failReason)
}

// verifyRebaseComplete checks all four post-agent conditions required by BR-003:
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

// runVerifyCommand runs the configured verify_command in the dev worktree via sh -c so
// compound commands (&&, ;, pipes) work as expected.
func (r *Runner) runVerifyCommand(devWT string) error {
	if r.cfg.VerifyCommand == "" {
		return fmt.Errorf("verify_command is empty")
	}
	if _, err := r.executor.RunInDir(devWT, "sh", "-c", r.cfg.VerifyCommand); err != nil {
		return fmt.Errorf("verify_command failed: %w", err)
	}
	return nil
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

// squashMerge executes gh pr merge --squash with the reviewer token (BR-001).
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

// deleteRemoteBranch removes the remote branch after a successful squash-merge (BR-002).
// It is idempotent: if the branch is already gone, no push is issued (BR-003).
// Any error is logged as a warning; the run outcome is not changed (BR-004).
func (r *Runner) deleteRemoteBranch(branchName string) {
	out, err := r.executor.RunInDir(r.repoRoot, "git", "ls-remote", "--heads", "origin", branchName)
	if err != nil {
		fmt.Fprintf(r.stderr, "Warning: remote branch delete failed: %v\n", err)
		return
	}
	if strings.TrimSpace(out) == "" {
		return // already gone, nothing to do (BR-003)
	}
	_, err = r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		r.repoRoot,
		"git", "push", "origin", "--delete", branchName,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "remote ref does not exist") {
			return // branch already gone (TOCTOU race with GitHub auto-delete-head-branches)
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

// runMergePhase implements PS-001 through PS-006 (gate → fetch → freshness → CI gate / rebase → merge).
// It is called by orchestrate() after the verdict is confirmed as "approved".
// Returns outcomeSuccess (merged or skipped) or outcomeMergeFailed.
func (r *Runner) runMergePhase(writer worktree.EventWriter, eventLogPath string) string {
	prNumber, err := r.getPRNumber(eventLogPath)
	if err != nil {
		fmt.Fprintf(r.stderr, "merge_failed: failed to get PR number: %v\n", err)
		r.writeAutomergeFailed(writer, "PR number unavailable")
		return outcomeMergeFailed
	}

	// PS-001: Gate evaluation (BR-001, BR-002)
	proceed, skipReason := r.evaluateAutoMergeGate(eventLogPath)
	if !proceed {
		if skipReason == "confidence low" {
			r.setConfidenceLowLabel(prNumber)
		}
		r.writeAutomergeSkipped(writer, skipReason)
		return outcomeSuccess // BR-008: skip is a successful run
	}

	devWT := r.devWorktreePath()

	// PS-002: Fetch origin so isBranchUpToDate compares against a current ref (BR-001, BR-004)
	if _, err := r.executor.RunInDir(devWT, "git", "fetch", "origin"); err != nil {
		return r.failMerge(writer, prNumber, fmt.Sprintf("git fetch origin failed: %v", err))
	}

	// PS-003: Freshness check
	upToDate, err := r.isBranchUpToDate(devWT)
	if err != nil {
		return r.failMerge(writer, prNumber, fmt.Sprintf("freshness check failed: %v", err))
	}
	if upToDate {
		// PS-004: CI gate on up-to-date branch (BR-002, BR-003)
		return r.mergeIfCIGreen(writer, prNumber)
	}

	// PS-005: Rebase (and resolve conflicts if needed) then push (BR-005)
	if err := r.rebaseAndResolve(writer, devWT, prNumber, eventLogPath); err != nil {
		return r.failMerge(writer, prNumber, err.Error())
	}
	return r.verifyAndPush(writer, prNumber, devWT)
}

// mergeIfCIGreen runs the CI gate on an up-to-date branch (PS-004).
// doSquashMerge is only called when pollCIChecks returns green (BR-002).
// no_checks is treated as merge_failed rather than falling back to a local verify_command (BR-003).
func (r *Runner) mergeIfCIGreen(writer worktree.EventWriter, prNumber int) string {
	result, failedChecks, err := r.pollCIChecks(prNumber, r.ciTimeout())
	if err != nil {
		return r.failMerge(writer, prNumber, fmt.Sprintf("CI check query failed: %v", err))
	}
	switch result {
	case "green":
		return r.doSquashMerge(writer, prNumber)
	case "no_checks":
		return r.failMerge(writer, prNumber, "required check not reported for PR head")
	default:
		if len(failedChecks) > 0 {
			return r.failMerge(writer, prNumber, r.ciFailReason(result, failedChecks))
		}
		return r.failMerge(writer, prNumber, fmt.Sprintf("CI checks %s on up-to-date branch", result))
	}
}

// verifyAndPush handles PS-003: verify the rebased branch and push.
func (r *Runner) verifyAndPush(writer worktree.EventWriter, prNumber int, devWT string) string {
	ciTimeout := r.ciTimeout()
	result, _, err := r.queryCIChecks(prNumber)
	if err != nil {
		return r.failMerge(writer, prNumber, fmt.Sprintf("CI check query failed: %v", err))
	}

	if result == "no_checks" {
		// AC-008: local verification gates the merge when no CI is configured
		if err := r.runVerifyCommand(devWT); err != nil {
			return r.failMerge(writer, prNumber, err.Error())
		}
		if err := r.forcePushBranch(devWT); err != nil {
			return r.failMerge(writer, prNumber, err.Error())
		}
		return r.doSquashMerge(writer, prNumber)
	}

	// CI checks exist: push first, then wait for green
	if err := r.forcePushBranch(devWT); err != nil {
		return r.failMerge(writer, prNumber, err.Error())
	}
	result, failedChecks, err := r.pollCIChecks(prNumber, ciTimeout)
	if err != nil {
		return r.failMerge(writer, prNumber, fmt.Sprintf("CI poll failed: %v", err))
	}
	if result != "green" {
		return r.failMerge(writer, prNumber, r.ciFailReason(result, failedChecks))
	}
	return r.doSquashMerge(writer, prNumber)
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

// doSquashMerge executes PS-004: squash-merge the PR with the reviewer token.
func (r *Runner) doSquashMerge(writer worktree.EventWriter, prNumber int) string {
	mergedSHA, err := r.squashMerge(prNumber)
	if err != nil {
		reason := err.Error()
		fmt.Fprintf(r.stderr, "merge_failed: %s\n", reason)
		r.postMergeFailureComment(prNumber, reason)
		r.writeAutomergeFailed(writer, reason)
		return outcomeMergeFailed // BR-006
	}

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

	r.deleteRemoteBranch(r.branchName) // BR-002, BR-005: after pr_merged, before worktree cleanup

	return outcomeSuccess
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
func (r *Runner) buildRebaseConflictAgentConfig(binaryDir, devWT, eventLogPath, userPrompt, golemicBinaryPath string, runsDir string) agent.RoleConfig {
	return agent.RoleConfig{
		Role:              "dev",
		SystemPromptFile:  filepath.Join(binaryDir, "prompts", "dev.md"),
		UserPrompt:        userPrompt,
		WorktreeDir:       devWT,
		RunID:             r.runID,
		EventLogPath:      eventLogPath,
		TurnID:            r.turnCounter,
		GHToken:           r.creds.DevToken(),
		GolemicBinaryPath: golemicBinaryPath,
		Model:             r.cfg.Models.Dev,
		Timeout:           r.agentTimeout(),
		ToolAllowlist:     []string{"read", "bash", "write", "edit"},
		RunsDir:           runsDir,
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
