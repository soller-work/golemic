// Package github provides GitHub API helpers for E2E tests.
//
// All helpers use the gh CLI under the hood and require gh to be
// installed and authenticated. Functions are designed to be idempotent
// for safe use in defer-based cleanup (BR-001).
package github

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// checkGhAvailable returns an error if gh is not in PATH or not authenticated.
func checkGhAvailable() error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh CLI not found in PATH: %w", err)
	}
	return nil
}

// retryGhCmd executes a gh command with retry logic for transient failures (IC-001).
// Retries up to 3 times on rate limit, timeout, temporary, or connection errors.
// Exponential backoff: 1s, 2s, 4s.
func retryGhCmd(args ...string) (string, error) {
	maxAttempts := 3
	backoffs := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		out, err := runGhCmdOnce(args...)
		if err == nil {
			return out, nil
		}

		lastErr = err

		// Check if error is transient (worth retrying).
		errStr := err.Error()
		isTransient := strings.Contains(errStr, "rate limit") ||
			strings.Contains(errStr, "timeout") ||
			strings.Contains(errStr, "temporary") ||
			strings.Contains(errStr, "connection reset") ||
			strings.Contains(errStr, "EOF")

		if !isTransient || attempt >= maxAttempts-1 {
			return "", err
		}

		// Wait before retry.
		time.Sleep(backoffs[attempt])
	}

	return "", lastErr
}

// runGhCmdOnce executes a gh command once without retry.
func runGhCmdOnce(args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = string(exitErr.Stderr)
		}
		return "", fmt.Errorf("gh %s failed: %w\nstderr: %s", strings.Join(args, " "), err, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

// testTimestamp returns a unique timestamp for test isolation (BR-002).
func testTimestamp() int64 {
	return time.Now().UnixNano()
}

// CreateTestIssue creates a GitHub issue in the given repository.
// Returns the issue number.
// Use as: defer DeleteTestIssue(repo, issueNum)
func CreateTestIssue(repo, title, body string) (int, error) {
	if err := checkGhAvailable(); err != nil {
		return 0, err
	}

	out, err := retryGhCmd(
		"issue", "create",
		"--repo", repo,
		"--title", title,
		"--body", body,
	)
	if err != nil {
		return 0, fmt.Errorf("gh issue create: %w", err)
	}

	// Parse issue URL to get number. Output looks like:
	// https://github.com/owner/repo/issues/42
	url := strings.TrimSpace(out)
	idx := strings.LastIndex(url, "/")
	if idx < 0 {
		return 0, fmt.Errorf("unexpected gh issue create output: %s", out)
	}
	num, err := strconv.Atoi(url[idx+1:])
	if err != nil {
		return 0, fmt.Errorf("failed to parse issue number from %q: %w", url, err)
	}
	return num, nil
}

// DeleteTestIssue deletes a GitHub issue by number. Safe to call multiple
// times (idempotent) — only swallows 404/not-found errors; returns all others.
func DeleteTestIssue(repo string, issueNum int) error {
	if err := checkGhAvailable(); err != nil {
		return err
	}

	// --yes flag skips confirmation prompt.
	cmd := exec.Command("gh", "issue", "delete",
		"--repo", repo,
		fmt.Sprintf("%d", issueNum),
		"--yes",
	)
	out, err := cmd.CombinedOutput()

	if err == nil {
		return nil // success
	}

	// Parse stderr to distinguish 404/not-found (idempotent) from other errors.
	stderr := string(out)
	if strings.Contains(stderr, "not found") ||
		strings.Contains(stderr, "Issue not found") ||
		strings.Contains(stderr, "Could not resolve to an issue") {
		return nil // issue already gone — idempotent success
	}

	// All other errors are non-idempotent and should be reported.
	return fmt.Errorf("failed to delete issue %d: %v\nstderr: %s", issueNum, err, stderr)
}

// AssertPRExists checks that a PR with the given number exists in the repo.
// Returns the PR number if found, or an error if not found or the query fails.
func AssertPRExists(repo string, prNum int) (int, error) {
	if err := checkGhAvailable(); err != nil {
		return 0, err
	}

	out, err := retryGhCmd(
		"pr", "view",
		fmt.Sprintf("%d", prNum),
		"--repo", repo,
		"--json", "number",
		"--jq", ".number",
	)
	if err != nil {
		return 0, fmt.Errorf("PR %d not found in %s: %w", prNum, repo, err)
	}

	found, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("failed to parse PR number from output %q: %w", out, err)
	}
	if found != prNum {
		return 0, fmt.Errorf("PR number mismatch: expected %d, got %d", prNum, found)
	}
	return found, nil
}

// AssertReviewExists checks whether a PR has at least one formal review.
// Returns true if a review exists, false otherwise.
// Returns false (not error) if the PR has no reviews — this allows callers
// to decide whether no reviews is a failure for their test scenario.
func AssertReviewExists(repo string, prNum int) bool {
	if err := checkGhAvailable(); err != nil {
		return false
	}

	out, err := retryGhCmd(
		"pr", "view",
		fmt.Sprintf("%d", prNum),
		"--repo", repo,
		"--json", "reviews",
		"--jq", ".reviews | length",
	)
	if err != nil {
		return false
	}

	count, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return false
	}
	return count > 0
}

