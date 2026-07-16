package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Collision describes one concrete collision found during checkAllCollisions.
type Collision struct {
	Message string // human-readable error with cleanup commands
}

// worktreeDir returns the expected path for the issue worktree.
func (r *Runner) worktreeDir() string {
	return filepath.Join(r.homeDir, ".golemic", r.project, "worktrees", fmt.Sprintf("issue-%d", r.issueNum))
}

// checkWorktreeCollision checks BR-004: worktree exists → abort.
func (r *Runner) checkWorktreeCollision() *Collision {
	wtDir := r.worktreeDir()
	if _, err := os.Stat(wtDir); err == nil {
		return &Collision{
			Message: fmt.Sprintf("Worktree exists at %s; remove with: git worktree remove %s", wtDir, wtDir),
		}
	}
	return nil
}

// checkBranchCollision checks BR-005: local or remote branch exists → abort.
// Returns error on git command failure (fail-closed per IC-002).
func (r *Runner) checkBranchCollision() (*Collision, error) {
	// Local branch check
	localOut, err := r.executor.Run("git", "branch", "--list", r.branchName)
	if err != nil {
		return nil, fmt.Errorf("failed to check git state: %w", err)
	}
	if strings.TrimSpace(localOut) != "" {
		return &Collision{
			Message: fmt.Sprintf("Branch %s exists locally; remove with: git branch -D %s", r.branchName, r.branchName),
		}, nil
	}

	// Remote branch check
	remoteOut, err := r.executor.Run("git", "ls-remote", "--heads", "origin", r.branchName)
	if err != nil {
		return nil, fmt.Errorf("failed to check git state: %w", err)
	}
	if strings.TrimSpace(remoteOut) != "" {
		return &Collision{
			Message: fmt.Sprintf("Branch %s exists on origin; remove with: git push origin --delete %s", r.branchName, r.branchName),
		}, nil
	}

	return nil, nil
}

// checkPRCollision checks BR-006: open PR with head branch exists → abort.
// Returns error on gh command or parse failure (fail-closed).
func (r *Runner) checkPRCollision() (*Collision, error) {
	out, err := r.executor.RunWithEnv(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		"gh", "pr", "list", "--head", r.branchName, "--json", "url,state",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to check PR state: %w", err)
	}

	var prs []struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return nil, fmt.Errorf("failed to check PR state: %w", err)
	}

	for _, pr := range prs {
		if pr.State == "OPEN" {
			return &Collision{
				Message: fmt.Sprintf("Open PR %s exists with head branch %s; close it first", pr.URL, r.branchName),
			}, nil
		}
	}
	return nil, nil
}

// checkAllCollisions runs all three collision checks in order and returns the first found.
// Order: worktree, local branch, remote branch, open PR (per DT-001).
// Returns error if any check fails (fail-closed).
func (r *Runner) checkAllCollisions() (*Collision, error) {
	// BR-004
	if c := r.checkWorktreeCollision(); c != nil {
		return c, nil
	}
	// BR-005
	c, err := r.checkBranchCollision()
	if err != nil {
		return nil, err
	}
	if c != nil {
		return c, nil
	}
	// BR-006
	c, err = r.checkPRCollision()
	if err != nil {
		return nil, err
	}
	if c != nil {
		return c, nil
	}
	return nil, nil
}
