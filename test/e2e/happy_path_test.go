//go:build e2e

// Package e2e contains end-to-end tests that exercise the full golemic
// orchestration loop against real GitHub infrastructure.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golemic/internal/eventlog"
	ghhelper "golemic/test/e2e/github"
	"golemic/test/e2e/harness"
)

// requireGh skips the test if gh CLI is not installed.
func requireGh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("gh"); err != nil {
		t.Skip("gh CLI not available — skipping E2E test:", err)
	}
}

// findE2EPath returns the local path to the golemic_e2e sandbox repository.
// Returns "" if not found; callers must skip when empty.
func findE2EPath() string {
	if p := os.Getenv("GOLEMIC_E2E_PATH"); p != "" {
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
			return p
		}
	}
	home := os.Getenv("HOME")
	for _, p := range []string{
		filepath.Join(home, "golemic_e2e"),
		filepath.Join(home, "Dev", "golemic_e2e"),
	} {
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
			return p
		}
	}
	return ""
}

// findBinary returns the path to the golemic binary.
// Returns "" if not found; callers must skip when empty.
func findBinary() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return harness.FindBinary(dir, os.Getenv("GOLEMIC_BINARY"))
}

// e2eRepo derives the GitHub owner/repo slug for the golemic_e2e sandbox.
// Uses the GOLEMIC_E2E_REPO env var when set; otherwise infers the owner
// from the current gh-authenticated user and appends "golemic_e2e".
func e2eRepo(t *testing.T) string {
	t.Helper()
	if r := os.Getenv("GOLEMIC_E2E_REPO"); r != "" {
		return r
	}
	out, err := exec.Command("gh", "api", "user", "--jq", ".login").Output()
	if err != nil {
		t.Skipf("cannot determine GitHub owner (gh not authenticated?): %v", err)
	}
	owner := strings.TrimSpace(string(out))
	if owner == "" {
		t.Skip("gh api user returned empty login — skipping E2E test")
	}
	return owner + "/golemic_e2e"
}

// parseRunID extracts the run ID from golemic's stdout.
// On success the runner prints just the run ID; on failure it prints "runs/<id>".
func parseRunID(stdout string) string {
	s := strings.TrimSpace(stdout)
	return strings.TrimPrefix(s, "runs/")
}

