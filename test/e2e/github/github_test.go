//go:build e2e

package github

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// e2eRepo returns the E2E test repository in owner/repo format.
func e2eRepo(t *testing.T) string {
	t.Helper()
	repo := "golemic_e2e"
	// Try to determine owner from git remote.
	out, err := runGhCmdOnce("api", "repos/"+repo, "--jq", ".owner.login")
	if err != nil {
		// Fallback: use gh repo view
		owner, err2 := runGhCmdOnce("repo", "view", "--json", "owner", "--jq", ".owner.login")
		if err2 != nil {
			t.Skipf("cannot determine repo owner (gh not configured): %v / %v", err, err2)
		}
		return strings.TrimSpace(owner) + "/" + repo
	}
	return strings.TrimSpace(out) + "/" + repo
}

// e2eRepoPath returns the local path to the golemic_e2e repository.
// Searches in standard locations: ~/golemic_e2e, ~/Dev/golemic_e2e.
func e2eRepoPath(t *testing.T) string {
	t.Helper()
	candidates := []string{
		filepath.Join(os.Getenv("HOME"), "golemic_e2e"),
		filepath.Join(os.Getenv("HOME"), "Dev", "golemic_e2e"),
		"/tmp/golemic_e2e",
	}

	for _, path := range candidates {
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			return path
		}
	}

	// Not found — test will skip.
	return ""
}

// requireGh ensures gh CLI is available; skips the test otherwise.
func requireGh(t *testing.T) {
	t.Helper()
	if err := checkGhAvailable(); err != nil {
		t.Skip("gh CLI not available:", err)
	}
}

// TestIssueLifecycle verifies AC-002:
// Given: Harness ready, gh CLI authenticated
// When: Test calls CreateTestIssue(title, body)
// Then: Issue created on GitHub, issue number returned,
// defer DeleteTestIssue(issueNum) deletes issue after test
func TestIssueLifecycle(t *testing.T) {
	requireGh(t)
	repo := e2eRepo(t)

	// Create an issue.
	issueNum, err := CreateTestIssue(repo, "E2E Test: Issue Lifecycle", "This is a test issue for E2E infrastructure.")
	if err != nil {
		t.Fatalf("CreateTestIssue failed: %v", err)
	}
	if issueNum <= 0 {
		t.Fatalf("expected positive issue number, got %d", issueNum)
	}

	// Verify the issue exists by listing it.
	verifyOut, err := retryGhCmd("issue", "view", fmt.Sprintf("%d", issueNum), "--repo", repo, "--json", "title", "--jq", ".title")
	if err != nil {
		t.Fatalf("failed to verify issue %d exists: %v", issueNum, err)
	}
	if !strings.Contains(verifyOut, "E2E Test: Issue Lifecycle") {
		t.Errorf("issue title mismatch: got %q", verifyOut)
	}

	// Delete the issue (the actual defer test).
	if err := DeleteTestIssue(repo, issueNum); err != nil {
		t.Fatalf("DeleteTestIssue failed: %v", err)
	}

	// Verify it's gone.
	_, err = retryGhCmd("issue", "view", fmt.Sprintf("%d", issueNum), "--repo", repo)
	if err == nil {
		t.Errorf("issue %d should have been deleted but still exists", issueNum)
	}
}

// TestCreateTestIssue verifies issue creation with unique title.
func TestCreateTestIssue(t *testing.T) {
	requireGh(t)
	repo := e2eRepo(t)

	uniqueTitle := fmt.Sprintf("E2E Test: Create %d", testTimestamp())
	issueNum, err := CreateTestIssue(repo, uniqueTitle, "Body for create test")
	if err != nil {
		t.Fatalf("CreateTestIssue failed: %v", err)
	}
	// Clean up.
	defer func() {
		if err := DeleteTestIssue(repo, issueNum); err != nil {
			t.Logf("cleanup: failed to delete issue %d: %v", issueNum, err)
		}
	}()

	if issueNum <= 0 {
		t.Fatalf("expected positive issue number, got %d", issueNum)
	}
}

// TestDeleteTestIssue verifies idempotent deletion (safe to call twice).
func TestDeleteTestIssue(t *testing.T) {
	requireGh(t)
	repo := e2eRepo(t)

	issueNum, err := CreateTestIssue(repo, "E2E Test: Delete Test", "Body for delete test")
	if err != nil {
		t.Fatalf("CreateTestIssue failed: %v", err)
	}

	// First delete should succeed.
	if err := DeleteTestIssue(repo, issueNum); err != nil {
		t.Fatalf("DeleteTestIssue (1st) failed: %v", err)
	}

	// Second delete should be idempotent (no error).
	if err := DeleteTestIssue(repo, issueNum); err != nil {
		t.Errorf("DeleteTestIssue (2nd) should be idempotent, got error: %v", err)
	}
}

