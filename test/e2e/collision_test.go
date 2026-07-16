package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ghhelper "golemic/test/e2e/github"
	"golemic/test/e2e/harness"
)

// TestCollision verifies that golemic aborts (exit 1) with a cleanup command
// when stale artifacts exist before the collision check (PS-005).
//
// AC→test mapping:
//
//	AC-001 (worktree)       → TestCollision/worktree      — "remove with: git worktree remove"
//	AC-002 (local branch)   → TestCollision/localBranch   — "git branch -D"
//	AC-003 (remote branch)  → TestCollision/remoteBranch  — "git push origin --delete"
//	AC-004 (open PR)        → TestCollision/openPR        — "close it first"
//
// AC-004 ordering note: checkAllCollisions checks remote branch BEFORE PR. To isolate
// the PR check, we push the branch, create a PR, then delete the remote branch. GitHub
// retains the head-branch association on the PR, so gh pr list --head <branch> still
// returns the open PR. If GitHub behaviour changes (PR no longer listed after head
// deletion), the openPR subtest will fail with a diagnostic message.
func TestCollision(t *testing.T) {
	requireGh(t)

	e2ePath := findE2EPath()
	if e2ePath == "" {
		t.Skip("golemic_e2e sandbox not found — skipping E2E collision test (set GOLEMIC_E2E_PATH to override)")
	}

	binary := findBinary()
	if binary == "" {
		t.Skip("golemic binary not found — skipping E2E collision test (run `go build ./cmd/golemic` or set GOLEMIC_BINARY)")
	}

	runner, err := harness.NewRunner(e2ePath, binary)
	if err != nil {
		t.Skipf("E2E sandbox not ready (config/credentials missing): %v", err)
	}

	repo := e2eRepo(t)
	project := runner.Config().Project
	golemicDir := filepath.Join(runner.HomeDir(), ".golemic", project)

	if out, err := exec.Command("gh", "repo", "view", repo, "--json", "name").CombinedOutput(); err != nil {
		t.Skipf("golemic_e2e GitHub repo %q not accessible — skipping E2E collision test: %v\n%s", repo, err, out)
	}

	// AC-001: stale worktree directory causes exit 1 with worktree cleanup command.
	t.Run("worktree", func(t *testing.T) {
		title := fmt.Sprintf("E2E Collision Worktree %d", time.Now().UnixNano())
		issueNum, err := ghhelper.CreateTestIssue(repo, title, "Automated E2E collision test — delete after run.")
		if err != nil {
			t.Fatalf("CreateTestIssue: %v", err)
		}
		defer func() {
			if err := ghhelper.DeleteTestIssue(repo, issueNum); err != nil {
				t.Logf("cleanup: DeleteTestIssue %d: %v", issueNum, err)
			}
		}()
		defer func() {
			if err := harness.RemoveWorktreeByIssue(golemicDir, e2ePath, issueNum); err != nil {
				t.Logf("cleanup: RemoveWorktreeByIssue %d: %v", issueNum, err)
			}
			if err := harness.CleanupRuns(golemicDir); err != nil {
				t.Logf("cleanup: CleanupRuns: %v", err)
			}
		}()

		// checkWorktreeCollision uses os.Stat; a plain directory is sufficient.
		wtDir := filepath.Join(runner.HomeDir(), ".golemic", project, "worktrees", fmt.Sprintf("issue-%d", issueNum))
		if err := os.MkdirAll(wtDir, 0755); err != nil {
			t.Fatalf("MkdirAll worktree dir: %v", err)
		}

		result, err := runner.Exec(context.Background(), "run", "--issue", fmt.Sprintf("%d", issueNum))
		if err != nil {
			t.Fatalf("runner.Exec: %v", err)
		}

		if result.ExitCode != 1 {
			t.Errorf("AC-001: want exit 1, got %d\nstdout: %s\nstderr: %s", result.ExitCode, result.Stdout, result.Stderr)
		}
		if !strings.Contains(result.Stderr, "remove with: git worktree remove") {
			t.Errorf("AC-001 BR-002: want cleanup hint 'remove with: git worktree remove' in stderr, got:\n%s", result.Stderr)
		}
	})

	// AC-002: local branch golemic/issue-N causes exit 1 with branch delete command.
	// No worktree must exist so the worktree check (higher precedence) does not fire first.
	t.Run("localBranch", func(t *testing.T) {
		title := fmt.Sprintf("E2E Collision LocalBranch %d", time.Now().UnixNano())
		issueNum, err := ghhelper.CreateTestIssue(repo, title, "Automated E2E collision test — delete after run.")
		if err != nil {
			t.Fatalf("CreateTestIssue: %v", err)
		}
		branchName := fmt.Sprintf("golemic/issue-%d", issueNum)

		defer func() {
			if err := ghhelper.DeleteTestIssue(repo, issueNum); err != nil {
				t.Logf("cleanup: DeleteTestIssue %d: %v", issueNum, err)
			}
		}()
		defer func() {
			cmd := exec.Command("git", "branch", "-D", branchName)
			cmd.Dir = e2ePath
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Logf("cleanup: git branch -D %s: %v\n%s", branchName, err, out)
			}
			if err := harness.CleanupRuns(golemicDir); err != nil {
				t.Logf("cleanup: CleanupRuns: %v", err)
			}
		}()

		// Create a local branch pointing at HEAD; no worktree, no remote branch.
		cmd := exec.Command("git", "branch", branchName)
		cmd.Dir = e2ePath
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git branch %s: %v\n%s", branchName, err, out)
		}

		result, err := runner.Exec(context.Background(), "run", "--issue", fmt.Sprintf("%d", issueNum))
		if err != nil {
			t.Fatalf("runner.Exec: %v", err)
		}

		if result.ExitCode != 1 {
			t.Errorf("AC-002: want exit 1, got %d\nstdout: %s\nstderr: %s", result.ExitCode, result.Stdout, result.Stderr)
		}
		if !strings.Contains(result.Stderr, "git branch -D") {
			t.Errorf("AC-002 BR-002: want cleanup hint 'git branch -D' in stderr, got:\n%s", result.Stderr)
		}
	})

	// AC-003: remote branch golemic/issue-N causes exit 1 with push --delete command.
	// No worktree, no local branch of that name (push uses HEAD:refs/heads/...).
	t.Run("remoteBranch", func(t *testing.T) {
		title := fmt.Sprintf("E2E Collision RemoteBranch %d", time.Now().UnixNano())
		issueNum, err := ghhelper.CreateTestIssue(repo, title, "Automated E2E collision test — delete after run.")
		if err != nil {
			t.Fatalf("CreateTestIssue: %v", err)
		}
		branchName := fmt.Sprintf("golemic/issue-%d", issueNum)

		defer func() {
			if err := ghhelper.DeleteTestIssue(repo, issueNum); err != nil {
				t.Logf("cleanup: DeleteTestIssue %d: %v", issueNum, err)
			}
		}()
		defer func() {
			deleteRemoteBranch(t, e2ePath, branchName)
			// Prune stale remote-tracking refs.
			prune := exec.Command("git", "fetch", "--prune", "origin")
			prune.Dir = e2ePath
			if out, err := prune.CombinedOutput(); err != nil {
				t.Logf("cleanup: git fetch --prune: %v\n%s", err, out)
			}
			if err := harness.CleanupRuns(golemicDir); err != nil {
				t.Logf("cleanup: CleanupRuns: %v", err)
			}
		}()

		// Push HEAD to origin as golemic/issue-N without creating a local branch.
		// git branch --list golemic/issue-N will return empty (local check passes);
		// git ls-remote --heads origin golemic/issue-N will find it (remote check fires).
		pushCmd := exec.Command("git", "push", "origin", fmt.Sprintf("HEAD:refs/heads/%s", branchName))
		pushCmd.Dir = e2ePath
		if out, err := pushCmd.CombinedOutput(); err != nil {
			t.Fatalf("git push origin HEAD:refs/heads/%s: %v\n%s", branchName, err, out)
		}

		result, err := runner.Exec(context.Background(), "run", "--issue", fmt.Sprintf("%d", issueNum))
		if err != nil {
			t.Fatalf("runner.Exec: %v", err)
		}

		if result.ExitCode != 1 {
			t.Errorf("AC-003: want exit 1, got %d\nstdout: %s\nstderr: %s", result.ExitCode, result.Stdout, result.Stderr)
		}
		if !strings.Contains(result.Stderr, "git push origin --delete") {
			t.Errorf("AC-003 BR-002: want cleanup hint 'git push origin --delete' in stderr, got:\n%s", result.Stderr)
		}
	})

	// AC-004: open PR with head golemic/issue-N causes exit 1 with "close it first".
	//
	// Isolation strategy: push branch → create PR → delete remote branch.
	// After deletion, git ls-remote --heads finds nothing (remote-branch check passes),
	// but gh pr list --head <branch> still returns the open PR (GitHub retains the
	// head-branch name on the PR record after branch deletion). The PR check then fires.
	//
	// If GitHub's behaviour changes and pr list no longer returns the PR after branch
	// deletion, the runner will exit 0 and the assertion below will fail with a
	// diagnostic explaining the limitation (AC-004 PR-specific isolation is not
	// achievable without modifying the forbidden collision order).
	t.Run("openPR", func(t *testing.T) {
		title := fmt.Sprintf("E2E Collision OpenPR %d", time.Now().UnixNano())
		issueNum, err := ghhelper.CreateTestIssue(repo, title, "Automated E2E collision test — delete after run.")
		if err != nil {
			t.Fatalf("CreateTestIssue: %v", err)
		}
		branchName := fmt.Sprintf("golemic/issue-%d", issueNum)
		var prNum int

		defer func() {
			if err := ghhelper.DeleteTestIssue(repo, issueNum); err != nil {
				t.Logf("cleanup: DeleteTestIssue %d: %v", issueNum, err)
			}
		}()
		defer func() {
			if prNum > 0 {
				if err := ghhelper.CloseTestPR(repo, prNum); err != nil {
					t.Logf("cleanup: CloseTestPR %d: %v", prNum, err)
				}
			}
			deleteRemoteBranch(t, e2ePath, branchName)
			if err := harness.CleanupRuns(golemicDir); err != nil {
				t.Logf("cleanup: CleanupRuns: %v", err)
			}
		}()

		// Step 1: push branch to origin.
		pushCmd := exec.Command("git", "push", "origin", fmt.Sprintf("HEAD:refs/heads/%s", branchName))
		pushCmd.Dir = e2ePath
		if out, err := pushCmd.CombinedOutput(); err != nil {
			t.Fatalf("git push origin HEAD:refs/heads/%s: %v\n%s", branchName, err, out)
		}

		// Step 2: create PR with head golemic/issue-N.
		prOut, err := exec.Command("gh", "pr", "create",
			"--repo", repo,
			"--head", branchName,
			"--base", "main",
			"--title", fmt.Sprintf("E2E Collision Test PR for issue #%d", issueNum),
			"--body", "Automated E2E collision test PR — close after run.",
		).Output()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				t.Fatalf("gh pr create: %v\nstderr: %s", err, exitErr.Stderr)
			}
			t.Fatalf("gh pr create: %v", err)
		}
		prURL := strings.TrimSpace(string(prOut))
		if idx := strings.LastIndex(prURL, "/"); idx >= 0 {
			var n int
			if _, scanErr := fmt.Sscanf(prURL[idx+1:], "%d", &n); scanErr == nil && n > 0 {
				prNum = n
			}
		}

		// Step 3: delete remote branch so git ls-remote finds nothing.
		// The open PR record on GitHub still references the head branch name.
		deleteRemoteBranch(t, e2ePath, branchName)

		result, err := runner.Exec(context.Background(), "run", "--issue", fmt.Sprintf("%d", issueNum))
		if err != nil {
			t.Fatalf("runner.Exec: %v", err)
		}

		if result.ExitCode != 1 {
			t.Errorf("AC-004: want exit 1, got %d\nstdout: %s\nstderr: %s", result.ExitCode, result.Stdout, result.Stderr)
		}
		// BR-002: error must include cleanup hint.
		if !strings.Contains(result.Stderr, "close it first") {
			t.Errorf("AC-004 BR-002: want 'close it first' in stderr, got:\n%s\n"+
				"(DIAGNOSTIC: if runner exited 0, gh pr list may not return PRs whose head branch has been deleted — "+
				"AC-004 PR-path isolation requires modifying the forbidden collision order to place PR check before remote-branch check)",
				result.Stderr)
		}
	})
}