// prNumFromEvents reads the events.jsonl at path and returns the PR number
// from the pr_opened event, or 0 if not found.
func prNumFromEvents(t *testing.T, eventsPath string) int {
	t.Helper()
	events, err := eventlog.Reader{}.Read(eventsPath)
	if err != nil {
		t.Logf("prNumFromEvents: could not read %s: %v", eventsPath, err)
		return 0
	}
	for _, e := range events {
		if e.Type != eventlog.EventPROpened {
			continue
		}
		var payload struct {
			PRNumber string `json:"prNumber"`
		}
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(payload.PRNumber, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// hasEvent returns true if the events.jsonl at path contains at least one
// event with the given type.
func hasEvent(eventsPath, eventType string) bool {
	events, err := eventlog.Reader{}.Read(eventsPath)
	if err != nil {
		return false
	}
	for _, e := range events {
		if e.Type == eventType {
			return true
		}
	}
	return false
}

// deleteRemoteBranch deletes the remote branch from origin inside repoPath.
// Errors are swallowed — the branch may already be gone (idempotent).
func deleteRemoteBranch(t *testing.T, repoPath, branch string) {
	t.Helper()
	cmd := exec.Command("git", "push", "origin", "--delete", branch)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("cleanup: delete remote branch %q: %v\n%s", branch, err, out)
	}
}

// TestE2EHappyPath exercises the full golemic orchestration loop end-to-end:
//
//	PS-001 preflight → PS-002 issue load → PS-003 collision check →
//	PS-004 dev PR → PS-005 reviewer review → PS-006 exit 0.
//
// AC→test mapping:
//
//	AC-001 (full run succeeds)  → subtests AC-001/ExitCode, AC-001/PRExists,
//	                               AC-001/ReviewExists, AC-001/Events
//	AC-002 (distinct identities) → subtest AC-002/DifferentIdentities
func TestE2EHappyPath(t *testing.T) {
	// ---- Prerequisites (skip, not fail, when sandbox is absent) ----
	requireGh(t)

	e2ePath := findE2EPath()
	if e2ePath == "" {
		t.Skip("golemic_e2e sandbox not found — skipping E2E test (set GOLEMIC_E2E_PATH to override)")
	}

	binary := findBinary()
	if binary == "" {
		t.Skip("golemic binary not found — skipping E2E test (run `go build ./cmd/golemic` or set GOLEMIC_BINARY)")
	}

	runner, err := harness.NewRunner(e2ePath, binary)
	if err != nil {
		t.Skipf("E2E sandbox not ready (config/credentials missing): %v", err)
	}

	repo := e2eRepo(t)
	project := runner.Config().Project
	golemicDir := filepath.Join(runner.HomeDir(), ".golemic", project)

	// Verify the sandbox repo is actually accessible on GitHub before touching it.
	if out, err := exec.Command("gh", "repo", "view", repo, "--json", "name").CombinedOutput(); err != nil {
		t.Skipf("golemic_e2e GitHub repo %q not accessible — skipping E2E test: %v\n%s", repo, err, out)
	}

	// ---- Create fresh test issue ----
	title := fmt.Sprintf("E2E Happy Path %d", time.Now().UnixNano())
	issueNum, err := ghhelper.CreateTestIssue(repo, title, "Automated E2E happy path test — delete after run.")
	if err != nil {
		t.Fatalf("CreateTestIssue: %v", err)
	}
	// defer runs in LIFO: issue deletion is first-registered → runs last,
	// so PR / worktrees are already cleaned up before the issue is deleted.
	defer func() {
		if err := ghhelper.DeleteTestIssue(repo, issueNum); err != nil {
			t.Logf("cleanup: DeleteTestIssue %d: %v", issueNum, err)
		}
	}()

	// Worktree / run-dir cleanup — registered before PR so it runs after PR close.
	defer func() {
		if err := harness.RemoveWorktrees(golemicDir, e2ePath); err != nil {
			t.Logf("cleanup: RemoveWorktrees: %v", err)
		}
		if err := harness.CleanupRuns(golemicDir); err != nil {
			t.Logf("cleanup: CleanupRuns: %v", err)
		}
	}()

	// PR / branch cleanup — captured via closure so prNum can be set after run.
	var prNum int
	branchName := fmt.Sprintf("golemic/issue-%d", issueNum)
	defer func() {
		if prNum > 0 {
			if err := ghhelper.CloseTestPR(repo, prNum); err != nil {
				t.Logf("cleanup: CloseTestPR %d: %v", prNum, err)
			}
		}
		deleteRemoteBranch(t, e2ePath, branchName)
	}()

	// ---- Run golemic ----
	// No explicit timeout here: harness.Exec applies config.timeout_minutes
	// automatically when the context has no deadline (IC-002).
	result, err := runner.Exec(context.Background(), "run", "--issue", fmt.Sprintf("%d", issueNum))
	if err != nil {
		t.Fatalf("runner.Exec: %v", err)
	}

	// Parse run ID from stdout (used to locate events.jsonl).
	runID := parseRunID(result.Stdout)
	eventsPath := filepath.Join(golemicDir, "runs", runID, "events.jsonl")

	// Extract PR number from events so cleanup defers can use it.
	prNum = prNumFromEvents(t, eventsPath)

	// ---- AC-001: Full run succeeds ----
	t.Run("AC-001/ExitCode", func(t *testing.T) {
		if result.ExitCode != 0 {
			t.Errorf("want exit 0, got %d\nstdout: %s\nstderr: %s",
				result.ExitCode, result.Stdout, result.Stderr)
		}
	})

	if result.ExitCode != 0 {
		t.FailNow() // remaining assertions require a successful run
	}

	if prNum == 0 {
		t.Fatal("AC-001: pr_opened event missing or unparseable — cannot assert PR/review")
	}

	t.Run("AC-001/PRExists", func(t *testing.T) {
		if _, err := ghhelper.AssertPRExists(repo, prNum); err != nil {
			t.Errorf("AssertPRExists PR %d: %v", prNum, err)
		}
	})

	t.Run("AC-001/ReviewExists", func(t *testing.T) {
		if !ghhelper.AssertReviewExists(repo, prNum) {
			t.Errorf("no formal review found on PR %d (review_submitted missing or query failed)", prNum)
		}
	})

	t.Run("AC-001/Events", func(t *testing.T) {
		if !hasEvent(eventsPath, eventlog.EventPROpened) {
			t.Errorf("BR-003: pr_opened event not found in %s", eventsPath)
		}
		if !hasEvent(eventsPath, eventlog.EventReviewSubmitted) {
			t.Errorf("BR-003: review_submitted event not found in %s", eventsPath)
		}
	})

	// ---- AC-002: Dev and reviewer are different GitHub identities (BR-002) ----
	t.Run("AC-002/DifferentIdentities", func(t *testing.T) {
		prAuthor, err := ghhelper.PRAuthor(repo, prNum)
		if err != nil {
			t.Fatalf("PRAuthor: %v", err)
		}
		reviewAuthor, err := ghhelper.FirstReviewAuthor(repo, prNum)
		if err != nil {
			t.Fatalf("FirstReviewAuthor: %v", err)
		}
		if prAuthor == reviewAuthor {
			t.Errorf("BR-002 violated: PR author and reviewer are the same account (%q)", prAuthor)
		}
		t.Logf("PR author: %q  |  review author: %q", prAuthor, reviewAuthor)
	})
}
