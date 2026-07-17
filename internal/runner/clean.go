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
func (r *Runner) cleanArtifacts() error { //nolint:gocognit,cyclop // five sequential removal steps each with presence check and error branch; extracting helpers adds no clarity
	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)

	// Dev worktree
	devWtPath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d", r.issueNum))
	if _, err := os.Stat(devWtPath); err == nil {
		if _, err := r.executor.RunInDir(r.repoRoot, "git", "worktree", "remove", devWtPath); err != nil {
			return fmt.Errorf("clean failed: could not remove dev worktree for issue %d: %w", r.issueNum, err)
		}
		fmt.Fprintf(r.stderr, "clean: removed dev worktree %s\n", devWtPath) //nolint:errcheck
	}

	// Reviewer worktree
	reviewerWtPath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d-review", r.issueNum))
	if _, err := os.Stat(reviewerWtPath); err == nil {
		if _, err := r.executor.RunInDir(r.repoRoot, "git", "worktree", "remove", reviewerWtPath); err != nil {
			return fmt.Errorf("clean failed: could not remove reviewer worktree for issue %d: %w", r.issueNum, err)
		}
		fmt.Fprintf(r.stderr, "clean: removed reviewer worktree %s\n", reviewerWtPath) //nolint:errcheck
	}

	// Local branch
	localOut, err := r.executor.RunInDir(r.repoRoot, "git", "branch", "--list", r.branchName)
	if err != nil {
		return fmt.Errorf("clean failed: could not check local branch for issue %d: %w", r.issueNum, err)
	}
	if strings.TrimSpace(localOut) != "" {
		if _, err := r.executor.RunInDir(r.repoRoot, "git", "branch", "-D", r.branchName); err != nil {
			return fmt.Errorf("clean failed: could not remove local branch %s for issue %d: %w", r.branchName, r.issueNum, err)
		}
		fmt.Fprintf(r.stderr, "clean: deleted local branch %s\n", r.branchName) //nolint:errcheck
	}

	// Remote branch
	remoteOut, err := r.executor.RunInDir(r.repoRoot, "git", "ls-remote", "--heads", "origin", r.branchName)
	if err != nil {
		return fmt.Errorf("clean failed: could not check remote branch for issue %d: %w", r.issueNum, err)
	}
	if strings.TrimSpace(remoteOut) != "" {
		if _, err := r.executor.RunInDir(r.repoRoot, "git", "push", "origin", "--delete", r.branchName); err != nil {
			return fmt.Errorf("clean failed: could not remove remote branch %s for issue %d: %w", r.branchName, r.issueNum, err)
		}
		fmt.Fprintf(r.stderr, "clean: deleted remote branch %s\n", r.branchName) //nolint:errcheck
	}

	// Open PR
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
		fmt.Fprintf(r.stderr, "clean: closed PR %s\n", pr.URL) //nolint:errcheck
	}

	return nil
}
