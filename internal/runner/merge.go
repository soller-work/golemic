package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"golemic/internal/eventlog"
	"golemic/internal/preflight"
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
		fmt.Fprintf(r.stderr, "Warning: failed to set confidence:low label: %v\n", err) //nolint:errcheck
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
		fmt.Fprintf(r.stderr, "Warning: failed to post merge failure comment: %v\n", err) //nolint:errcheck
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

// rebaseBranch fetches origin and rebases the dev worktree onto origin/main.
// On conflict it runs git rebase --abort. Returns the rebase outcome.
func (r *Runner) rebaseBranch(devWT string) error {
	if _, err := r.executor.RunInDir(devWT, "git", "fetch", "origin"); err != nil {
		return fmt.Errorf("fetch origin: %w", err)
	}
	if _, err := r.executor.RunInDir(devWT, "git", "rebase", "origin/main"); err != nil {
		// BR-005: abort and leave worktree in place
		_, _ = r.executor.RunInDir(devWT, "git", "rebase", "--abort")
		return fmt.Errorf("rebase conflict: %w", err)
	}
	return nil
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
		fmt.Fprintf(r.stderr, "Warning: remote branch delete failed: %v\n", err) //nolint:errcheck
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
		fmt.Fprintf(r.stderr, "Warning: remote branch delete failed: %v\n", err) //nolint:errcheck
	}
}

// runMergePhase implements PS-001 through PS-005 (gate → rebase → verify → merge → finalize).
// It is called by orchestrate() after the verdict is confirmed as "approved".
// Returns outcomeSuccess (merged or skipped) or outcomeMergeFailed.
func (r *Runner) runMergePhase(writer worktree.EventWriter, eventLogPath string) string { //nolint:cyclop
	prNumber, err := r.getPRNumber(eventLogPath)
	if err != nil {
		fmt.Fprintf(r.stderr, "merge_failed: failed to get PR number: %v\n", err) //nolint:errcheck
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

	// PS-002: Freshness check and rebase (BR-003)
	upToDate, err := r.isBranchUpToDate(devWT)
	if err != nil {
		return r.failMerge(writer, prNumber, fmt.Sprintf("freshness check failed: %v", err))
	}
	if upToDate {
		return r.doSquashMerge(writer, prNumber)
	}
	if err := r.rebaseBranch(devWT); err != nil {
		return r.failMerge(writer, prNumber, err.Error()) // BR-005
	}

	// PS-003: Post-rebase verification and push (BR-004)
	return r.verifyAndPush(writer, prNumber, devWT)
}

// verifyAndPush handles PS-003: verify the rebased branch and push.
func (r *Runner) verifyAndPush(writer worktree.EventWriter, prNumber int, devWT string) string { //nolint:cyclop
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
	fmt.Fprintf(r.stderr, "merge_failed: %s\n", reason) //nolint:errcheck
	r.postMergeFailureComment(prNumber, reason)
	r.writeAutomergeFailed(writer, reason)
	return outcomeMergeFailed
}

// doSquashMerge executes PS-004: squash-merge the PR with the reviewer token.
func (r *Runner) doSquashMerge(writer worktree.EventWriter, prNumber int) string {
	mergedSHA, err := r.squashMerge(prNumber)
	if err != nil {
		reason := err.Error()
		fmt.Fprintf(r.stderr, "merge_failed: %s\n", reason) //nolint:errcheck
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
