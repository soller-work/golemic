// Package worktree provides isolated working directories for golemic roles.
//
// The Create function sets up a git worktree from origin/main with correct
// authentication (env-based credential helper) and bot identity
// (user.name/user.email). The Cleanup function removes the worktree and its
// local branch — called only on success outcome (not on errors).
//
// All git commands go through the injectable preflight.Executor interface.
package worktree

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"golemic/internal/eventlog"
	"golemic/internal/preflight"
)

// EventWriter is the subset of eventlog.Writer needed by this package.
// Defined as an interface so tests can use mocks without writing to disk.
// Real production code passes *eventlog.Writer which satisfies this interface.
type EventWriter interface {
	Write(event eventlog.Event) error
}

// worktreePath returns the absolute path for an issue worktree.
func worktreePath(golemicDir string, issueNumber int) string {
	return filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d", issueNumber))
}

// branchName returns the git branch name for an issue worktree.
func branchName(issueNumber int) string {
	return fmt.Sprintf("golemic/issue-%d", issueNumber)
}

// configureWorktreeGit sets credential.helper, user.name, and user.email in the
// worktree at wtPath via git -C <wtPath> config.
func configureWorktreeGit(executor preflight.Executor, wtPath, login string) error {
	credHelper := "!f() { echo username=x-access-token; echo password=$GH_TOKEN; }; f"
	if _, err := executor.Run("git", "-C", wtPath, "config", "credential.helper", credHelper); err != nil {
		return fmt.Errorf("GIT_CONFIG_FAILED: credential.helper: %w", err)
	}
	if _, err := executor.Run("git", "-C", wtPath, "config", "user.name", login); err != nil {
		return fmt.Errorf("GIT_CONFIG_FAILED: user.name: %w", err)
	}
	if _, err := executor.Run("git", "-C", wtPath, "config", "user.email", login); err != nil {
		return fmt.Errorf("GIT_CONFIG_FAILED: user.email: %w", err)
	}
	return nil
}

// Create sets up a dev worktree for the given issue.
//
// Steps:
//  1. Validate issueNumber > 0
//  2. git -C <repoRoot> fetch origin
//  3. git -C <repoRoot> rev-parse origin/main → baseSha
//  4. git -C <repoRoot> worktree add <path> -b golemic/issue-<N> origin/main
//  5. git config credential.helper, user.name, user.email in the worktree
//  6. write worktree_created event with role: dev
//
// If any step fails, the partial worktree is left in place for debugging
// (no cleanup is called).
func Create(repoRoot, golemicDir, runID string, issueNumber int, botLogin string, executor preflight.Executor, eventWriter EventWriter, turnID int) error {
	if issueNumber <= 0 {
		return fmt.Errorf("INVALID_ISSUE_NUMBER: %d", issueNumber)
	}
	// 2. git -C <repoRoot> fetch origin
	if _, err := executor.Run("git", "-C", repoRoot, "fetch", "origin"); err != nil {
		return fmt.Errorf("GIT_FETCH_FAILED: %w", err)
	}

	// 3. Capture baseSha from origin/main
	baseShaOut, err := executor.Run("git", "-C", repoRoot, "rev-parse", "origin/main")
	if err != nil {
		return fmt.Errorf("GIT_REV_PARSE_FAILED: %w", err)
	}
	baseSha := strings.TrimSpace(baseShaOut)

	// 4. git worktree add
	wtPath := worktreePath(golemicDir, issueNumber)
	branch := branchName(issueNumber)
	if _, err := executor.Run("git", "-C", repoRoot, "worktree", "add", wtPath, "-b", branch, "origin/main"); err != nil {
		return fmt.Errorf("GIT_WORKTREE_ADD_FAILED: %w", err)
	}

	// 5. Set worktree-local git config (env-based credential helper, bot identity)
	if err := configureWorktreeGit(executor, wtPath, botLogin); err != nil {
		return err
	}

	// 6. Write worktree_created event
	payload := map[string]string{
		"path":    wtPath,
		"branch":  branch,
		"baseSha": baseSha,
		"role":    "dev",
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("EVENT_MARSHAL_FAILED: %w", err)
	}

	event := eventlog.Event{
		Type:    eventlog.EventWorktreeCreated,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   runID,
		TurnID:  turnID,
		Payload: rawPayload,
	}
	if err := eventWriter.Write(event); err != nil {
		return fmt.Errorf("EVENT_WRITE_FAILED: %w", err)
	}

	return nil
}

