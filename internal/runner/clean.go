package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// cleanArtifacts removes all collision artifacts for the target issue before the
// collision check runs. Each artifact is skipped silently when absent (BR-003).
// Any removal failure aborts and returns a named error (BR-005).
//
// Order: dev worktree, reviewer worktree, local branch, remote branch, open PR.
func (r *Runner) cleanArtifacts() error {
	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)

	devWtPath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d", r.issueNum))
	if err := r.removeWorktreeIfExists(devWtPath, "dev"); err != nil {
		return err
	}

	reviewerWtPath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d-review", r.issueNum))
	if err := r.removeWorktreeIfExists(reviewerWtPath, "reviewer"); err != nil {
		return err
	}

	if err := r.removeLocalBranchIfExists(); err != nil {
		return err
	}

	if err := r.removeRemoteBranchIfExists(); err != nil {
		return err
	}

	return r.closeOpenPRs()
}

func (r *Runner) removeWorktreeIfExists(path, kind string) error {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	if _, err := r.executor.RunInDir(r.repoRoot, "git", "worktree", "remove", "--force", path); err != nil {
		return fmt.Errorf("clean failed: could not remove %s worktree %s for issue %d: %w", kind, path, r.issueNum, err)
	}
	fmt.Fprintf(r.stderr, "clean: removed %s worktree %s\n", kind, path)
	return nil
}

func (r *Runner) removeLocalBranchIfExists() error {
	localOut, err := r.executor.RunInDir(r.repoRoot, "git", "branch", "--list", r.branchName)
	if err != nil {
		return fmt.Errorf("clean failed: could not check local branch for issue %d: %w", r.issueNum, err)
	}
	if strings.TrimSpace(localOut) == "" {
		return nil
	}
	if _, err := r.executor.RunInDir(r.repoRoot, "git", "branch", "-D", r.branchName); err != nil {
		return fmt.Errorf("clean failed: could not remove local branch %s for issue %d: %w", r.branchName, r.issueNum, err)
	}
	fmt.Fprintf(r.stderr, "clean: deleted local branch %s\n", r.branchName)
	return nil
}

func (r *Runner) removeRemoteBranchIfExists() error {
	remoteOut, err := r.executor.RunInDir(r.repoRoot, "git", "ls-remote", "--heads", "origin", r.branchName)
	if err != nil {
		return fmt.Errorf("clean failed: could not check remote branch for issue %d: %w", r.issueNum, err)
	}
	if strings.TrimSpace(remoteOut) == "" {
		return nil
	}
	if _, err := r.executor.RunInDir(r.repoRoot, "git", "push", "origin", "--delete", r.branchName); err != nil {
		return fmt.Errorf("clean failed: could not remove remote branch %s for issue %d: %w", r.branchName, r.issueNum, err)
	}
	fmt.Fprintf(r.stderr, "clean: deleted remote branch %s\n", r.branchName)
	return nil
}

func (r *Runner) closeOpenPRs() error {
	prOut, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		r.repoRoot,
		"gh", "pr", "list", "--head", r.branchName, "--json", "url,state",
	)
	if err != nil {
		return fmt.Errorf("clean failed: could not list PRs for issue %d: %w", r.issueNum, err)
	}

	var prs []struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(prOut), &prs); err != nil {
		return fmt.Errorf("clean failed: could not parse PR list for issue %d: %w", r.issueNum, err)
	}

	for _, pr := range prs {
		if pr.State != "OPEN" {
			continue
		}
		if _, err := r.executor.RunWithEnvInDir(
			map[string]string{"GH_TOKEN": r.creds.DevToken()},
			r.repoRoot,
			"gh", "pr", "close", pr.URL,
		); err != nil {
			return fmt.Errorf("clean failed: could not remove PR %s for issue %d: %w", pr.URL, r.issueNum, err)
		}
		fmt.Fprintf(r.stderr, "clean: closed PR %s\n", pr.URL)
	}

	return nil
}
