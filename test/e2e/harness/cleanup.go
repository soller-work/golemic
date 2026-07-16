package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// RemoveWorktrees removes all worktree directories from the golemic_e2e state
// directory. Safe to call multiple times (idempotent).
//
// golemicDir is the state directory (e.g. ~/.golemic/golemic_e2e/), which holds
// the worktrees/ subdirectory. repoRoot is the actual git repository root
// (e.g. ~/Dev/golemic_e2e). The distinction matters: git worktree commands must
// run against the git repo, not the state directory.
//
// Uses `git -C <repoRoot> worktree remove --force <path>` — matches production
// pattern in internal/worktree/worktree.go. Falls back to os.RemoveAll if git
// fails (e.g. repoRoot not available in unit tests).
func RemoveWorktrees(golemicDir, repoRoot string) error {
	worktreesDir := filepath.Join(golemicDir, "worktrees")

	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read worktrees directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		wtPath := filepath.Join(worktreesDir, entry.Name())

		// git -C <repoRoot> worktree remove --force <absolute-path>
		cmd := exec.Command("git", "-C", repoRoot, "worktree", "remove", "--force", wtPath)
		err := cmd.Run()

		// Fallback: raw delete when git fails (no git repo in unit tests, etc.).
		if err != nil {
			if errDel := os.RemoveAll(wtPath); errDel != nil {
				return fmt.Errorf("failed to remove worktree %s (git: %v, rm: %w)", wtPath, err, errDel)
			}
		}
	}

	return nil
}

// CleanupRuns removes all run directories from the golemic_e2e state directory.
// Safe to call multiple times (idempotent).
func CleanupRuns(golemicDir string) error {
	runsDir := filepath.Join(golemicDir, "runs")

	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read runs directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(runsDir, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("failed to remove run dir %s: %w", path, err)
		}
	}

	return nil
}

// RemoveWorktreeByIssue removes the dev and reviewer worktrees for a given issue
// number. Safe to call multiple times (idempotent).
//
// golemicDir is the state directory; repoRoot is the git repository root.
func RemoveWorktreeByIssue(golemicDir, repoRoot string, issueNum int) error {
	for _, name := range []string{
		fmt.Sprintf("issue-%d", issueNum),
		fmt.Sprintf("issue-%d-review", issueNum),
	} {
		wtPath := filepath.Join(golemicDir, "worktrees", name)

		cmd := exec.Command("git", "-C", repoRoot, "worktree", "remove", "--force", wtPath)
		err := cmd.Run()

		if err != nil {
			if errDel := os.RemoveAll(wtPath); errDel != nil {
				return fmt.Errorf("failed to remove worktree %s (git: %v, rm: %w)", wtPath, err, errDel)
			}
		}
	}
	return nil
}