// CreateTestPR creates a throwaway PR for assertion testing using an isolated
// git worktree. Operates in repoPath (golemic_e2e), not in the current directory.
// Returns the PR number.
//
// Creates a temporary worktree with an empty commit and pushes it as a branch,
// then opens a PR. Safe for parallel tests (each gets a unique branch name).
func CreateTestPR(repo string, repoPath string, issueNum int) (int, error) {
	if err := checkGhAvailable(); err != nil {
		return 0, err
	}

	uniqueBranch := fmt.Sprintf("test-e2e-%d-%d", issueNum, testTimestamp())
	tempWtDir := filepath.Join(repoPath, "worktrees", "_test_"+uniqueBranch)

	// Create an isolated worktree in a temp directory to avoid cwd mutation.
	// This ensures parallel test safety (P2-7).
	runCmd := func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Dir = repoPath
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s %s: %w\nout: %s", name, strings.Join(args, " "), err, string(out))
		}
		return nil
	}

	// Create worktree at detached HEAD of main.
	if err := runCmd("git", "worktree", "add", "--detach", tempWtDir, "main"); err != nil {
		return 0, fmt.Errorf("git worktree add: %w", err)
	}

	// Clean up worktree and remote branch on exit (AC-005, BR-001).
	defer func() {
		// F-2: set cmd.Dir so git finds the right repo.
		wtClean := exec.Command("git", "worktree", "remove", "--force", tempWtDir)
		wtClean.Dir = repoPath
		_ = wtClean.Run()

		// F-3: delete remote branch — idempotent (ignore error if branch never pushed).
		branchClean := exec.Command("git", "push", "origin", "--delete", uniqueBranch)
		branchClean.Dir = repoPath
		_ = branchClean.Run()
	}()

	// Create a branch in the worktree with an empty commit.
	cmdInWt := func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Dir = tempWtDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s %s in worktree: %w\nout: %s", name, strings.Join(args, " "), err, string(out))
		}
		return nil
	}

	if err := cmdInWt("git", "checkout", "-b", uniqueBranch); err != nil {
		return 0, fmt.Errorf("git checkout -b: %w", err)
	}

	if err := cmdInWt("git", "commit", "--allow-empty", "-m", "test: e2e assertion PR"); err != nil {
		return 0, fmt.Errorf("git commit: %w", err)
	}

	// Push the branch from the worktree's remote-tracking reference.
	if err := cmdInWt("git", "push", "origin", uniqueBranch); err != nil {
		return 0, fmt.Errorf("git push: %w", err)
	}

	// Create the PR.
	out, err := retryGhCmd(
		"pr", "create",
		"--repo", repo,
		"--head", uniqueBranch,
		"--base", "main",
		"--title", fmt.Sprintf("E2E Test PR for issue #%d", issueNum),
		"--body", "This is an automatically created test PR for E2E infrastructure testing.",
	)
	if err != nil {
		return 0, fmt.Errorf("gh pr create: %w", err)
	}

	// Parse PR number from URL.
	url := strings.TrimSpace(out)
	idx := strings.LastIndex(url, "/")
	if idx < 0 {
		return 0, fmt.Errorf("unexpected gh pr create output: %s", out)
	}
	num, err := strconv.Atoi(url[idx+1:])
	if err != nil {
		return 0, fmt.Errorf("failed to parse PR number from %q: %w", url, err)
	}
	return num, nil
}

// PRAuthor returns the login of the PR author (BR-002: used to verify dev ≠ reviewer).
func PRAuthor(repo string, prNum int) (string, error) {
	if err := checkGhAvailable(); err != nil {
		return "", err
	}
	out, err := retryGhCmd(
		"pr", "view",
		fmt.Sprintf("%d", prNum),
		"--repo", repo,
		"--json", "author",
		"--jq", ".author.login",
	)
	if err != nil {
		return "", fmt.Errorf("PRAuthor PR %d: %w", prNum, err)
	}
	login := strings.TrimSpace(out)
	if login == "" || login == "null" {
		return "", fmt.Errorf("no author found for PR %d", prNum)
	}
	return login, nil
}

// FirstReviewAuthor returns the login of the first formal reviewer (BR-002).
func FirstReviewAuthor(repo string, prNum int) (string, error) {
	if err := checkGhAvailable(); err != nil {
		return "", err
	}
	out, err := retryGhCmd(
		"pr", "view",
		fmt.Sprintf("%d", prNum),
		"--repo", repo,
		"--json", "reviews",
		"--jq", ".reviews[0].author.login",
	)
	if err != nil {
		return "", fmt.Errorf("FirstReviewAuthor PR %d: %w", prNum, err)
	}
	login := strings.TrimSpace(out)
	if login == "" || login == "null" {
		return "", fmt.Errorf("no review author found for PR %d", prNum)
	}
	return login, nil
}

// CloseTestPR closes a PR. Safe to call multiple times (idempotent) —
// only swallows "not found" errors; returns all others.
func CloseTestPR(repo string, prNum int) error {
	if err := checkGhAvailable(); err != nil {
		return err
	}

	cmd := exec.Command("gh", "pr", "close",
		fmt.Sprintf("%d", prNum),
		"--repo", repo,
	)
	out, err := cmd.CombinedOutput()

	if err == nil {
		return nil // success
	}

	// Only treat "not found" errors as idempotent.
	stderr := string(out)
	if strings.Contains(stderr, "not found") || strings.Contains(stderr, "no pull requests found") {
		return nil
	}

	// All other errors should be reported.
	return fmt.Errorf("failed to close PR %d: %v\nstderr: %s", prNum, err, stderr)
}