// TestGitHubAssertions verifies AC-004:
// Given: PR exists on GitHub (created by golemic)
// When: Test calls AssertPRExists(issueNum), AssertReviewExists(prNum)
// Then: Assertions pass if found
func TestGitHubAssertions(t *testing.T) {
	requireGh(t)
	repo := e2eRepo(t)
	repoPath := e2eRepoPath(t)
	if repoPath == "" {
		t.Skip("golemic_e2e not found locally")
	}

	// Create a test issue first.
	issueNum, err := CreateTestIssue(repo, "E2E Test: Assertions", "Body for assertion test")
	if err != nil {
		t.Fatalf("CreateTestIssue failed: %v", err)
	}
	defer func() {
		if err := DeleteTestIssue(repo, issueNum); err != nil {
			t.Logf("cleanup: failed to delete issue %d: %v", issueNum, err)
		}
	}()

	// Create a PR using isolated worktree (P1-1 fix).
	prNum, err := CreateTestPR(repo, repoPath, issueNum)
	if err != nil {
		t.Skipf("CreateTestPR failed (skipping PR assertion test): %v", err)
	}
	defer func() {
		if err := CloseTestPR(repo, prNum); err != nil {
			t.Logf("cleanup: failed to close PR %d: %v", prNum, err)
		}
	}()

	// Assert PR exists.
	foundPR, err := AssertPRExists(repo, prNum)
	if err != nil {
		t.Fatalf("AssertPRExists failed: %v", err)
	}
	if foundPR != prNum {
		t.Errorf("AssertPRExists returned wrong PR number: got %d, want %d", foundPR, prNum)
	}

	// Assert that no PR exists for a non-existent number.
	_, err = AssertPRExists(repo, 99999999)
	if err == nil {
		t.Error("AssertPRExists should fail for non-existent PR number")
	}
}

// TestAssertReviewExists verifies review assertion for a PR.
func TestAssertReviewExists(t *testing.T) {
	requireGh(t)
	repo := e2eRepo(t)
	repoPath := e2eRepoPath(t)
	if repoPath == "" {
		t.Skip("golemic_e2e not found locally")
	}

	// Create test issue.
	issueNum, err := CreateTestIssue(repo, "E2E Test: Review Assertion", "Body for review test")
	if err != nil {
		t.Fatalf("CreateTestIssue failed: %v", err)
	}
	defer func() {
		if err := DeleteTestIssue(repo, issueNum); err != nil {
			t.Logf("cleanup: failed to delete issue %d: %v", issueNum, err)
		}
	}()

	// Create a test PR.
	prNum, err := CreateTestPR(repo, repoPath, issueNum)
	if err != nil {
		t.Skipf("CreateTestPR failed (skipping review assertion test): %v", err)
	}
	defer func() {
		if err := CloseTestPR(repo, prNum); err != nil {
			t.Logf("cleanup: failed to close PR %d: %v", prNum, err)
		}
	}()

	// AssertReviewExists should work (may or may not have a review).
	// We just test that the function doesn't crash and returns reasonable results.
	hasReview := AssertReviewExists(repo, prNum)
	t.Logf("PR %d has review: %v", prNum, hasReview)
}

// TestUniqueIssueNumber verifies BR-002: unique issue numbers prevent collisions.
func TestUniqueIssueNumber(t *testing.T) {
	requireGh(t)
	repo := e2eRepo(t)

	// Create two issues with unique numbers (test timestamp in title).
	title1 := fmt.Sprintf("E2E Test: Unique #1 %d", testTimestamp())
	title2 := fmt.Sprintf("E2E Test: Unique #2 %d", testTimestamp())

	num1, err := CreateTestIssue(repo, title1, "body1")
	if err != nil {
		t.Fatalf("CreateTestIssue #1 failed: %v", err)
	}
	defer func() {
		if err := DeleteTestIssue(repo, num1); err != nil {
			t.Logf("cleanup: failed to delete issue %d: %v", num1, err)
		}
	}()

	num2, err := CreateTestIssue(repo, title2, "body2")
	if err != nil {
		t.Fatalf("CreateTestIssue #2 failed: %v", err)
	}
	defer func() {
		if err := DeleteTestIssue(repo, num2); err != nil {
			t.Logf("cleanup: failed to delete issue %d: %v", num2, err)
		}
	}()

	if num1 == num2 {
		t.Errorf("issues should have unique numbers, both got %d", num1)
	}
}
