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
// (no cleanup is called — see BR-005).
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
	//    Uses git -C <wtPath> to target the worktree directory (not <repoRoot>).
	credHelper := "!f() { echo username=x-access-token; echo password=$GH_TOKEN; }; f"
	if _, err := executor.Run("git", "-C", wtPath, "config", "credential.helper", credHelper); err != nil {
		return fmt.Errorf("GIT_CONFIG_FAILED: credential.helper: %w", err)
	}
	if _, err := executor.Run("git", "-C", wtPath, "config", "user.name", botLogin); err != nil {
		return fmt.Errorf("GIT_CONFIG_FAILED: user.name: %w", err)
	}
	if _, err := executor.Run("git", "-C", wtPath, "config", "user.email", botLogin); err != nil {
		return fmt.Errorf("GIT_CONFIG_FAILED: user.email: %w", err)
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
func CreateForReviewer(repoRoot, golemicDir, runID string, issueNumber int, branchName, reviewerBotLogin string, executor preflight.Executor, eventWriter EventWriter, turnID int) error { //nolint:cyclop
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
	credHelper := "!f() { echo username=x-access-token; echo password=$GH_TOKEN; }; f"
	if _, err := executor.Run("git", "-C", wtPath, "config", "credential.helper", credHelper); err != nil {
		return fmt.Errorf("GIT_CONFIG_FAILED: credential.helper: %w", err)
	}
	if _, err := executor.Run("git", "-C", wtPath, "config", "user.name", reviewerBotLogin); err != nil {
		return fmt.Errorf("GIT_CONFIG_FAILED: user.name: %w", err)
	}
	if _, err := executor.Run("git", "-C", wtPath, "config", "user.email", reviewerBotLogin); err != nil {
		return fmt.Errorf("GIT_CONFIG_FAILED: user.email: %w", err)
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
// This is called only on success outcome (BR-004 / §2.11). On errors the
// partial worktree is left in place for debugging (BR-005).
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