// CreateForReviewer sets up a reviewer worktree for the given issue from the remote PR branch.
//
// Steps:
//  1. Validate issueNumber > 0
//  2. git -C <repoRoot> fetch origin
//  3. Verify remote branch exists via git rev-parse --verify origin/<branchName>
//  4. git -C <repoRoot> rev-parse origin/<branchName> → baseSha
//  5. git -C <repoRoot> worktree add <path> origin/<branchName> (detached HEAD)
//  6. git config credential.helper, user.name, user.email in the worktree
//  7. write worktree_created event with role: reviewer
//
// Returns REMOTE_BRANCH_NOT_FOUND if origin/<branchName> doesn't exist.
// If any other step fails, the partial worktree is left in place for debugging.
func CreateForReviewer(repoRoot, golemicDir, runID string, issueNumber int, branchName, reviewerBotLogin string, executor preflight.Executor, eventWriter EventWriter, turnID int) error {
	if issueNumber <= 0 {
		return fmt.Errorf("INVALID_ISSUE_NUMBER: %d", issueNumber)
	}
	// 2. git -C <repoRoot> fetch origin
	if _, err := executor.Run("git", "-C", repoRoot, "fetch", "origin"); err != nil {
		return fmt.Errorf("GIT_FETCH_FAILED: %w", err)
	}

	// 3. Verify the remote branch exists (for clear error messaging).
	remoteBranch := "origin/" + branchName
	if _, err := executor.Run("git", "-C", repoRoot, "rev-parse", "--verify", remoteBranch); err != nil {
		return fmt.Errorf("REMOTE_BRANCH_NOT_FOUND: Remote branch %s not found; was the branch pushed?", remoteBranch)
	}

	// 4. Capture baseSha from remote branch
	baseShaOut, err := executor.Run("git", "-C", repoRoot, "rev-parse", remoteBranch)
	if err != nil {
		return fmt.Errorf("GIT_REV_PARSE_FAILED: %w", err)
	}
	baseSha := strings.TrimSpace(baseShaOut)

	// 5. git worktree add with detached HEAD
	wtPath := reviewerWorktreePath(golemicDir, issueNumber)
	if _, err := executor.Run("git", "-C", repoRoot, "worktree", "add", wtPath, remoteBranch); err != nil {
		return fmt.Errorf("GIT_WORKTREE_ADD_FAILED: %w", err)
	}

	// 6. Set worktree-local git config (env-based credential helper, reviewer bot identity)
	if err := configureWorktreeGit(executor, wtPath, reviewerBotLogin); err != nil {
		return err
	}

	// 7. Write worktree_created event
	payload := map[string]string{
		"path":    wtPath,
		"branch":  branchName,
		"baseSha": baseSha,
		"role":    "reviewer",
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("EVENT_MARSHAL_FAILED: %w", err)
	}

	event := eventlog.Event{
		Type:    eventlog.EventWorktreeCreated,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   runID,
		TurnID:  turnID,
		Payload: rawPayload,
	}
	if err := eventWriter.Write(event); err != nil {
		return fmt.Errorf("EVENT_WRITE_FAILED: %w", err)
	}

	return nil
}

// EnsureForResume guarantees the dev worktree for a resumed run exists on branch
// golemic/issue-<N>. It is idempotent: an already-registered worktree on that branch
// is a no-op. A worktree registered on a different branch is a hard error (the caller
// must resolve it manually). Otherwise it recreates the worktree from the remote PR
// branch via createForResume.
//
// This is the single entry point for resume worktree handling so that all git
// plumbing (worktree registration inspection and creation) lives in this package.
func EnsureForResume(repoRoot, golemicDir, runID string, issueNumber int, botLogin string, executor preflight.Executor, eventWriter EventWriter, turnID int) error {
	if issueNumber <= 0 {
		return fmt.Errorf("INVALID_ISSUE_NUMBER: %d", issueNumber)
	}
	wtPath := worktreePath(golemicDir, issueNumber)
	expectedRef := "refs/heads/" + branchName(issueNumber)

	registered, curBranch, err := registeredWorktreeBranch(executor, repoRoot, wtPath)
	if err != nil {
		return fmt.Errorf("failed to inspect existing worktrees: %w", err)
	}
	if registered {
		if curBranch == expectedRef {
			return nil // idempotent no-op
		}
		return fmt.Errorf("dev worktree at %s is registered on branch %q, expected %q; resolve manually before resuming", wtPath, curBranch, expectedRef)
	}
	return createForResume(repoRoot, golemicDir, runID, issueNumber, botLogin, executor, eventWriter, turnID)
}

// registeredWorktreeBranch parses `git worktree list --porcelain` and reports whether
// wtPath is a registered git worktree and, if so, its checked-out branch ref
// (e.g. "refs/heads/golemic/issue-7"; empty for a detached HEAD).
func registeredWorktreeBranch(executor preflight.Executor, repoRoot, wtPath string) (bool, string, error) {
	out, err := executor.Run("git", "-C", repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return false, "", err
	}
	var curPath, curBranch string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			curPath = strings.TrimPrefix(line, "worktree ")
			curBranch = ""
		case strings.HasPrefix(line, "branch "):
			curBranch = strings.TrimPrefix(line, "branch ")
		case line == "":
			if curPath == wtPath {
				return true, curBranch, nil
			}
			curPath, curBranch = "", ""
		}
	}
	if curPath == wtPath {
		return true, curBranch, nil
	}
	return false, "", nil
}

