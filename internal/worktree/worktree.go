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
//  1. git -C <repoRoot> fetch origin
//  2. git -C <repoRoot> rev-parse origin/main → baseSha
//  3. git -C <repoRoot> worktree add <path> -b golemic/issue-<N> origin/main
//  4. git config credential.helper, user.name, user.email in the worktree
//  5. write worktree_created event
//
// If any step fails, the partial worktree is left in place for debugging
// (no cleanup is called — see BR-005).
func Create(repoRoot, golemicDir, runID string, issueNumber int, botLogin string, executor preflight.Executor, eventWriter EventWriter) error {
	// 1. git -C <repoRoot> fetch origin
	if _, err := executor.Run("git", "-C", repoRoot, "fetch", "origin"); err != nil {
		return fmt.Errorf("GIT_FETCH_FAILED: %w", err)
	}

	// 2. Capture baseSha from origin/main
	baseShaOut, err := executor.Run("git", "-C", repoRoot, "rev-parse", "origin/main")
	if err != nil {
		return fmt.Errorf("GIT_REV_PARSE_FAILED: %w", err)
	}
	baseSha := strings.TrimSpace(baseShaOut)

	// 3. git worktree add
	wtPath := worktreePath(golemicDir, issueNumber)
	branch := branchName(issueNumber)
	if _, err := executor.Run("git", "-C", repoRoot, "worktree", "add", wtPath, "-b", branch, "origin/main"); err != nil {
		return fmt.Errorf("GIT_WORKTREE_ADD_FAILED: %w", err)
	}

	// 4. Set worktree-local git config (env-based credential helper, bot identity)
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

	// 5. Write worktree_created event
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
		Payload: rawPayload,
	}
	if err := eventWriter.Write(event); err != nil {
		return fmt.Errorf("EVENT_WRITE_FAILED: %w", err)
	}

	return nil
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