// createForResume sets up the dev worktree for a resumed run from the remote PR
// branch. Unlike Create (which starts a fresh branch off origin/main and would
// discard the PR's commits) and CreateForReviewer (which uses a detached HEAD with
// the reviewer identity), createForResume checks out the existing remote branch
// origin/golemic/issue-<N> as a tracking local branch golemic/issue-<N> at the dev
// worktree path, with the dev bot identity, so the dev agent can commit and
// force-push during the resumed run.
//
// It uses `worktree add --track -B` (force create/reset) so an existing stale local
// branch left over from an interrupted run is reset to the remote head rather than
// causing a "branch already exists" failure.
//
// Steps:
//  1. git -C <repoRoot> fetch origin
//  2. git -C <repoRoot> ls-remote --exit-code --heads origin golemic/issue-<N>
//     (queries origin directly, avoiding a stale remote-tracking ref for a deleted
//     branch; REMOTE_BRANCH_NOT_FOUND otherwise) and capture its head SHA as baseSha
//  3. git -C <repoRoot> worktree add --track -B golemic/issue-<N> <path> origin/golemic/issue-<N>
//  4. git config credential.helper, user.name, user.email in the worktree
//  5. write worktree_created event with role: dev
func createForResume(repoRoot, golemicDir, runID string, issueNumber int, botLogin string, executor preflight.Executor, eventWriter EventWriter, turnID int) error {
	if _, err := executor.Run("git", "-C", repoRoot, "fetch", "origin"); err != nil {
		return fmt.Errorf("GIT_FETCH_FAILED: %w", err)
	}

	branch := branchName(issueNumber)
	lsOut, err := executor.Run("git", "-C", repoRoot, "ls-remote", "--exit-code", "--heads", "origin", branch)
	if err != nil {
		return fmt.Errorf("REMOTE_BRANCH_NOT_FOUND: Remote branch origin/%s not found; was the branch pushed?", branch)
	}
	fields := strings.Fields(lsOut)
	if len(fields) == 0 {
		return fmt.Errorf("REMOTE_BRANCH_NOT_FOUND: Remote branch origin/%s returned no ref", branch)
	}
	baseSha := fields[0]

	remoteBranch := "origin/" + branch
	wtPath := worktreePath(golemicDir, issueNumber)
	if _, err := executor.Run("git", "-C", repoRoot, "worktree", "add", "--track", "-B", branch, wtPath, remoteBranch); err != nil {
		return fmt.Errorf("GIT_WORKTREE_ADD_FAILED: %w", err)
	}

	if err := configureWorktreeGit(executor, wtPath, botLogin); err != nil {
		return err
	}

	payload := map[string]string{
		"path":    wtPath,
		"branch":  branch,
		"baseSha": baseSha,
		"role":    "dev",
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("EVENT_MARSHAL_FAILED: %w", err)
	}

	event := eventlog.Event{
		Type:    eventlog.EventWorktreeCreated,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   runID,
		TurnID:  turnID,
		Payload: rawPayload,
	}
	if err := eventWriter.Write(event); err != nil {
		return fmt.Errorf("EVENT_WRITE_FAILED: %w", err)
	}

	return nil
}

// IsDirty checks if the worktree at worktreePath has uncommitted changes
// by running git status --porcelain. Returns true if the output is non-empty
// (dirty), false if empty (clean).
func IsDirty(worktreePath string, executor preflight.Executor) (bool, error) {
	output, err := executor.Run("git", "-C", worktreePath, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("GIT_STATUS_FAILED: %w", err)
	}

	// Trim whitespace and check if output is empty
	if strings.TrimSpace(output) == "" {
		return false, nil
	}
	return true, nil
}

// reviewerWorktreePath returns the absolute path for a reviewer worktree.
func reviewerWorktreePath(golemicDir string, issueNumber int) string {
	return filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d-review", issueNumber))
}

// Cleanup removes the worktree and its local branch for the given issue.
//
// This is called only on success outcome. On errors the
// partial worktree is left in place for debugging.
func Cleanup(repoRoot, golemicDir string, issueNumber int, executor preflight.Executor) error {
	wtPath := worktreePath(golemicDir, issueNumber)
	branch := branchName(issueNumber)

	if _, err := executor.Run("git", "-C", repoRoot, "worktree", "remove", wtPath); err != nil {
		return fmt.Errorf("CLEANUP_REMOVE_FAILED: %w", err)
	}

	if _, err := executor.Run("git", "-C", repoRoot, "branch", "-D", branch); err != nil {
		return fmt.Errorf("CLEANUP_BRANCH_FAILED: %w", err)
	}

	return nil
}

// CleanupReviewer removes the reviewer worktree for the given issue.
// Unlike Cleanup, this does not delete a git branch (reviewer worktree uses detached HEAD).
// Called only on success outcome. On errors, the partial worktree is left in place for debugging.
func CleanupReviewer(repoRoot, golemicDir string, issueNumber int, executor preflight.Executor) error {
	wtPath := reviewerWorktreePath(golemicDir, issueNumber)
	if _, err := executor.Run("git", "-C", repoRoot, "worktree", "remove", wtPath); err != nil {
		return fmt.Errorf("CLEANUP_REMOVE_FAILED: %w", err)
	}
	return nil
}
